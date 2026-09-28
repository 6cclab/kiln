// Package faux implements a deterministic, scripted model server used by
// harness tests. It speaks enough of the Anthropic Messages API and the
// OpenAI chat-completions API (streaming and non-streaming) for a real
// client to drive a conversation against it, while its behavior is fully
// controlled by a small YAML script.
//
// # Endpoints
//
// The server exposes the following HTTP endpoints:
//
//	POST /v1/messages              Anthropic Messages API (streaming via
//	                                "stream": true, or a single JSON body).
//	                                The request's "model" field selects
//	                                which scripted model's cursor is
//	                                consumed; a model with no script gets
//	                                a 400 JSON error naming the model and
//	                                the scripted ones.
//	GET  /v1/models                Anthropic-style model list, one entry
//	                                per scripted model.
//	POST /v1/chat/completions      OpenAI chat-completions API (streaming
//	                                via "stream": true, or a single JSON
//	                                body). Routed by "model" exactly like
//	                                /v1/messages.
//	GET  /v1/models                shared with the Anthropic list; the
//	                                response format matches whichever
//	                                client asked (both use the same list
//	                                of scripted models).
//	GET  /_faux/requests           JSON array of every request recorded
//	                                so far (see Request), across all
//	                                models.
//	POST /_faux/reset              Clears recorded requests and rewinds
//	                                every scripted model's cursor to its
//	                                first step.
//	POST /_faux/script             Body is a YAML script; hot-swaps the
//	                                entire model set (replacing whichever
//	                                models were previously scripted) and
//	                                rewinds every cursor to its first
//	                                step.
//	GET  /_faux/state               Per scripted model: current step
//	                                index, whether that model's script is
//	                                exhausted, any recorded errors (e.g.
//	                                on_tool_result mismatches), and how
//	                                many disconnect_after faults have
//	                                fired. See MultiState.
//
// # Script format
//
// A script is a YAML document. The common, single-model form names one
// model and an ordered list of steps:
//
//	model: faux-1
//	steps:
//	  - text: "I'll look at the file."
//	  - thinking: "the add function subtracts"
//	  - tool_call: {name: read, args: {path: src/math.js}, id: tc1}
//	  - on_tool_result: tc1
//	    then:
//	      - tool_call: {name: edit, args: {path: src/math.js, edits: [...]}, id: tc2}
//	  - on_tool_result: tc2
//	    then:
//	      - text: "Fixed."
//	        usage: {input: 812, output: 34}
//	  - error: {status: 529, type: overloaded_error, message: "Overloaded"}
//	  - delay: 50ms
//
// The multi-model form gives several models each their own independent
// step list, and therefore their own independent cursor:
//
//	models:
//	  faux-1:
//	    - text: "parent turn"
//	  faux-2:
//	    - tool_call: {name: read, args: {path: x}, id: tc1}
//	    - on_tool_result: tc1
//	      then: [{text: "done"}]
//
// The single-model form is exactly the multi-model form with one entry
// (models: {<model>: <steps>}); a script using it behaves identically to
// one written directly in the multi-model form.
//
// A tool_calls step (as opposed to the singular tool_call) takes a list of
// tool call specs and emits them all as one assistant turn with N tool_use
// blocks, still ending that turn with a tool-use stop reason:
//
//	steps:
//	  - tool_calls:
//	      - {name: read, args: {path: a.js}, id: tc1}
//	      - {name: read, args: {path: b.js}, id: tc2}
//	  - on_tool_results: [tc1, tc2]
//	    then:
//	      - text: "both read"
//
// on_tool_results (plural) gates the next turn on the following request
// containing a tool result for every listed id, in any order; it is the
// multi-id counterpart of on_tool_result and the two may be mixed on one
// step.
//
// Each HTTP request to /v1/messages or /v1/chat/completions is dispatched
// to the model named in the request and consumes one "turn" from that
// model's script. A turn is made up of the consecutive content steps
// (text, thinking, tool_call, tool_calls, delay, usage) that precede the
// next turn boundary. A tool_call or tool_calls step always ends its turn,
// and the response carries a tool-use stop reason; a turn with no tool
// call ends with a normal end-of-turn stop reason. A step carrying
// disconnect_after (see "Fault injection" below) likewise always ends its
// turn, even a plain text or thinking step that would otherwise merge
// with the steps around it.
//
// An on_tool_result or on_tool_results step does not itself consume a
// request. Instead it gates the *next* turn (built from its then: steps)
// on the following request containing a tool result for every given id.
// If the next request is missing a matching tool result for one or more
// ids, each mismatch is recorded (via /_faux/state) rather than causing
// the server to fail the request.
//
// An error step consumes exactly one request and returns the given HTTP
// status with a provider-shaped error body; the next request proceeds
// normally with the following step.
//
// A delay step sleeps for the given duration before the turn's response
// is written.
//
// Consecutive text, thinking and delay steps merge into one turn. A step
// carrying end_turn: true closes its turn, so the next step answers the
// following request:
//
//	steps:
//	  - text: "first reply"
//	    end_turn: true
//	  - text: "second reply"
//
// A text step may carry chunk_delay (a duration) to sleep between its
// streamed chunks, holding a partly arrived reply on screen.
//
// # Fault injection
//
// A text or thinking step may also carry disconnect_after, a mid-stream
// (or mid-response) cut:
//
//	steps:
//	  - text: "partial reply, then the connection dies"
//	    disconnect_after: 20
//	  - text: "the retry lands here"
//
// disconnect_after: 20 streams the response normally up to roughly 20
// bytes of the response body (rounded up to whatever write happens to
// cross that count; for a streaming response that's the nearest SSE
// frame boundary) and then closes the connection instead of completing
// it. disconnect_after: 200ms instead waits 200ms from the moment the
// response begins and then closes the connection without writing any of
// the turn's body first, since it's a hard deadline rather than a byte
// count: for the deterministically small, in-memory responses this
// server generates, "200ms in" and "before a single byte goes out" are
// normally the same instant. Either form closes the underlying TCP
// connection instead of completing the response: no terminating SSE
// event for a streaming request, no closing bytes of the JSON body for a
// non-streaming one. This is done via http.Hijacker, taking over and
// closing the raw connection; if the ResponseWriter doesn't support
// hijacking, the handler instead panics with http.ErrAbortHandler after
// flushing whatever was already written, which net/http recognizes
// specially and aborts the response the same way, without logging a
// stack trace. Either path leaves a real HTTP client seeing an abrupt EOF
// or a connection-reset error partway through reading the response, not
// a well-formed one.
//
// A disconnect_after step always ends its own turn, the same way a
// tool_call does, and still consumes it like any other step (the cursor
// advances), so a client that retries after the cut receives the *next*
// scripted step, not a repeat of the cut one. Script "cut once, then
// succeed" as two separate steps, as in the example above. The number of
// times a disconnect_after fault has fired for a model is reported in
// /_faux/state as that model's disconnects count.
//
// A tool_call (or an entry inside tool_calls) may carry raw_args instead
// of args:
//
//	steps:
//	  - tool_call: {name: read, raw_args: '{"path": ', id: tc1}
//
// raw_args is spliced into the response verbatim as the tool call's
// input/arguments, instead of being marshaled from a Go value, so a
// script can emit intentionally invalid JSON to exercise a client's
// error handling. args and raw_args are mutually exclusive; a script
// setting both fails to load. For Anthropic responses, the tool_use
// block's "input" field is normally a JSON object; raw_args bypasses
// encoding/json for that field by marshaling the response with a
// placeholder in its place and then splicing the raw text into the
// marshaled bytes (encoding/json cannot itself emit invalid JSON), for
// both the non-streaming body and, in the streaming form, the
// input_json_delta partial_json chunks (which are plain text already, so
// no splicing is needed there). For OpenAI responses, function.arguments
// is always a JSON string field, so raw_args is simply used as that
// string's value directly, in both the streaming and non-streaming
// forms.
//
// Once a model's script is fully consumed, further requests to that model
// receive a fixed reply "(faux: script exhausted)" with a normal
// end-of-turn stop reason, and the exhaustion is recorded in /_faux/state
// under that model; other scripted models are unaffected.
//
// A raw_blocks step emits a list of objects verbatim as Anthropic content
// blocks, for provider-native blocks this package has no first-class step
// for (server_tool_use, web_search_tool_result):
//
//	steps:
//	  - raw_blocks:
//	      - {type: server_tool_use, id: srvtoolu_1, name: web_search, input: {query: "go generics"}}
//	      - {type: web_search_tool_result, tool_use_id: srvtoolu_1, content: [{type: web_search_result, url: "https://go.dev", title: "Go"}]}
//	    stop_reason: pause_turn
//
// A "server_tool_use"-typed block streams its "input" field via
// input_json_delta chunks, matching the real API; every other type is sent
// complete in content_block_start, with no deltas. A raw_blocks step always
// ends its own turn, like a tool_call. stop_reason overrides the turn's
// normal end_turn/tool_use stop reason inference (e.g. "pause_turn" for a
// long server-tool turn that was cut for interim delivery) and may be set
// on any step, not just raw_blocks. Both are Anthropic-only; the OpenAI
// handler ignores them.
//
// The server is deterministic: streamed content is split into small,
// fixed-size chunks, ids are derived from script step ids and a request
// sequence number instead of randomness, and no wall-clock value is
// embedded in any response.
package faux

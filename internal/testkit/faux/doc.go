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
//	                                exhausted, and any recorded errors
//	                                (e.g. on_tool_result mismatches). See
//	                                MultiState.
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
// call ends with a normal end-of-turn stop reason.
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
// Once a model's script is fully consumed, further requests to that model
// receive a fixed reply "(faux: script exhausted)" with a normal
// end-of-turn stop reason, and the exhaustion is recorded in /_faux/state
// under that model; other scripted models are unaffected.
//
// The server is deterministic: streamed content is split into small,
// fixed-size chunks, ids are derived from script step ids and a request
// sequence number instead of randomness, and no wall-clock value is
// embedded in any response.
package faux

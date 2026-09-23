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
//	GET  /v1/models                Anthropic-style model list.
//	POST /v1/chat/completions      OpenAI chat-completions API (streaming
//	                                via "stream": true, or a single JSON
//	                                body).
//	GET  /v1/models                shared with the Anthropic list; the
//	                                response format matches whichever
//	                                client asked (both use the same list
//	                                of scripted models).
//	GET  /_faux/requests           JSON array of every request recorded
//	                                so far (see Request).
//	POST /_faux/reset              Clears recorded requests and rewinds
//	                                the script to its first step.
//	POST /_faux/script             Body is a YAML script; hot-swaps the
//	                                script and rewinds to its first step.
//	GET  /_faux/state               Current step index, whether the script
//	                                is exhausted, and any recorded errors
//	                                (e.g. on_tool_result mismatches).
//
// # Script format
//
// A script is a YAML document with a model name and an ordered list of
// steps:
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
// Each HTTP request to /v1/messages or /v1/chat/completions consumes one
// "turn" from the script. A turn is made up of the consecutive content
// steps (text, thinking, tool_call, delay, usage) that precede the next
// turn boundary. A tool_call step always ends its turn, and the response
// carries a tool-use stop reason; a turn with no tool_call ends with a
// normal end-of-turn stop reason.
//
// An on_tool_result step does not itself consume a request. Instead it
// gates the *next* turn (built from its then: steps) on the following
// request containing a tool result for the given id. If the next request
// does not contain a matching tool result, the mismatch is recorded (via
// /_faux/state) rather than causing the server to fail the request.
//
// An error step consumes exactly one request and returns the given HTTP
// status with a provider-shaped error body; the next request proceeds
// normally with the following step.
//
// A delay step sleeps for the given duration before the turn's response
// is written.
//
// Once every step has been consumed, further requests receive a fixed
// reply "(faux: script exhausted)" with a normal end-of-turn stop reason,
// and the exhaustion is recorded in /_faux/state.
//
// The server is deterministic: streamed content is split into small,
// fixed-size chunks, ids are derived from script step ids and a request
// sequence number instead of randomness, and no wall-clock value is
// embedded in any response.
package faux

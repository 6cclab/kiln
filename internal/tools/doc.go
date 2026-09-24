// Package tools implements the built-in tools every harness session gets
// for free: bash, read, edit and write. Each is ported from
// pi-agent-core's tools (node_modules/@earendil-works/pi-agent-core/dist/
// harness/tools/{bash,read,edit,edit-diff,write,image,path-utils}.js) —
// same schemas, same description text, same output formats, same fuzzy
// text matching for edit — against internal/execenv.Env instead of pi's
// NodeExecutionEnv, and internal/tool.Tool instead of pi's
// AgentHarnessTool.
package tools

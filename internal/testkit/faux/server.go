package faux

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
)

// Options configures a new Server.
type Options struct {
	// Script is the initial script to serve. If empty, LoadScript or
	// LoadScriptYAML must be called before Start.
	ScriptPath string
	ScriptYAML string

	// Addr is the address to listen on, e.g. "127.0.0.1:0" for a random
	// free port. Defaults to "127.0.0.1:0".
	Addr string

	// RecordTo, if set, appends every recorded request as JSONL to this
	// path. Defaults to the HARNESS_FAUX_RECORD environment variable
	// when empty.
	RecordTo string
}

// State reports the faux engine's current position in its script.
type State struct {
	StepIndex int      `json:"stepIndex"`
	Exhausted bool     `json:"exhausted"`
	Errors    []string `json:"errors,omitempty"`
}

// engineState holds the script execution state, guarded by mu.
type engineState struct {
	mu         sync.Mutex
	model      string
	turns      []turn
	pos        int
	exhausted  bool
	mismatches []string
}

func (e *engineState) load(s *Script) error {
	turns, err := flattenSteps(s.Steps)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.model = s.Model
	e.turns = turns
	e.pos = 0
	e.exhausted = false
	e.mismatches = nil
	return nil
}

func (e *engineState) resetPosition() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pos = 0
	e.exhausted = false
	e.mismatches = nil
}

func (e *engineState) modelName() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.model
}

// consume returns the next turn to execute given the set of tool_result
// ids present in the triggering request. It never blocks or errors on a
// mismatched tool_result id; it records it instead.
func (e *engineState) consume(presentToolResultIDs map[string]bool) (t turn, exhausted bool, index int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pos >= len(e.turns) {
		e.exhausted = true
		return turn{}, true, e.pos
	}
	t = e.turns[e.pos]
	index = e.pos
	e.pos++
	if t.requireToolResult != "" && !toolResultPresent(presentToolResultIDs, t.requireToolResult) {
		e.mismatches = append(e.mismatches, fmt.Sprintf(
			"turn %d: expected a tool_result for id %q, none found in request", index, t.requireToolResult))
	}
	return t, false, index
}

// toolResultPresent reports whether the request's tool_result ids include
// the scripted id, accounting for the wire-format id prefix ("toolu_" for
// Anthropic, "call_" for OpenAI) that a real client echoes back exactly
// as it was given.
func toolResultPresent(present map[string]bool, rawID string) bool {
	return present[rawID] || present[anthropicToolID(rawID)] || present[openAICallID(rawID)]
}

func (e *engineState) state() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	errs := make([]string, len(e.mismatches))
	copy(errs, e.mismatches)
	return State{StepIndex: e.pos, Exhausted: e.exhausted, Errors: errs}
}

// Server is a deterministic scripted model server speaking the Anthropic
// Messages API and the OpenAI chat-completions API. See the package
// documentation for the script format and endpoints.
type Server struct {
	opts     Options
	engine   *engineState
	rec      *recorder
	mux      *http.ServeMux
	httpSrv  *http.Server
	listener net.Listener
	addr     string
}

// New creates a Server. Call Start to begin listening.
func New(opts Options) (*Server, error) {
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:0"
	}
	if opts.RecordTo == "" {
		opts.RecordTo = os.Getenv("HARNESS_FAUX_RECORD")
	}
	s := &Server{
		opts:   opts,
		engine: &engineState{model: "faux-1"},
		rec:    newRecorder(opts.RecordTo),
	}
	switch {
	case opts.ScriptYAML != "":
		if err := s.LoadScriptYAML(opts.ScriptYAML); err != nil {
			return nil, err
		}
	case opts.ScriptPath != "":
		if err := s.LoadScript(opts.ScriptPath); err != nil {
			return nil, err
		}
	default:
		// Start with an empty script; requests will be immediately
		// "exhausted" until a script is loaded.
		if err := s.engine.load(&Script{Model: "faux-1"}); err != nil {
			return nil, err
		}
	}
	s.mux = http.NewServeMux()
	s.routes()
	return s, nil
}

func (s *Server) routes() {
	s.mux.HandleFunc("POST /v1/messages", s.handleAnthropicMessages)
	s.mux.HandleFunc("POST /v1/chat/completions", s.handleOpenAIChatCompletions)
	s.mux.HandleFunc("GET /v1/models", s.handleModels)
	s.mux.HandleFunc("GET /_faux/requests", s.handleFauxRequests)
	s.mux.HandleFunc("POST /_faux/reset", s.handleFauxReset)
	s.mux.HandleFunc("POST /_faux/script", s.handleFauxScript)
	s.mux.HandleFunc("GET /_faux/state", s.handleFauxState)
}

// Start begins listening and serving in a background goroutine, and
// returns the address it is listening on.
func (s *Server) Start() (string, error) {
	l, err := net.Listen("tcp", s.opts.Addr)
	if err != nil {
		return "", fmt.Errorf("faux: listen: %w", err)
	}
	s.listener = l
	s.addr = l.Addr().String()
	s.httpSrv = &http.Server{Handler: s.mux}
	go func() {
		_ = s.httpSrv.Serve(l)
	}()
	return s.addr, nil
}

// Close shuts down the server.
func (s *Server) Close() error {
	if s.httpSrv == nil {
		return nil
	}
	return s.httpSrv.Shutdown(context.Background())
}

// LoadScript loads a YAML script from disk and rewinds execution to the
// first step.
func (s *Server) LoadScript(path string) error {
	sc, err := loadScriptFile(path)
	if err != nil {
		return err
	}
	return s.engine.load(sc)
}

// LoadScriptYAML loads a YAML script from a string and rewinds execution
// to the first step.
func (s *Server) LoadScriptYAML(doc string) error {
	sc, err := parseScriptYAML(doc)
	if err != nil {
		return err
	}
	return s.engine.load(sc)
}

// Requests returns every request recorded so far, in order.
func (s *Server) Requests() []Request {
	return s.rec.all()
}

// Reset clears recorded requests and rewinds the script to its first
// step.
func (s *Server) Reset() {
	s.rec.reset()
	s.engine.resetPosition()
}

// Main is a standalone entry point so that a future harness-drive command
// can run the faux server out-of-process. args[0], if present, is treated
// as a script path; the -addr flag (default "127.0.0.1:0") sets the
// listen address. It blocks until the process receives a signal or ctx
// is done, whichever the caller arranges; here it simply serves forever
// on the given address and writes the chosen address to stdout.
func Main(args []string) error {
	opts := Options{Addr: "127.0.0.1:0"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-addr":
			i++
			if i < len(args) {
				opts.Addr = args[i]
			}
		default:
			opts.ScriptPath = args[i]
		}
	}
	srv, err := New(opts)
	if err != nil {
		return err
	}
	addr, err := srv.Start()
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, addr)
	select {}
}

// --- shared HTTP helpers -----------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func readBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	model := s.engine.modelName()
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data": []map[string]any{
			{"id": model, "object": "model", "created": 0, "owned_by": "faux"},
		},
	})
}

func (s *Server) handleFauxRequests(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Requests())
}

func (s *Server) handleFauxReset(w http.ResponseWriter, r *http.Request) {
	s.Reset()
	writeJSON(w, http.StatusOK, map[string]string{"status": "reset"})
}

func (s *Server) handleFauxScript(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.LoadScriptYAML(string(body)); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "loaded"})
}

func (s *Server) handleFauxState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.engine.state())
}

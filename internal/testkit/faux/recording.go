package faux

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// ToolSpec is the recorded shape of a tool definition from an incoming
// request, in whichever wire format the client used.
type ToolSpec struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema,omitempty"`
}

// Request is a recorded snapshot of one HTTP request handled by the
// server.
type Request struct {
	Seq      int             `json:"seq"`
	Time     time.Time       `json:"time"`
	Method   string          `json:"method"`
	Path     string          `json:"path"`
	Protocol string          `json:"protocol"` // "anthropic" or "openai"
	Model    string          `json:"model"`
	System   string          `json:"system,omitempty"`
	Tools    []ToolSpec      `json:"tools,omitempty"`
	Messages json.RawMessage `json:"messages,omitempty"`
	Stream   bool            `json:"stream"`
	Body     json.RawMessage `json:"body"`
}

// recorder captures requests in memory and, optionally, to a JSONL file.
type recorder struct {
	mu       sync.Mutex
	requests []Request
	nextSeq  int
	recordTo string
}

func newRecorder(recordTo string) *recorder {
	return &recorder{recordTo: recordTo}
}

func (r *recorder) record(req Request) Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextSeq++
	req.Seq = r.nextSeq
	r.requests = append(r.requests, req)
	if r.recordTo != "" {
		r.appendJSONL(req)
	}
	return req
}

func (r *recorder) appendJSONL(req Request) {
	f, err := os.OpenFile(r.recordTo, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	_ = enc.Encode(req)
}

func (r *recorder) all() []Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Request, len(r.requests))
	copy(out, r.requests)
	return out
}

func (r *recorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = nil
	r.nextSeq = 0
}

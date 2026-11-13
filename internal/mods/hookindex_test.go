package mods

import (
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
)

// TestRendersComponentFollowsTheMatchers: a transcript row waits on a mod only
// when a ui.render hook names its component (by string or an any-of array) or
// names none.
func TestRendersComponentFollowsTheMatchers(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "rowdrawer")
	for c, want := range map[string]bool{
		"CommandOutput": true, "UserMessage": true, "ToolGroup": true, "InfoNotice": true,
		"Pane": false, "Spinner": false,
	} {
		if got := h.e.RendersComponent(c); got != want {
			t.Errorf("RendersComponent(%q) = %v, want %v", c, got, want)
		}
	}
	if !h.e.Has("ui.render") || h.e.Has("tool.call") {
		t.Errorf("Has: ui.render %v, tool.call %v; want true, false", h.e.Has("ui.render"), h.e.Has("tool.call"))
	}

	// A matcher with no component, or a RegExp one, may draw anything.
	vm := goja.New()
	for src, want := range map[string]string{
		`({requestId: "x"})`:        "",
		`({component: /Tool.*/})`:   "",
		`({component: ["A", /B/]})`: "",
		`({component: ["A", "B"]})`: "A,B",
		`({component: "Pane"})`:     "Pane",
	} {
		v, err := vm.RunString(src)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(namedComponents(v.ToObject(vm)), ","); got != want {
			t.Errorf("namedComponents(%s) = %q, want %q", src, got, want)
		}
	}
	if got := namedComponents(nil); len(got) != 1 || got[0] != "" {
		t.Errorf("namedComponents(nil) = %q, want [\"\"]", got)
	}

	var nilEngine *Engine
	if nilEngine.RendersComponent("Pane") || nilEngine.Has("ui.render") {
		t.Error("a nil engine reported hooks")
	}
}

// TestHasDoesNotWaitOnABusyWorker: Has and RendersComponent are called from
// the Bubble Tea goroutine, so they must answer while a hook holds the worker.
func TestHasDoesNotWaitOnABusyWorker(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "rowdrawer")
	release := make(chan struct{})
	held := make(chan struct{})
	if !h.e.w.post(func() { close(held); <-release }) {
		t.Fatal("could not post to the worker")
	}
	defer close(release)
	<-held

	done := make(chan bool, 1)
	go func() { done <- h.e.Has("ui.render") && h.e.RendersComponent("CommandOutput") }()
	select {
	case ok := <-done:
		if !ok {
			t.Error("Has/RendersComponent answered false for a hooked component")
		}
	case <-time.After(time.Second):
		t.Fatal("Has waited on the busy worker")
	}
}

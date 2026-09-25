package statusline

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRunEmptyCommandReturnsNoRows(t *testing.T) {
	rows, err := Run(context.Background(), "", Input{}, time.Second)
	if err != nil {
		t.Fatalf("Run(\"\") error = %v, want nil", err)
	}
	if rows != nil {
		t.Fatalf("Run(\"\") rows = %v, want nil", rows)
	}

	rows, err = Run(context.Background(), "   ", Input{}, time.Second)
	if err != nil {
		t.Fatalf("Run(whitespace) error = %v, want nil", err)
	}
	if rows != nil {
		t.Fatalf("Run(whitespace) rows = %v, want nil", rows)
	}
}

func TestRunSingleLineOutput(t *testing.T) {
	rows, err := Run(context.Background(), "echo hello", Input{}, time.Second)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []string{"hello"}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("Run() rows = %v, want %v", rows, want)
	}
}

func TestRunMultiLineOutputSplit(t *testing.T) {
	rows, err := Run(context.Background(), "printf 'line1\\nline2\\nline3\\n'", Input{}, time.Second)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []string{"line1", "line2", "line3"}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("Run() rows = %v, want %v", rows, want)
	}
}

func TestRunTrailingNewlinesTrimmed(t *testing.T) {
	rows, err := Run(context.Background(), "printf 'only line\\n\\n\\n'", Input{}, time.Second)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []string{"only line"}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("Run() rows = %v, want %v", rows, want)
	}
}

func TestRunOutputOnlyNewlinesReturnsNoRows(t *testing.T) {
	rows, err := Run(context.Background(), "printf '\\n\\n'", Input{}, time.Second)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if rows != nil {
		t.Fatalf("Run() rows = %v, want nil", rows)
	}
}

func TestRunNonZeroExitReturnsError(t *testing.T) {
	rows, err := Run(context.Background(), "exit 1", Input{}, time.Second)
	if err == nil {
		t.Fatalf("Run(exit 1) error = nil, want error")
	}
	if rows != nil {
		t.Fatalf("Run(exit 1) rows = %v, want nil", rows)
	}
}

func TestRunTimeoutReturnsError(t *testing.T) {
	rows, err := Run(context.Background(), "sleep 5", Input{}, 20*time.Millisecond)
	if err == nil {
		t.Fatalf("Run(sleep past timeout) error = nil, want error")
	}
	if rows != nil {
		t.Fatalf("Run(sleep past timeout) rows = %v, want nil", rows)
	}
}

// TestRunPipesJSONInputOnStdin verifies the command receives the marshalled
// Input on stdin, with HookEventName forced to "Status" regardless of what
// the caller set, matching Claude Code's statusLine contract.
func TestRunPipesJSONInputOnStdin(t *testing.T) {
	in := Input{
		HookEventName: "SomethingElse",
		SessionID:     "sess-1",
		Cwd:           "/tmp/work",
		Model:         Model{ID: "model-x", DisplayName: "Model X"},
		Workspace:     Workspace{CurrentDir: "/tmp/work", ProjectDir: "/tmp"},
	}
	rows, err := Run(context.Background(), "cat", in, time.Second)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("Run() rows = %v, want exactly 1 line of JSON", rows)
	}
	out := rows[0]
	if !strings.Contains(out, `"hook_event_name":"Status"`) {
		t.Fatalf("Run() stdin JSON = %s, want hook_event_name forced to Status", out)
	}
	if !strings.Contains(out, `"session_id":"sess-1"`) {
		t.Fatalf("Run() stdin JSON = %s, want session_id sess-1", out)
	}
	if !strings.Contains(out, `"id":"model-x"`) {
		t.Fatalf("Run() stdin JSON = %s, want model id model-x", out)
	}
}

func TestRunContextCancelledBeforeCallReturnsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rows, err := Run(ctx, "echo hi", Input{}, time.Second)
	if err == nil {
		t.Fatalf("Run() with cancelled ctx error = nil, want error")
	}
	if rows != nil {
		t.Fatalf("Run() with cancelled ctx rows = %v, want nil", rows)
	}
}

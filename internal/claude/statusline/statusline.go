// Package statusline runs Claude Code's configured statusLine command and
// returns its output for the TUI to render below the input box. Claude
// Code invokes the command with a JSON status payload on stdin and renders
// its stdout; this package mirrors that contract so a user's existing
// statusline-command.sh works unchanged.
package statusline

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

// Model is the "model" object in the status payload.
type Model struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// Workspace is the "workspace" object in the status payload.
type Workspace struct {
	CurrentDir string `json:"current_dir"`
	ProjectDir string `json:"project_dir"`
}

// Cost is the "cost" object in the status payload.
type Cost struct {
	TotalCostUSD      float64 `json:"total_cost_usd"`
	TotalDurationMS   int64   `json:"total_duration_ms"`
	TotalAPIDuration  int64   `json:"total_api_duration_ms"`
	TotalLinesAdded   int     `json:"total_lines_added"`
	TotalLinesRemoved int     `json:"total_lines_removed"`
}

// Usage is the "current_usage" object nested in ContextWindow.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// ContextWindow is the "context_window" object in the status payload.
type ContextWindow struct {
	ContextWindowSize int     `json:"context_window_size"`
	UsedPercentage    float64 `json:"used_percentage"`
	TotalInputTokens  int     `json:"total_input_tokens"`
	TotalOutputTokens int     `json:"total_output_tokens"`
	CurrentUsage      Usage   `json:"current_usage"`
}

// Input is the JSON object piped to the statusLine command on stdin,
// matching Claude Code's statusLine contract (the subset the harness can
// populate; fields sourced from the Anthropic API in Claude Code — rate
// limits, extra-usage credits — are omitted, and a well-written script
// treats them as absent).
type Input struct {
	HookEventName  string    `json:"hook_event_name"`
	SessionID      string    `json:"session_id"`
	TranscriptPath string    `json:"transcript_path"`
	Cwd            string    `json:"cwd"`
	Model          Model     `json:"model"`
	Workspace      Workspace `json:"workspace"`
	Version        string    `json:"version"`
	OutputStyle    struct {
		Name string `json:"name"`
	} `json:"output_style"`
	Cost              Cost          `json:"cost"`
	ContextWindow     ContextWindow `json:"context_window"`
	Exceeds200kTokens bool          `json:"exceeds_200k_tokens"`
}

// Run executes command via "sh -c", pipes in as JSON on its stdin, and
// returns the trimmed stdout as the status line's rows. It never blocks
// longer than timeout. An error (non-zero exit, timeout, no command) yields
// no rows rather than a broken screen.
func Run(ctx context.Context, command string, in Input, timeout time.Duration) ([]string, error) {
	if strings.TrimSpace(command) == "" {
		return nil, nil
	}
	in.HookEventName = "Status"
	data, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stdin = bytes.NewReader(data)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	text := strings.TrimRight(out.String(), "\n")
	if text == "" {
		return nil, nil
	}
	return strings.Split(text, "\n"), nil
}

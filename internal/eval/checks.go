package eval

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/testkit/faux"
	"github.com/andrepato/harness/internal/testkit/scenario"
)

// ResultJSON mirrors the shape of `kiln -p --output-format json`'s stdout
// (internal/cli.printJSON, which is unexported). It is decoded straight
// from the child process's stdout by the runner.
type ResultJSON struct {
	OK        bool             `json:"ok"`
	Text      string           `json:"text"`
	ToolCalls []ToolCallRecord `json:"toolCalls"`
	Blocked   []string         `json:"blocked"`
	Usage     struct {
		Input      int `json:"input"`
		Output     int `json:"output"`
		CacheRead  int `json:"cache_read"`
		CacheWrite int `json:"cache_write"`
	} `json:"usage"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	DurationMS   int64   `json:"duration_ms"`
	NumTurns     int     `json:"num_turns"`
	NumToolCalls int     `json:"num_tool_calls"`
	Reason       string  `json:"reason,omitempty"`
}

// ToolCallRecord is one entry of ResultJSON.ToolCalls.
type ToolCallRecord struct {
	Name    string  `json:"name"`
	Arg     *string `json:"arg,omitempty"`
	Blocked *bool   `json:"blocked,omitempty"`
}

// LogEvent is one parsed line of the harness run log (a log/slog text
// line): its message (the harness.EventType string, e.g. "turn_end",
// "tool_start") plus its key=value fields.
type LogEvent struct {
	Msg string
	KV  map[string]string
}

// Artifacts is everything RunChecks needs from one completed run: the
// scratch project directory as left by the run, the decoded JSON result,
// the opened session's stats/entries, the parsed run log, and (for faux
// runs only) the faux server's recorded requests.
type Artifacts struct {
	ProjectDir string
	FixtureDir string // pristine fixture, for reference; may be empty

	ExitCode int
	Result   ResultJSON

	SessionStats       session.SessionStats
	CompactionOccurred bool

	LogEvents []LogEvent

	// FauxRequests is nil for a live (non-faux) run; checks that only make
	// sense against a scripted model (system_prompt_tokens) report
	// Skipped: true when this is nil.
	FauxRequests []faux.Request
}

// CheckResult is the outcome of one scenario.Check.
type CheckResult struct {
	Name    string `json:"name,omitempty"`
	Type    string `json:"type"`
	Pass    bool   `json:"pass"`
	Skipped bool   `json:"skipped,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

func checkName(c scenario.Check) string {
	if c.Name != "" {
		return c.Name
	}
	return c.Type
}

func result(c scenario.Check, pass bool, detail string) CheckResult {
	return CheckResult{Name: checkName(c), Type: c.Type, Pass: pass, Detail: detail}
}

func skip(c scenario.Check, detail string) CheckResult {
	return CheckResult{Name: checkName(c), Type: c.Type, Skipped: true, Detail: detail}
}

// RunChecks evaluates every check a scenario declares against art.
func RunChecks(sc *scenario.Scenario, art Artifacts) []CheckResult {
	out := make([]CheckResult, 0, len(sc.Checks))
	for _, c := range sc.Checks {
		out = append(out, runOne(c, art))
	}
	return out
}

func runOne(c scenario.Check, art Artifacts) CheckResult {
	switch c.Type {
	case "file_matches":
		return checkFileMatches(c, art)
	case "file_equals":
		return checkFileEquals(c, art)
	case "file_absent":
		return checkFileAbsent(c, art)
	case "command":
		return checkCommand(c, art)
	case "result_ok":
		return checkResultOK(c, art)
	case "text_matches":
		return checkTextMatches(c, art)
	case "tool_used":
		return checkToolUsed(c, art, true)
	case "tool_not_used":
		return checkToolUsed(c, art, false)
	case "tool_calls":
		return checkToolCalls(c, art)
	case "no_permission_blocks":
		return checkNoPermissionBlocks(c, art)
	case "permission_blocked":
		return checkPermissionBlocked(c, art)
	case "usage":
		return checkUsage(c, art)
	case "compaction_occurred":
		return checkCompaction(c, art)
	case "turns":
		return checkTurns(c, art)
	case "subagent_used":
		return checkSubagentUsed(c, art)
	case "system_prompt_tokens":
		return checkSystemPromptTokens(c, art)
	default:
		return result(c, false, fmt.Sprintf("unknown check type %q", c.Type))
	}
}

func checkFileMatches(c scenario.Check, art Artifacts) CheckResult {
	path := filepath.Join(art.ProjectDir, c.Path)
	data, err := os.ReadFile(path)
	if err != nil {
		return result(c, false, fmt.Sprintf("reading %s: %v", c.Path, err))
	}
	re, err := regexp.Compile(c.Regex)
	if err != nil {
		return result(c, false, fmt.Sprintf("bad regex %q: %v", c.Regex, err))
	}
	if re.Match(data) {
		return result(c, true, "")
	}
	return result(c, false, fmt.Sprintf("%s does not match /%s/", c.Path, c.Regex))
}

func checkFileEquals(c scenario.Check, art Artifacts) CheckResult {
	path := filepath.Join(art.ProjectDir, c.Path)
	data, err := os.ReadFile(path)
	if err != nil {
		return result(c, false, fmt.Sprintf("reading %s: %v", c.Path, err))
	}
	got := strings.TrimRight(string(data), "\n")
	want := strings.TrimRight(c.Expect, "\n")
	if got == want {
		return result(c, true, "")
	}
	return result(c, false, fmt.Sprintf("%s content differs from expect", c.Path))
}

func checkFileAbsent(c scenario.Check, art Artifacts) CheckResult {
	path := filepath.Join(art.ProjectDir, c.Path)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return result(c, true, "")
	}
	return result(c, false, fmt.Sprintf("%s exists", c.Path))
}

func checkCommand(c scenario.Check, art Artifacts) CheckResult {
	cwd := art.ProjectDir
	if c.Cwd != "" {
		cwd = filepath.Join(art.ProjectDir, c.Cwd)
	}
	cmd := exec.Command("sh", "-c", c.Run)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			return result(c, false, fmt.Sprintf("running %q: %v", c.Run, err))
		}
	}
	want := 0
	if c.ExpectExit != nil {
		want = *c.ExpectExit
	}
	if exit == want {
		return result(c, true, "")
	}
	return result(c, false, fmt.Sprintf("%q exited %d, want %d: %s", c.Run, exit, want, truncate(string(out), 400)))
}

func checkResultOK(c scenario.Check, art Artifacts) CheckResult {
	if art.Result.OK {
		return result(c, true, "")
	}
	return result(c, false, fmt.Sprintf("result.ok=false reason=%q", art.Result.Reason))
}

func checkTextMatches(c scenario.Check, art Artifacts) CheckResult {
	re, err := regexp.Compile(c.Regex)
	if err != nil {
		return result(c, false, fmt.Sprintf("bad regex %q: %v", c.Regex, err))
	}
	if re.MatchString(art.Result.Text) {
		return result(c, true, "")
	}
	return result(c, false, fmt.Sprintf("final text does not match /%s/", c.Regex))
}

func checkToolUsed(c scenario.Check, art Artifacts, want bool) CheckResult {
	used := false
	for _, tc := range art.Result.ToolCalls {
		if tc.Name == c.Name {
			used = true
			break
		}
	}
	if used == want {
		return result(c, true, "")
	}
	if want {
		return result(c, false, fmt.Sprintf("tool %q was never called", c.Name))
	}
	return result(c, false, fmt.Sprintf("tool %q was called", c.Name))
}

func checkToolCalls(c scenario.Check, art Artifacts) CheckResult {
	count := 0
	for _, tc := range art.Result.ToolCalls {
		if c.Name == "" || tc.Name == c.Name {
			count++
		}
	}
	if c.Min > 0 && count < c.Min {
		return result(c, false, fmt.Sprintf("%d tool calls, want >= %d", count, c.Min))
	}
	if c.Max > 0 && count > c.Max {
		return result(c, false, fmt.Sprintf("%d tool calls, want <= %d", count, c.Max))
	}
	return result(c, true, fmt.Sprintf("%d tool calls", count))
}

func checkNoPermissionBlocks(c scenario.Check, art Artifacts) CheckResult {
	if len(art.Result.Blocked) == 0 {
		return result(c, true, "")
	}
	return result(c, false, fmt.Sprintf("%d permission blocks: %v", len(art.Result.Blocked), art.Result.Blocked))
}

func checkPermissionBlocked(c scenario.Check, art Artifacts) CheckResult {
	n := len(art.Result.Blocked)
	min := c.Min
	if min == 0 {
		min = 1
	}
	if n >= min {
		return result(c, true, fmt.Sprintf("%d blocks", n))
	}
	return result(c, false, fmt.Sprintf("%d permission blocks, want >= %d", n, min))
}

func checkUsage(c scenario.Check, art Artifacts) CheckResult {
	u := art.Result.Usage
	if c.MaxInput > 0 && u.Input > c.MaxInput {
		return result(c, false, fmt.Sprintf("input tokens %d > max %d", u.Input, c.MaxInput))
	}
	if c.MaxOutput > 0 && u.Output > c.MaxOutput {
		return result(c, false, fmt.Sprintf("output tokens %d > max %d", u.Output, c.MaxOutput))
	}
	if c.MaxCostUSD > 0 && art.Result.TotalCostUSD > c.MaxCostUSD {
		return result(c, false, fmt.Sprintf("cost $%.4f > max $%.4f", art.Result.TotalCostUSD, c.MaxCostUSD))
	}
	return result(c, true, "")
}

func checkCompaction(c scenario.Check, art Artifacts) CheckResult {
	if art.CompactionOccurred {
		return result(c, true, "")
	}
	return result(c, false, "no compaction entry found in session")
}

func checkTurns(c scenario.Check, art Artifacts) CheckResult {
	turns := art.Result.NumTurns
	if c.Max > 0 && turns > c.Max {
		return result(c, false, fmt.Sprintf("%d turns > max %d", turns, c.Max))
	}
	if c.Min > 0 && turns < c.Min {
		return result(c, false, fmt.Sprintf("%d turns < min %d", turns, c.Min))
	}
	return result(c, true, fmt.Sprintf("%d turns", turns))
}

func checkSubagentUsed(c scenario.Check, art Artifacts) CheckResult {
	for _, ev := range art.LogEvents {
		if ev.Msg != "subagent model resolved" {
			continue
		}
		if c.Role == "" || ev.KV["requested"] == c.Role || ev.KV["kind"] == c.Role {
			return result(c, true, "")
		}
	}
	return result(c, false, fmt.Sprintf("no subagent dispatch resolved for role %q", c.Role))
}

func checkSystemPromptTokens(c scenario.Check, art Artifacts) CheckResult {
	if art.FauxRequests == nil {
		return skip(c, "no faux server for this run (live model)")
	}
	if len(art.FauxRequests) == 0 {
		return result(c, false, "no requests recorded")
	}
	tokens := estimateTokens(art.FauxRequests[0].System)
	if c.Max > 0 && tokens > c.Max {
		return result(c, false, fmt.Sprintf("system prompt ~%d tokens > max %d", tokens, c.Max))
	}
	return result(c, true, fmt.Sprintf("system prompt ~%d tokens", tokens))
}

// estimateTokens is a plain chars/4 estimate, matching the order of
// magnitude internal/compaction.EstimateTokens uses for text content
// without pulling in that package's message-shaped API for a single string.
func estimateTokens(s string) int {
	return (len(s) + 3) / 4
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

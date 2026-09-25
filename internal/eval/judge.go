package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// Verdict is an LLM judge's grade of one run: a 0-10 score plus the reasons
// it gave for that score.
type Verdict struct {
	Score   float64  `json:"score"`
	Reasons []string `json:"reasons"`
}

// parseVerdict decodes raw as a Verdict. raw may be exactly the JSON object
// (the faux-verdict path, where a scenario author writes it directly in
// YAML) or prose with a JSON object embedded somewhere in it (a live
// judge's completion, which may wrap the object in a sentence or markdown
// fence despite being asked not to): the first balanced {...} block is
// extracted and decoded.
func parseVerdict(raw []byte) (Verdict, error) {
	block, err := firstJSONObject(raw)
	if err != nil {
		return Verdict{}, err
	}
	var v struct {
		Score   float64  `json:"score"`
		Reasons []string `json:"reasons"`
		Reason  string   `json:"reason"`
	}
	if err := json.Unmarshal(block, &v); err != nil {
		return Verdict{}, fmt.Errorf("eval: parsing verdict %s: %w", truncate(string(block), 200), err)
	}
	reasons := v.Reasons
	if len(reasons) == 0 && v.Reason != "" {
		reasons = []string{v.Reason}
	}
	return Verdict{Score: v.Score, Reasons: reasons}, nil
}

// firstJSONObject scans raw for the first balanced {...} block (tracking
// string/escape state so braces inside string values don't confuse it) and
// returns it verbatim.
func firstJSONObject(raw []byte) ([]byte, error) {
	start := -1
	depth := 0
	inString := false
	escaped := false
	for i, b := range raw {
		if start == -1 {
			if b == '{' {
				start = i
				depth = 1
			}
			continue
		}
		if inString {
			switch {
			case escaped:
				escaped = false
			case b == '\\':
				escaped = true
			case b == '"':
				inString = false
			}
			continue
		}
		switch b {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return raw[start : i+1], nil
			}
		}
	}
	return nil, fmt.Errorf("eval: no JSON object found in %s", truncate(string(raw), 200))
}

// JudgeInputs is what buildRubricPrompt has to work with to grade a run.
type JudgeInputs struct {
	Prompt    string
	FinalText string
	Diff      string
	ToolCalls []ToolCallRecord
}

// buildRubricPrompt builds the one-shot prompt handed to the judge model:
// the scenario's rubric, the run's prompt/response/diff/tool calls, and a
// strict instruction to answer with nothing but a JSON object.
func buildRubricPrompt(sc rubricScenario, in JudgeInputs) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are grading one run of a coding agent against a rubric. Score 0-10.\n\n")
	fmt.Fprintf(&b, "Rubric:\n%s\n\n", sc.Rubric())
	fmt.Fprintf(&b, "User prompt given to the agent:\n%s\n\n", in.Prompt)
	fmt.Fprintf(&b, "Agent's final response text:\n%s\n\n", orNone(in.FinalText))
	if len(in.ToolCalls) > 0 {
		b.WriteString("Tool calls made:\n")
		for _, tc := range in.ToolCalls {
			arg := ""
			if tc.Arg != nil {
				arg = *tc.Arg
			}
			fmt.Fprintf(&b, "  - %s(%s)\n", tc.Name, arg)
		}
		b.WriteString("\n")
	}
	if in.Diff != "" {
		fmt.Fprintf(&b, "Diff of files changed:\n%s\n\n", in.Diff)
	}
	b.WriteString("Respond with ONLY a JSON object of the exact shape " +
		`{"score": <number 0-10>, "reasons": ["..."]}` +
		". No prose, no markdown fence, nothing else.\n")
	return b.String()
}

// rubricScenario is the tiny slice of scenario.Scenario/scenario.Judge
// buildRubricPrompt needs, kept as an interface so this file has no import
// cycle concerns and tests can supply a fake.
type rubricScenario interface {
	Rubric() string
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(empty)"
	}
	return s
}

// RunJudge asks the resolved judge model (reg.Resolve(provider, model))
// to grade one run with a single, non-tool completion, then parses its
// response as a Verdict.
func RunJudge(ctx context.Context, reg *provider.Registry, providerID, modelID, prompt string) (Verdict, string, error) {
	resolved, err := reg.Resolve(providerID, modelID)
	if err != nil {
		return Verdict{}, "", fmt.Errorf("eval: resolving judge model %s/%s: %w", providerID, modelID, err)
	}
	p, ok := reg.Provider(providerID)
	if !ok {
		return Verdict{}, "", fmt.Errorf("eval: judge provider %q not registered", providerID)
	}

	transcript := []msg.Message{
		msg.UserMessage{
			Role:    msg.RoleUser,
			Content: msg.Blocks{msg.TextContent{Type: "text", Text: prompt}},
		},
	}
	events, wait := p.Stream(ctx, resolved.Model, transcript, provider.StreamOptions{})
	for range events {
		// drained for side effects only; the final message comes from wait.
	}
	am, err := wait()
	if err != nil {
		return Verdict{}, "", fmt.Errorf("eval: judge stream: %w", err)
	}
	raw := msg.TextOf(am.Content)
	v, err := parseVerdict([]byte(raw))
	if err != nil {
		return Verdict{}, raw, err
	}
	return v, raw, nil
}

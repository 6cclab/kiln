package tools

// `ask_user_question` — kiln's port of Claude Code's AskUserQuestion tool:
// the model's way to ask the user one or more multiple-choice questions
// mid-task, including from inside plan mode (it changes nothing on disk,
// so internal/claude/settings.ReadOnly lists it the same way it lists
// exit_plan_mode and todo_write).
//
// Wiring note (see this phase's task brief): internal/cli/chat.go is being
// edited concurrently by another session rewriting the system prompt, so
// this tool takes no approver argument the way ExitPlanModeTool takes
// PlanApprover — chat.go would otherwise need a rebindable-approver
// variable and an InteractiveDeps field, both edits outside the one
// registration line this phase was told to touch. Instead the interactive
// approver is a package-level, atomically-swapped function
// (SetAskUserApprover), set once by internal/cli/tui.go's RunInteractive
// (not a restricted file) after the bridge exists, exactly the moment
// chat.go's own rebindable planApprover is bound today. Print mode and
// eval runs never call SetAskUserApprover, so Execute falls back to the
// headless message below — the same shape as exit_plan_mode's "No
// interactive approval available" fallback in chat.go, just reached
// without needing chat.go's own plumbing.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/andrepato/harness/internal/tool"
)

// AskUserOption is one multiple-choice option offered for a question.
type AskUserOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// AskUserQuestion is one question the model wants answered, with its
// options. An automatic "Other" choice (free text) is always available in
// addition to Options and must not be listed among them.
type AskUserQuestion struct {
	Question    string          `json:"question"`
	Header      string          `json:"header"`
	Options     []AskUserOption `json:"options"`
	MultiSelect bool            `json:"multiSelect"`
}

// AskUserAnswer is the user's answer to one question, in the order the
// questions were asked. Answers holds one item for a single-select
// question (or an "Other" free-text answer), and one item per toggled
// option for a multi-select question, in the order they were chosen.
type AskUserAnswer struct {
	Question string
	Answers  []string
}

// ErrAskUserCancelled is returned by an AskUserApprover when the user
// cancels the whole exchange (Esc from the questions, or Cancel from the
// multi-question review screen), distinct from a plumbing error
// (ctx.Done()) so Execute can turn it into a plain declined result rather
// than propagating a tool-call failure.
var ErrAskUserCancelled = errors.New("ask_user_question: user declined to answer")

// AskUserApprover asks the user the given questions and returns their
// answers in order, or ErrAskUserCancelled if the user cancelled.
// Implemented by the TUI (internal/tui.Bridge.AskUserApprover).
type AskUserApprover func(ctx context.Context, questions []AskUserQuestion) ([]AskUserAnswer, error)

// askUserApprover is the current interactive approver, nil in every
// headless run (print mode, eval) and whenever no TUI has bound one yet.
var askUserApprover atomic.Pointer[AskUserApprover]

// SetAskUserApprover rebinds the interactive approver. Passing nil clears
// it, restoring the headless fallback — used by tests and by a caller
// that tears its TUI down.
func SetAskUserApprover(fn AskUserApprover) {
	if fn == nil {
		askUserApprover.Store(nil)
		return
	}
	askUserApprover.Store(&fn)
}

var askUserQuestionParameters = json.RawMessage(`{
	"type": "object",
	"properties": {
		"questions": {
			"type": "array",
			"minItems": 1,
			"maxItems": 4,
			"description": "1-4 multiple-choice questions to ask the user, shown one at a time.",
			"items": {
				"type": "object",
				"properties": {
					"question": {"type": "string", "description": "The full question text, ending with \"?\"."},
					"header": {"type": "string", "maxLength": 12, "description": "Tab label of at most 12 characters, one or two words: \"Scope\", \"Tests\", \"PUT rule\"."},
					"multiSelect": {"type": "boolean", "description": "Allow choosing more than one option. Default false."},
					"options": {
						"type": "array",
						"minItems": 2,
						"maxItems": 4,
						"description": "2-4 choices. The user can also always type a free-text \"Other\" answer; do not add it yourself.",
						"items": {
							"type": "object",
							"properties": {
								"label": {"type": "string", "description": "1-5 words naming this choice."},
								"description": {"type": "string", "description": "One sentence explaining the choice."}
							},
							"required": ["label", "description"]
						}
					}
				},
				"required": ["question", "header", "options"]
			}
		}
	},
	"required": ["questions"]
}`)

type askUserQuestionArgs struct {
	Questions []AskUserQuestion `json:"questions"`
}

// validateAskUserQuestions checks the input shape Claude Code's own
// AskUserQuestion enforces: 1-4 questions with unique text, each with a
// header of at most 12 characters and 2-4 options with unique, 1-5-word
// labels. It returns the first violation found, for a tool.Errorf result
// the model can act on.
func validateAskUserQuestions(qs []AskUserQuestion) error {
	if len(qs) == 0 {
		return fmt.Errorf("questions must not be empty")
	}
	if len(qs) > 4 {
		return fmt.Errorf("at most 4 questions are allowed, got %d", len(qs))
	}
	seenQuestions := make(map[string]bool, len(qs))
	for i, q := range qs {
		question := strings.TrimSpace(q.Question)
		if question == "" {
			return fmt.Errorf("question %d: question text must not be empty", i+1)
		}
		if !strings.HasSuffix(question, "?") {
			return fmt.Errorf("question %d: question text must end with \"?\": %q", i+1, question)
		}
		key := strings.ToLower(question)
		if seenQuestions[key] {
			return fmt.Errorf("question %d: duplicate question text %q", i+1, question)
		}
		seenQuestions[key] = true

		header := strings.TrimSpace(q.Header)
		if header == "" {
			return fmt.Errorf("question %d: header must not be empty", i+1)
		}
		if len(header) > 12 {
			return fmt.Errorf("question %d: header %q is %d characters, want at most 12; use one or two words", i+1, header, len(header))
		}

		if len(q.Options) < 2 {
			return fmt.Errorf("question %d: at least 2 options are required, got %d", i+1, len(q.Options))
		}
		if len(q.Options) > 4 {
			return fmt.Errorf("question %d: at most 4 options are allowed, got %d", i+1, len(q.Options))
		}
		seenLabels := make(map[string]bool, len(q.Options))
		for j, o := range q.Options {
			label := strings.TrimSpace(o.Label)
			if label == "" {
				return fmt.Errorf("question %d option %d: label must not be empty", i+1, j+1)
			}
			words := strings.Fields(label)
			if len(words) > 5 {
				return fmt.Errorf("question %d option %d: label %q is %d words, want at most 5", i+1, j+1, label, len(words))
			}
			lkey := strings.ToLower(label)
			if seenLabels[lkey] {
				return fmt.Errorf("question %d: duplicate option label %q", i+1, label)
			}
			seenLabels[lkey] = true
			if strings.EqualFold(label, "other") {
				return fmt.Errorf("question %d option %d: \"Other\" is offered automatically; do not list it as an option", i+1, j+1)
			}
		}
	}
	return nil
}

// FormatAskUserAnswers renders the model-facing result text for a
// completed exchange, matching Claude Code's own AskUserQuestion result
// shape: `User has answered your questions: "<question>"="<answer>", ...`
// with a multi-select answer's choices comma-joined.
func FormatAskUserAnswers(answers []AskUserAnswer) string {
	parts := make([]string, 0, len(answers))
	for _, a := range answers {
		parts = append(parts, fmt.Sprintf("%q=%q", a.Question, strings.Join(a.Answers, ", ")))
	}
	return "User has answered your questions: " + strings.Join(parts, ", ")
}

// AskUserAnswerPair is one question and its answer as shown to the user.
type AskUserAnswerPair struct {
	Question string
	Answer   string
}

// ParseAskUserAnswers inverts FormatAskUserAnswers, so the TUI can show the
// answers without the model-facing wrapper. ok is false for any other text
// (a decline, an error).
func ParseAskUserAnswers(s string) (pairs []AskUserAnswerPair, ok bool) {
	rest, found := strings.CutPrefix(s, "User has answered your questions: ")
	if !found {
		return nil, false
	}
	for rest != "" {
		q, err := strconv.QuotedPrefix(rest)
		if err != nil {
			return nil, false
		}
		rest = rest[len(q):]
		if rest, found = strings.CutPrefix(rest, "="); !found {
			return nil, false
		}
		a, err := strconv.QuotedPrefix(rest)
		if err != nil {
			return nil, false
		}
		rest = strings.TrimPrefix(rest[len(a):], ", ")
		uq, _ := strconv.Unquote(q)
		ua, _ := strconv.Unquote(a)
		pairs = append(pairs, AskUserAnswerPair{Question: uq, Answer: ua})
	}
	return pairs, len(pairs) > 0
}

// AskUserQuestionTool builds `ask_user_question`.
func AskUserQuestionTool() *tool.Tool {
	return &tool.Tool{
		Name:  "ask_user_question",
		Label: "Ask a question",
		Description: "Ask the user one or more multiple-choice questions when you need a decision only they can make - " +
			"which approach to take, which of several ambiguous readings is right, a preference with no clearly correct " +
			"answer. Up to 4 questions, each with 2-4 short options; the user can also type a free-text answer. Do not " +
			"use this for things you can figure out yourself by reading code or running commands.",
		Parameters: askUserQuestionParameters,
		Execute: func(ctx context.Context, raw json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
			var a askUserQuestionArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return tool.Result{}, fmt.Errorf("ask_user_question: decoding arguments: %w", err)
				}
			}
			if err := validateAskUserQuestions(a.Questions); err != nil {
				return tool.Errorf("ask_user_question: %s", err), nil
			}

			approverPtr := askUserApprover.Load()
			if approverPtr == nil {
				// Headless (print mode, eval, or a TUI that has not bound
				// one yet): no one is available to answer. Matching
				// exit_plan_mode's own headless fallback in chat.go, the
				// model is told to proceed on its own judgement rather than
				// getting a bare failure it might retry forever.
				return tool.Errorf(
					"No user is available to answer right now (non-interactive run). " +
						"Proceed with your best judgement and state the assumptions you made.",
				), nil
			}

			answers, err := (*approverPtr)(ctx, a.Questions)
			if err != nil {
				if errors.Is(err, ErrAskUserCancelled) {
					return tool.Errorf("The user declined to answer. Proceed with your best judgement, or ask a different way."), nil
				}
				return tool.Result{}, err
			}
			return tool.Text(FormatAskUserAnswers(answers)), nil
		},
	}
}

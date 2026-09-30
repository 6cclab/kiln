package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/tool"
)

func oneValidQuestion() AskUserQuestion {
	return AskUserQuestion{
		Question: "Which approach should I use?",
		Header:   "Approach",
		Options: []AskUserOption{
			{Label: "Fast path", Description: "Ship quickly, fewer tests"},
			{Label: "Careful path", Description: "Slower, more coverage"},
		},
	}
}

func TestValidateAskUserQuestions_Valid(t *testing.T) {
	if err := validateAskUserQuestions([]AskUserQuestion{oneValidQuestion()}); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
}

func TestValidateAskUserQuestions_Empty(t *testing.T) {
	if err := validateAskUserQuestions(nil); err == nil {
		t.Fatal("expected an error for zero questions")
	}
}

func TestValidateAskUserQuestions_TooMany(t *testing.T) {
	qs := make([]AskUserQuestion, 5)
	for i := range qs {
		q := oneValidQuestion()
		q.Question = q.Question + strings.Repeat(" ", i) // keep unique-ish, still ends in "?"
		qs[i] = q
	}
	if err := validateAskUserQuestions(qs); err == nil {
		t.Fatal("expected an error for 5 questions (max 4)")
	}
}

func TestValidateAskUserQuestions_MissingQuestionMark(t *testing.T) {
	q := oneValidQuestion()
	q.Question = "Which approach should I use"
	if err := validateAskUserQuestions([]AskUserQuestion{q}); err == nil {
		t.Fatal("expected an error for a question not ending in \"?\"")
	}
}

func TestValidateAskUserQuestions_DuplicateQuestionText(t *testing.T) {
	q1 := oneValidQuestion()
	q2 := oneValidQuestion()
	q2.Header = "Other"
	if err := validateAskUserQuestions([]AskUserQuestion{q1, q2}); err == nil {
		t.Fatal("expected an error for duplicate question text")
	}
}

func TestValidateAskUserQuestions_HeaderTooLong(t *testing.T) {
	q := oneValidQuestion()
	q.Header = "Way too long a header"
	if err := validateAskUserQuestions([]AskUserQuestion{q}); err == nil {
		t.Fatal("expected an error for a header over 12 characters")
	}
}

func TestValidateAskUserQuestions_TooFewOptions(t *testing.T) {
	q := oneValidQuestion()
	q.Options = q.Options[:1]
	if err := validateAskUserQuestions([]AskUserQuestion{q}); err == nil {
		t.Fatal("expected an error for a single option")
	}
}

func TestValidateAskUserQuestions_TooManyOptions(t *testing.T) {
	q := oneValidQuestion()
	q.Options = append(q.Options,
		AskUserOption{Label: "Third", Description: "d"},
		AskUserOption{Label: "Fourth", Description: "d"},
		AskUserOption{Label: "Fifth", Description: "d"},
	)
	if err := validateAskUserQuestions([]AskUserQuestion{q}); err == nil {
		t.Fatal("expected an error for 5 options (max 4)")
	}
}

func TestValidateAskUserQuestions_DuplicateLabel(t *testing.T) {
	q := oneValidQuestion()
	q.Options[1].Label = q.Options[0].Label
	if err := validateAskUserQuestions([]AskUserQuestion{q}); err == nil {
		t.Fatal("expected an error for duplicate option labels")
	}
}

func TestValidateAskUserQuestions_LabelTooManyWords(t *testing.T) {
	q := oneValidQuestion()
	q.Options[0].Label = "this label has way too many words in it"
	if err := validateAskUserQuestions([]AskUserQuestion{q}); err == nil {
		t.Fatal("expected an error for a label over 5 words")
	}
}

func TestValidateAskUserQuestions_OtherReserved(t *testing.T) {
	q := oneValidQuestion()
	q.Options[0].Label = "Other"
	if err := validateAskUserQuestions([]AskUserQuestion{q}); err == nil {
		t.Fatal(`expected an error for an explicit "Other" option`)
	}
}

func TestFormatAskUserAnswers_SingleSelect(t *testing.T) {
	got := FormatAskUserAnswers([]AskUserAnswer{
		{Question: "Which approach should I use?", Answers: []string{"Fast path"}},
	})
	want := `User has answered your questions: "Which approach should I use?"="Fast path"`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFormatAskUserAnswers_MultiSelectJoined(t *testing.T) {
	got := FormatAskUserAnswers([]AskUserAnswer{
		{Question: "Which checks matter?", Answers: []string{"Lint", "Tests"}},
	})
	want := `User has answered your questions: "Which checks matter?"="Lint, Tests"`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFormatAskUserAnswers_MultipleQuestions(t *testing.T) {
	got := FormatAskUserAnswers([]AskUserAnswer{
		{Question: "Q1?", Answers: []string{"A"}},
		{Question: "Q2?", Answers: []string{"B"}},
	})
	want := `User has answered your questions: "Q1?"="A", "Q2?"="B"`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// runAskUser invokes the tool's Execute with the given questions JSON.
func runAskUser(t *testing.T, argsJSON string) tool.Result {
	t.Helper()
	tl := AskUserQuestionTool()
	res, err := tl.Execute(context.Background(), json.RawMessage(argsJSON), func(tool.Result) {}, tool.Invocation{ToolName: "ask_user_question"})
	if err != nil {
		t.Fatalf("Execute returned an error: %v", err)
	}
	return res
}

func TestAskUserQuestionTool_HeadlessReturnsErrorResult(t *testing.T) {
	SetAskUserApprover(nil) // ensure no approver bound (print mode / eval shape)
	res := runAskUser(t, `{"questions":[{"question":"Which approach should I use?","header":"Approach","options":[{"label":"Fast path","description":"a"},{"label":"Careful path","description":"b"}]}]}`)
	if !res.IsError {
		t.Fatalf("expected an error result in headless mode, got %+v", res)
	}
	text := resultText(res)
	if !strings.Contains(text, "No user is available") {
		t.Errorf("headless result missing the no-user-available message: %q", text)
	}
	if !strings.Contains(text, "best judgement") {
		t.Errorf("headless result missing the best-judgement instruction: %q", text)
	}
}

func TestAskUserQuestionTool_InvalidArgsReturnsErrorResult(t *testing.T) {
	res := runAskUser(t, `{"questions":[]}`)
	if !res.IsError {
		t.Fatalf("expected an error result for empty questions, got %+v", res)
	}
}

func TestAskUserQuestionTool_ApproverAnswersReachResult(t *testing.T) {
	t.Cleanup(func() { SetAskUserApprover(nil) })
	var gotQuestions []AskUserQuestion
	SetAskUserApprover(func(ctx context.Context, questions []AskUserQuestion) ([]AskUserAnswer, error) {
		gotQuestions = questions
		return []AskUserAnswer{{Question: questions[0].Question, Answers: []string{"Fast path"}}}, nil
	})

	res := runAskUser(t, `{"questions":[{"question":"Which approach should I use?","header":"Approach","options":[{"label":"Fast path","description":"a"},{"label":"Careful path","description":"b"}]}]}`)
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	text := resultText(res)
	want := `User has answered your questions: "Which approach should I use?"="Fast path"`
	if text != want {
		t.Errorf("got %q, want %q", text, want)
	}
	if len(gotQuestions) != 1 || gotQuestions[0].Header != "Approach" {
		t.Errorf("approver did not receive the parsed question: %+v", gotQuestions)
	}
}

func TestAskUserQuestionTool_CancelledReturnsDeclinedResult(t *testing.T) {
	t.Cleanup(func() { SetAskUserApprover(nil) })
	SetAskUserApprover(func(ctx context.Context, questions []AskUserQuestion) ([]AskUserAnswer, error) {
		return nil, ErrAskUserCancelled
	})

	res := runAskUser(t, `{"questions":[{"question":"Which approach should I use?","header":"Approach","options":[{"label":"Fast path","description":"a"},{"label":"Careful path","description":"b"}]}]}`)
	if !res.IsError {
		t.Fatalf("expected an error result on cancellation, got %+v", res)
	}
	text := resultText(res)
	if !strings.Contains(text, "declined to answer") {
		t.Errorf("cancelled result missing decline wording: %q", text)
	}
}

func TestAskUserQuestionTool_PlumbingErrorPropagates(t *testing.T) {
	t.Cleanup(func() { SetAskUserApprover(nil) })
	boom := context.Canceled
	SetAskUserApprover(func(ctx context.Context, questions []AskUserQuestion) ([]AskUserAnswer, error) {
		return nil, boom
	})

	tl := AskUserQuestionTool()
	_, err := tl.Execute(context.Background(), json.RawMessage(`{"questions":[{"question":"Which approach should I use?","header":"Approach","options":[{"label":"Fast path","description":"a"},{"label":"Careful path","description":"b"}]}]}`), func(tool.Result) {}, tool.Invocation{})
	if err != boom {
		t.Fatalf("expected the plumbing error to propagate, got %v", err)
	}
}

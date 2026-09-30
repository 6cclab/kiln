package tui

import "testing"

// Golden coverage for the ask_user_question prompt (askuser_render.go):
// single-select, multi-select with toggled items, the free-text "Other"
// entry, and the multi-question review screen. See golden_test.go for the
// harness these use.

func oneQuestionView() AskUserQuestionView {
	return AskUserQuestionView{
		Header:   "Approach",
		Question: "Which approach should I use?",
		Options: []AskUserOptionView{
			{Label: "Fast path", Description: "Ship quickly, fewer tests"},
			{Label: "Careful path", Description: "Slower, more coverage"},
		},
	}
}

func multiSelectQuestion() AskUserQuestionView {
	return AskUserQuestionView{
		Header:      "Checks",
		Question:    "Which checks should block the merge?",
		MultiSelect: true,
		Options: []AskUserOptionView{
			{Label: "Lint", Description: "gofmt, vet, staticcheck"},
			{Label: "Unit tests", Description: "go test ./..."},
			{Label: "E2E", Description: "PTY behaviour suite"},
		},
	}
}

func TestRenderGolden_AskUser_SingleSelect(t *testing.T) {
	withRenderEnv(t, 80)
	headers := []string{"Approach"}
	out := RenderAskUserQuestion(headers, 0, oneQuestionView(), 80, 0, map[int]bool{}, false, "")
	assertRenderGolden(t, "askuser-single-select", out)
}

func TestRenderGolden_AskUser_SingleSelect_SecondOptionHighlighted(t *testing.T) {
	withRenderEnv(t, 80)
	headers := []string{"Approach"}
	out := RenderAskUserQuestion(headers, 0, oneQuestionView(), 80, 1, map[int]bool{}, false, "")
	assertRenderGolden(t, "askuser-single-select-option2", out)
}

func TestRenderGolden_AskUser_MultiSelect_Toggled(t *testing.T) {
	withRenderEnv(t, 80)
	headers := []string{"Checks"}
	checked := map[int]bool{0: true, 2: true} // Lint and E2E on, Unit tests off
	out := RenderAskUserQuestion(headers, 0, multiSelectQuestion(), 80, 1, checked, false, "")
	assertRenderGolden(t, "askuser-multi-select-toggled", out)
}

func TestRenderGolden_AskUser_OtherFreeText(t *testing.T) {
	withRenderEnv(t, 80)
	headers := []string{"Approach"}
	out := RenderAskUserQuestion(headers, 0, oneQuestionView(), 80, 2, map[int]bool{}, true, "Actually, do both in stages")
	assertRenderGolden(t, "askuser-other-freetext", out)
}

func TestRenderGolden_AskUser_MultiQuestionTabs(t *testing.T) {
	withRenderEnv(t, 80)
	headers := []string{"Approach", "Checks", "Scope"}
	out := RenderAskUserQuestion(headers, 1, multiSelectQuestion(), 80, 0, map[int]bool{}, false, "")
	assertRenderGolden(t, "askuser-multi-question-tabs", out)
}

func TestRenderGolden_AskUser_Review(t *testing.T) {
	withRenderEnv(t, 80)
	questions := []AskUserQuestionView{oneQuestionView(), multiSelectQuestion()}
	answers := [][]string{
		{"Fast path"},
		{"Lint", "E2E"},
	}
	out := RenderAskUserReview(questions, answers, 80, 0)
	assertRenderGolden(t, "askuser-review", out)
}

func TestRenderGolden_AskUser_Review_CancelSelected(t *testing.T) {
	withRenderEnv(t, 80)
	questions := []AskUserQuestionView{oneQuestionView(), multiSelectQuestion()}
	answers := [][]string{
		{"Fast path"},
		{"Lint", "E2E"},
	}
	out := RenderAskUserReview(questions, answers, 80, 1)
	assertRenderGolden(t, "askuser-review-cancel-selected", out)
}

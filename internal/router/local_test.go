package router

import (
	"testing"

	"github.com/andrepato/harness/internal/provider"
)

func localModel(id string) provider.Model {
	return provider.Model{Provider: "ollama", ID: id}
}

func TestCollapseLocal(t *testing.T) {
	implement := localModel("qwen3.8")
	plan := localModel("qwen3:30b-a3b")
	planComplex := localModel("gpt-oss:20b")

	threeWay := []LocalCandidate{
		{Slot: "implement", Model: implement},
		{Slot: "plan", Model: plan},
		{Slot: "planComplex", Model: planComplex},
		{Slot: "direct", Model: implement},
		{Slot: "fast", Model: implement},
	}

	t.Run("nothing resident: falls back to implement's model", func(t *testing.T) {
		got, ok := CollapseLocal(threeWay, nil)
		if !ok {
			t.Fatal("want a collapse")
		}
		if !sameModel(got.Chosen, implement) {
			t.Errorf("chosen = %s, want implement's %s", got.Chosen.ID, implement.ID)
		}
		if got.AlreadyLoaded {
			t.Error("AlreadyLoaded = true, want false (resident is empty)")
		}
		if len(got.Others) != 2 || got.Others[0].ID != plan.ID || got.Others[1].ID != planComplex.ID {
			t.Errorf("Others = %v, want [plan, planComplex] in that order", got.Others)
		}
	})

	t.Run("a Resident error (nil resident) behaves like nothing resident", func(t *testing.T) {
		got, ok := CollapseLocal(threeWay, []string{})
		if !ok || !sameModel(got.Chosen, implement) || got.AlreadyLoaded {
			t.Errorf("got %+v, %v, want implement's model, not loaded", got, ok)
		}
	})

	t.Run("plan's model resident: plan's model is chosen, marked loaded", func(t *testing.T) {
		got, ok := CollapseLocal(threeWay, []string{plan.ID})
		if !ok {
			t.Fatal("want a collapse")
		}
		if !sameModel(got.Chosen, plan) {
			t.Errorf("chosen = %s, want plan's %s", got.Chosen.ID, plan.ID)
		}
		if !got.AlreadyLoaded {
			t.Error("AlreadyLoaded = false, want true")
		}
	})

	t.Run("a :latest suffix on the resident id still matches", func(t *testing.T) {
		got, ok := CollapseLocal(threeWay, []string{plan.ID + ":latest"})
		if !ok || !sameModel(got.Chosen, plan) || !got.AlreadyLoaded {
			t.Errorf("got %+v, %v, want plan's model, loaded", got, ok)
		}
	})

	t.Run("all resident: unchanged", func(t *testing.T) {
		got, ok := CollapseLocal(threeWay, []string{implement.ID, plan.ID, planComplex.ID})
		if ok {
			t.Errorf("want no collapse when every candidate is resident, got %+v", got)
		}
	})

	t.Run("fast collapsed along with the router slots", func(t *testing.T) {
		candidates := []LocalCandidate{
			{Slot: "implement", Model: implement},
			{Slot: "plan", Model: plan},
			{Slot: "fast", Model: planComplex},
		}
		got, ok := CollapseLocal(candidates, nil)
		if !ok {
			t.Fatal("want a collapse")
		}
		if !sameModel(got.Chosen, implement) {
			t.Errorf("chosen = %s, want implement's %s", got.Chosen.ID, implement.ID)
		}
		foundPlanComplexAsOther := false
		for _, m := range got.Others {
			if sameModel(m, planComplex) {
				foundPlanComplexAsOther = true
			}
		}
		if !foundPlanComplexAsOther {
			t.Errorf("Others = %v, want fast's model (%s) among them", got.Others, planComplex.ID)
		}
	})

	t.Run("a single local model: no change", func(t *testing.T) {
		candidates := []LocalCandidate{
			{Slot: "implement", Model: implement},
			{Slot: "plan", Model: implement},
			{Slot: "direct", Model: implement},
		}
		got, ok := CollapseLocal(candidates, nil)
		if ok {
			t.Errorf("want no collapse with only one distinct model, got %+v", got)
		}
	})

	t.Run("candidates in any order sort by priority before picking", func(t *testing.T) {
		shuffled := []LocalCandidate{
			{Slot: "fast", Model: implement},
			{Slot: "planComplex", Model: planComplex},
			{Slot: "plan", Model: plan},
			{Slot: "direct", Model: implement},
			{Slot: "implement", Model: implement},
		}
		got, ok := CollapseLocal(shuffled, nil)
		if !ok || !sameModel(got.Chosen, implement) {
			t.Errorf("got %+v, %v, want implement's model chosen regardless of input order", got, ok)
		}
	})
}

func TestNormalizeModelID(t *testing.T) {
	cases := map[string]string{
		"qwen3.8":        "qwen3.8",
		"qwen3.8:latest": "qwen3.8",
		"llama3:8b":      "llama3:8b",
	}
	for in, want := range cases {
		if got := normalizeModelID(in); got != want {
			t.Errorf("normalizeModelID(%q) = %q, want %q", in, got, want)
		}
	}
}

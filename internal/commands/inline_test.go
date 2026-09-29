package commands

import (
	"context"
	"strings"
	"testing"
)

func TestPostureListsWithActiveMarked(t *testing.T) {
	source := InlineCommands(InlineDeps{
		Postures:      []Posture{{Name: "coding", Description: "day-to-day"}, {Name: "ops", Description: "infra"}},
		ActivePosture: func() string { return "ops" },
	})
	res, err := findCmd(t, source, "posture").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Output, "\n")
	if !strings.Contains(joined, "ops ✓") || strings.Contains(joined, "coding ✓") {
		t.Fatalf("got %q", joined)
	}
}

func TestPostureSwitchClearsAndCallsIntegrator(t *testing.T) {
	var switched string
	source := InlineCommands(InlineDeps{
		Postures:      []Posture{{Name: "coding"}, {Name: "ops"}},
		ActivePosture: func() string { return "coding" },
		SwitchPosture: func(ctx context.Context, name string) error {
			switched = name
			return nil
		},
	})
	res, err := findCmd(t, source, "posture").Run(context.Background(), "ops")
	if err != nil {
		t.Fatal(err)
	}
	if switched != "ops" {
		t.Fatalf("got %q", switched)
	}
	if !strings.Contains(res.Output[0], "Admitted tools cleared") {
		t.Fatalf("got %+v", res)
	}
}

func TestPostureUnknownNamesTheOptions(t *testing.T) {
	source := InlineCommands(InlineDeps{Postures: []Posture{{Name: "coding"}, {Name: "ops"}}})
	res, err := findCmd(t, source, "posture").Run(context.Background(), "bogus")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output[0], "coding, ops") {
		t.Fatalf("got %+v", res)
	}
}

func TestBashesWithNoRendererSaysNone(t *testing.T) {
	source := InlineCommands(InlineDeps{})
	res, err := findCmd(t, source, "bashes").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Output) != 1 || res.Output[0] != "no background shells" {
		t.Fatalf("got %+v", res)
	}
}

func TestBashesUsesRenderer(t *testing.T) {
	source := InlineCommands(InlineDeps{RenderShellList: func() []string { return []string{"1  running  npm test"} }})
	res, err := findCmd(t, source, "bashes").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Output) != 1 || !strings.Contains(res.Output[0], "npm test") {
		t.Fatalf("got %+v", res)
	}
}

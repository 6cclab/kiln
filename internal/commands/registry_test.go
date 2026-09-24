package commands

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
)

func ok(name, out string) Command {
	return Command{
		Name:        name,
		Description: name,
		Run: func(ctx context.Context, args string) (Result, error) {
			return Result{Output: []string{out}}, nil
		},
	}
}

func TestRegistryResolvesOneNamespace(t *testing.T) {
	r := NewRegistry()
	r.Add(StaticSource(OriginBuiltin, []Command{ok("compact", "b")}))
	plugin := ok("a11y", "p")
	plugin.Namespace = "chrome"
	r.Add(StaticSource(OriginPlugin, []Command{plugin}))
	r.Add(StaticSource(OriginProject, []Command{ok("track-work", "j")}))

	var names []string
	for _, c := range r.List() {
		names = append(names, QualifiedName(c))
	}
	sort.Strings(names)
	want := []string{"chrome:a11y", "compact", "track-work"}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("got %v, want %v", names, want)
		}
	}
}

func TestRegistryLaterSourceShadowsEarlier(t *testing.T) {
	r := NewRegistry()
	r.Add(StaticSource(OriginBuiltin, []Command{ok("clear", "builtin")}))
	r.Add(StaticSource(OriginProject, []Command{ok("clear", "project")}))

	res, err := r.Execute(context.Background(), "/clear")
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || len(res.Output) != 1 || res.Output[0] != "project" {
		t.Fatalf("got %+v, want output [project]", res)
	}
}

func TestRegistryUnknownCommandFailsLoudly(t *testing.T) {
	r := NewRegistry()
	res, err := r.Execute(context.Background(), "/nope do things")
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || len(res.Output) == 0 {
		t.Fatalf("expected an Unknown command output, got %+v", res)
	}
	if got := res.Output[0]; !strings.Contains(got, "Unknown command") {
		t.Fatalf("got %q, want it to mention Unknown command", got)
	}
}

func TestRegistryPassesNonCommandsThrough(t *testing.T) {
	r := NewRegistry()
	res, err := r.Execute(context.Background(), "just a normal prompt")
	if err != nil {
		t.Fatal(err)
	}
	if res != nil {
		t.Fatalf("got %+v, want nil (pass through to the model)", res)
	}
}

func TestRegistryCollapsesRepeatedLeadingSlashes(t *testing.T) {
	r := NewRegistry()
	r.Add(StaticSource(OriginBuiltin, []Command{ok("model", "m")}))

	for _, line := range []string{"//model", "///model anthropic/claude"} {
		res, err := r.Execute(context.Background(), line)
		if err != nil {
			t.Fatal(err)
		}
		if res == nil || len(res.Output) != 1 || res.Output[0] != "m" {
			t.Fatalf("line %q: got %+v, want output [m]", line, res)
		}
	}
}

func TestRegistrySurvivesAFailingSource(t *testing.T) {
	r := NewRegistry()
	r.Add(Source{
		Origin: OriginProject,
		Load: func() ([]Command, error) {
			return nil, errors.New("bad yaml")
		},
	})
	r.Add(StaticSource(OriginBuiltin, []Command{ok("help", "h")}))

	list := r.List()
	if len(list) != 1 {
		t.Fatalf("got %d commands, want 1", len(list))
	}
}

func TestRegistryListIsCachedUntilInvalidate(t *testing.T) {
	r := NewRegistry()
	calls := 0
	r.Add(Source{
		Origin: OriginBuiltin,
		Load: func() ([]Command, error) {
			calls++
			return []Command{ok("help", "h")}, nil
		},
	})
	r.List()
	r.List()
	if calls != 1 {
		t.Fatalf("Load called %d times, want 1 (cached)", calls)
	}
	r.Invalidate()
	r.List()
	if calls != 2 {
		t.Fatalf("Load called %d times after Invalidate, want 2", calls)
	}
}

func TestAutocompleteItems(t *testing.T) {
	r := NewRegistry()
	r.Add(StaticSource(OriginBuiltin, []Command{
		{Name: "model", Description: "Show or change the active model", ArgumentHint: "<provider/model>"},
	}))
	items := r.AutocompleteItems()
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].Value != "/model" || items[0].Label != "/model <provider/model>" {
		t.Fatalf("got %+v", items[0])
	}
}

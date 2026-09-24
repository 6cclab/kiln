// Command harness-providers is a TEMPORARY dev binary for manually checking
// the phase-2 provider layer (registry, catalog, budget, auth) end to end.
// Phase 4 folds its subcommands into cmd/harness.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/auth"
	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/provider/builtin"
	fauxprovider "github.com/andrepato/harness/internal/provider/faux"
	"github.com/andrepato/harness/internal/provider/ollama"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "harness-providers:", err)
		os.Exit(1)
	}
}

func buildRegistry() *provider.Registry {
	store := auth.NewFileCredentialStore("")
	reg := provider.NewRegistry(store)
	reg.Register(builtin.NewAnthropicProvider(store))
	reg.Register(builtin.NewOpenAIProvider(store))
	reg.Register(ollama.New(ollamaOptionsFromEnv()))
	if fp, ok := fauxprovider.New(); ok {
		reg.Register(fp)
	}
	return reg
}

func ollamaOptionsFromEnv() ollama.Options {
	url := os.Getenv("OLLAMA_HOST")
	// Local models can take a while to load into memory on first use; the
	// package default (15s) is tuned for discovery calls, not generation.
	return ollama.Options{URL: url, HTTPClient: &http.Client{Timeout: 3 * time.Minute}}
}

func run(args []string) error {
	if len(args) == 0 {
		printHelp()
		return nil
	}
	switch args[0] {
	case "providers":
		return cmdProviders()
	case "models":
		var providerID string
		if len(args) > 1 {
			providerID = args[1]
		}
		return cmdModels(providerID)
	case "login":
		if len(args) < 2 {
			return fmt.Errorf("usage: harness-providers login <provider>")
		}
		return cmdLogin(args[1])
	case "logout":
		if len(args) < 2 {
			return fmt.Errorf("usage: harness-providers logout <provider>")
		}
		return cmdLogout(args[1])
	case "stream":
		if len(args) < 3 {
			return fmt.Errorf("usage: harness-providers stream <provider/model> \"<prompt>\"")
		}
		return cmdStream(args[1], args[2])
	default:
		printHelp()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func printHelp() {
	fmt.Println(`harness-providers: temporary dev CLI for the phase-2 provider layer

  providers                        list every registered provider, auth kind, authed?
  models [provider]                list models, context window, tier, strategy, usable
  login <provider>                 run a provider's login flow
  logout <provider>                remove a provider's stored credential
  stream <provider/model> "<...>"  stream a completion, printing deltas then usage`)
}

func cmdProviders() error {
	reg := buildRegistry()
	ctx := context.Background()
	fmt.Printf("  %-16s %-24s %-8s %-6s\n", "id", "name", "auth", "authed")
	for _, p := range reg.Providers() {
		spec := p.Auth()
		authed, err := reg.CheckAuth(ctx, p.ID())
		authedStr := "no"
		if err != nil {
			authedStr = "error: " + err.Error()
		} else if authed {
			authedStr = "yes"
		}
		fmt.Printf("  %-16s %-24s %-8s %-6s\n", p.ID(), p.Name(), spec.Kind, authedStr)
	}
	fmt.Println()
	fmt.Println("known, not yet streamable (catalog data only, no API client this phase):")
	notStreamable := builtin.KnownNotStreamable()
	sort.Strings(notStreamable)
	fmt.Println("  " + strings.Join(notStreamable, ", "))
	return nil
}

func cmdModels(providerID string) error {
	reg := buildRegistry()
	if providerID != "" {
		p, ok := reg.Provider(providerID)
		if !ok {
			return fmt.Errorf("unknown provider %q", providerID)
		}
		if err := p.RefreshModels(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "  (refresh failed: %v)\n", err)
		}
	}

	fmt.Printf("  %-16s %-34s %9s %7s %14s %10s\n", "provider", "model", "ctx", "tier", "strategy", "usable")
	for _, p := range reg.Providers() {
		if providerID != "" && p.ID() != providerID {
			continue
		}
		models := p.Models()
		sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
		for _, m := range models {
			tier, err := budget.RequireTierForWindow(m.ContextWindow)
			if err != nil {
				fmt.Printf("  %-16s %-34s %9s %s\n", p.ID(), truncate(m.ID, 34), "-", err.Error())
				continue
			}
			usable := budget.UsableTokens(tier)
			fmt.Printf("  %-16s %-34s %9d %7s %14s %10d\n", p.ID(), truncate(m.ID, 34), m.ContextWindow, tier.Name, tier.ToolStrategy, usable)
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func cmdLogin(providerID string) error {
	if providerID == "anthropic" {
		return fmt.Errorf("refusing to run the Anthropic OAuth login here: it opens a browser. Run it interactively yourself")
	}
	reg := buildRegistry()
	p, ok := reg.Provider(providerID)
	if !ok {
		return fmt.Errorf("unknown provider %q", providerID)
	}
	if providerID == "ollama" {
		url := os.Getenv("OLLAMA_HOST")
		if url == "" {
			url = ollama.DefaultURL
		}
		if err := ollama.Login(context.Background(), url, os.Getenv("OLLAMA_API_KEY")); err != nil {
			return fmt.Errorf("ollama login check failed: %w", err)
		}
		fmt.Printf("ollama: %s reachable\n", url)
		return nil
	}
	spec := p.Auth()
	if len(spec.EnvVars) == 0 {
		return fmt.Errorf("provider %q has no api-key env vars to check; nothing to do here", providerID)
	}
	fmt.Printf("Set one of %s in the environment, or use `harness login %s` once phase 4 lands.\n", strings.Join(spec.EnvVars, ", "), providerID)
	return nil
}

func cmdLogout(providerID string) error {
	reg := buildRegistry()
	if err := reg.Logout(providerID); err != nil {
		return err
	}
	fmt.Printf("Logged out of %s.\n", providerID)
	return nil
}

func cmdStream(providerModel, prompt string) error {
	parts := strings.SplitN(providerModel, "/", 2)
	if len(parts) != 2 {
		return fmt.Errorf("expected provider/model, got %q", providerModel)
	}
	providerID, modelID := parts[0], parts[1]

	reg := buildRegistry()
	if p, ok := reg.Provider(providerID); ok {
		_ = p.RefreshModels(context.Background())
	}
	resolved, err := reg.Resolve(providerID, modelID)
	if err != nil {
		return err
	}

	p, ok := reg.Provider(providerID)
	if !ok {
		return fmt.Errorf("unknown provider %q", providerID)
	}

	ctx := context.Background()
	transcript := []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text(prompt)}},
	}
	events, wait := p.Stream(ctx, resolved.Model, transcript, provider.StreamOptions{})
	for ev := range events {
		switch ev.Type {
		case "text_delta":
			fmt.Print(ev.Delta)
		case "thinking_delta":
			fmt.Fprint(os.Stderr, ev.Delta)
		}
	}
	final, err := wait()
	if err != nil {
		return err
	}
	fmt.Println()
	fmt.Printf("usage: input=%d output=%d cost=$%.6f stopReason=%s\n",
		final.Usage.Input, final.Usage.Output, final.Usage.Cost.Total, final.StopReason)
	return nil
}

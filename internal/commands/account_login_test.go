package commands

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/auth"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// keyProvider is an API-key provider with nothing else to it.
type keyProvider struct {
	id   string
	kind provider.AuthKind
	envs []string
}

func (p keyProvider) ID() string   { return p.id }
func (p keyProvider) Name() string { return strings.ToUpper(p.id) }
func (p keyProvider) Auth() provider.AuthSpec {
	return provider.AuthSpec{Kind: p.kind, EnvVars: p.envs}
}
func (p keyProvider) Models() []provider.Model            { return nil }
func (p keyProvider) RefreshModels(context.Context) error { return nil }
func (p keyProvider) Stream(context.Context, provider.Model, []msg.Message, provider.StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	return nil, nil
}

// scriptedIA answers prompts from a list and records what it was asked and
// told.
type scriptedIA struct {
	answers []string
	prompts []auth.Prompt
	events  []auth.Event
	err     error
}

func (s *scriptedIA) Prompt(_ context.Context, p auth.Prompt) (string, error) {
	s.prompts = append(s.prompts, p)
	if s.err != nil {
		return "", s.err
	}
	a := s.answers[0]
	s.answers = s.answers[1:]
	return a, nil
}
func (s *scriptedIA) Notify(e auth.Event) { s.events = append(s.events, e) }

func loginFixture(t *testing.T, ia auth.Interaction) (Command, Command, *provider.Registry) {
	t.Helper()
	store := auth.NewFileCredentialStore(filepath.Join(t.TempDir(), "credentials.json"))
	reg := provider.NewRegistry(store)
	reg.Register(keyProvider{id: "acme", kind: provider.AuthKindAPIKey, envs: []string{"ACME_TEST_KEY_UNSET"}})
	reg.Register(keyProvider{id: "local", kind: provider.AuthKindNone})
	src := AccountCommands(AccountDeps{Registry: reg, Interaction: func() auth.Interaction { return ia }})
	return findCmd(t, src, "login"), findCmd(t, src, "logout"), reg
}

func TestLoginWithProviderRunsInTheBackground(t *testing.T) {
	ia := &scriptedIA{answers: []string{"  sk-fake-key  "}}
	login, _, reg := loginFixture(t, ia)

	res, err := login.Run(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if res.Background == nil || len(res.Output) != 0 {
		t.Fatalf("result = %+v, want a Background command and no text", res)
	}
	if res.BusyLabel != "Logging in to ACME" || res.CancelNote == "" {
		t.Fatalf("label %q, cancel note %q", res.BusyLabel, res.CancelNote)
	}
	if len(ia.prompts) != 0 {
		t.Fatal("Run prompted on the event loop; the flow belongs in Background")
	}

	if _, err := res.Background(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ia.prompts) != 1 || ia.prompts[0].Type != auth.PromptSecret {
		t.Fatalf("prompts = %+v, want one secret prompt", ia.prompts)
	}
	last := ia.events[len(ia.events)-1]
	if last.Message != "Logged in to acme." {
		t.Fatalf("last notice = %q", last.Message)
	}
	// The running session sees the credential at once: the registry reads
	// its store on every check, it caches nothing.
	if ok, err := reg.CheckAuth(context.Background(), "acme"); err != nil || !ok {
		t.Fatalf("CheckAuth after login = %v, %v", ok, err)
	}
	cred, err := reg.Credentials().Read("acme")
	if err != nil || cred == nil || cred.APIKey == nil || cred.APIKey.Key != "sk-fake-key" {
		t.Fatalf("stored credential = %+v, %v", cred, err)
	}
}

func TestLoginWithoutAnInteractionKeepsTheOutsideSessionText(t *testing.T) {
	src := AccountCommands(AccountDeps{})
	res, err := findCmd(t, src, "login").Run(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if res.Background != nil || !strings.Contains(strings.Join(res.Output, "\n"), "kiln login acme") {
		t.Fatalf("result = %+v", res)
	}
	res, err = findCmd(t, src, "logout").Run(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if res.Background != nil || !strings.Contains(strings.Join(res.Output, "\n"), "kiln logout acme") {
		t.Fatalf("result = %+v", res)
	}
}

func TestLoginUnknownOrKeylessProviderIsAMistake(t *testing.T) {
	login, _, _ := loginFixture(t, &scriptedIA{})
	res, _ := login.Run(context.Background(), "nope")
	if !res.Mistake || res.Background != nil || !strings.Contains(res.Output[0], `Unknown provider "nope"`) || !strings.Contains(res.Output[0], "acme") {
		t.Fatalf("unknown: %+v", res)
	}
	res, _ = login.Run(context.Background(), "local")
	if !res.Mistake || res.Background != nil || !strings.Contains(res.Output[0], "needs no login") {
		t.Fatalf("keyless: %+v", res)
	}
}

func TestLoginWithNoProviderAsksWhichFirst(t *testing.T) {
	ia := &scriptedIA{answers: []string{"acme", "sk-picked"}}
	login, _, reg := loginFixture(t, ia)
	t.Setenv("ACME_TEST_KEY_UNSET", "")

	res, _ := login.Run(context.Background(), "")
	if res.Background == nil {
		t.Fatalf("result = %+v, want Background", res)
	}
	if _, err := res.Background(context.Background()); err != nil {
		t.Fatal(err)
	}
	pick := ia.prompts[0]
	if pick.Type != auth.PromptSelect || len(pick.Options) != 1 || pick.Options[0].ID != "acme" {
		t.Fatalf("picker = %+v, want only the provider with something to log in to", pick)
	}
	if strings.Contains(pick.Options[0].Description, "configured") {
		t.Fatalf("an unconfigured provider is marked configured: %+v", pick.Options[0])
	}
	if cred, _ := reg.Credentials().Read("acme"); cred == nil || cred.APIKey == nil || cred.APIKey.Key != "sk-picked" {
		t.Fatalf("stored credential = %+v", cred)
	}
}

func TestLoginPickerMarksConfiguredProviders(t *testing.T) {
	ia := &scriptedIA{answers: []string{"acme", "sk-new"}}
	login, _, reg := loginFixture(t, ia)
	if err := reg.Login("acme", "sk-old"); err != nil {
		t.Fatal(err)
	}
	res, _ := login.Run(context.Background(), "")
	if _, err := res.Background(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := ia.prompts[0].Options[0].Description; !strings.Contains(got, "configured") {
		t.Fatalf("description = %q, want it marked configured", got)
	}
}

func TestLoginCancelledAtThePickerSaysSoAndStoresNothing(t *testing.T) {
	ia := &scriptedIA{err: context.Canceled}
	login, _, reg := loginFixture(t, ia)
	res, _ := login.Run(context.Background(), "")
	out, err := res.Background(context.Background())
	if err != nil || strings.Join(out.Output, "") != "Login cancelled." {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	if cred, _ := reg.Credentials().Read("acme"); cred != nil {
		t.Fatalf("a cancelled login stored %+v", cred)
	}
}

func TestLoginCancelledAtTheKeyPromptDoesNotStore(t *testing.T) {
	ia := &scriptedIA{err: context.Canceled}
	login, _, reg := loginFixture(t, ia)
	res, _ := login.Run(context.Background(), "acme")
	if _, err := res.Background(context.Background()); err != nil {
		t.Fatal(err)
	}
	if last := ia.events[len(ia.events)-1].Message; !strings.HasPrefix(last, "Login failed") {
		t.Fatalf("last notice = %q", last)
	}
	if cred, _ := reg.Credentials().Read("acme"); cred != nil {
		t.Fatalf("a cancelled login stored %+v", cred)
	}
}

func TestLogoutRemovesTheCredentialInSession(t *testing.T) {
	login, logout, reg := loginFixture(t, &scriptedIA{})
	_ = login
	if err := reg.Login("acme", "sk-old"); err != nil {
		t.Fatal(err)
	}
	res, _ := logout.Run(context.Background(), "acme")
	if res.Background != nil || strings.Join(res.Output, "\n") != "Logged out of acme." {
		t.Fatalf("result = %+v", res)
	}
	if ok, _ := reg.CheckAuth(context.Background(), "acme"); ok {
		t.Fatal("still configured after /logout")
	}
}

func TestLogoutSaysWhenTheEnvironmentStillConfiguresIt(t *testing.T) {
	_, logout, reg := loginFixture(t, &scriptedIA{})
	if err := reg.Login("acme", "sk-old"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACME_TEST_KEY_UNSET", "from-env")
	res, _ := logout.Run(context.Background(), "acme")
	if !strings.Contains(strings.Join(res.Output, "\n"), "still configured through its environment variable") {
		t.Fatalf("result = %+v", res)
	}
}

func TestLogoutUnknownProviderIsAMistake(t *testing.T) {
	_, logout, _ := loginFixture(t, &scriptedIA{})
	res, _ := logout.Run(context.Background(), "nope")
	if !res.Mistake || !strings.Contains(res.Output[0], "unknown provider") {
		t.Fatalf("result = %+v", res)
	}
}

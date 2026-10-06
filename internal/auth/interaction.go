package auth

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/andrepato/harness/internal/crash"
)

// Terminal implementation of the login prompt/notify surface.
//
// This is the whole app-side cost of subscription auth: PKCE, device-code,
// token exchange and refresh live in internal/auth/oauth/*.go. The harness
// only has to render four prompt kinds and four event kinds.
//
// Port of harness/src/auth/interaction.ts.

// PromptKind is the kind of a login prompt.
type PromptKind string

const (
	PromptText       PromptKind = "text"
	PromptSecret     PromptKind = "secret"
	PromptSelect     PromptKind = "select"
	PromptManualCode PromptKind = "manual_code"
)

// SelectOption is one choice in a PromptSelect prompt.
type SelectOption struct {
	ID          string
	Label       string
	Description string
}

// Prompt is a request for terminal input during login.
type Prompt struct {
	Type        PromptKind
	Message     string
	Placeholder string
	Options     []SelectOption
}

// EventKind is the kind of a login notification.
type EventKind string

const (
	EventInfo       EventKind = "info"
	EventAuthURL    EventKind = "auth_url"
	EventDeviceCode EventKind = "device_code"
	EventProgress   EventKind = "progress"
)

// InfoLink is a labeled URL attached to an EventInfo notification.
type InfoLink struct {
	URL   string
	Label string
}

// Event is a login-flow notification.
type Event struct {
	Type EventKind

	// EventInfo
	Message string
	Links   []InfoLink

	// EventAuthURL
	URL          string
	Instructions string

	// EventDeviceCode
	UserCode         string
	VerificationURI  string
	IntervalSeconds  int
	ExpiresInSeconds int
}

// Interaction is login interaction callbacks serving both api-key and OAuth
// flows.
type Interaction interface {
	Prompt(ctx context.Context, p Prompt) (string, error)
	Notify(e Event)
}

// TerminalOptions configures a terminal Interaction.
type TerminalOptions struct {
	// PrintOnly prints the auth URL instead of opening a browser. Defaults
	// to true over SSH or when stdout is not a TTY.
	PrintOnly *bool
	In        io.Reader
	Out       io.Writer
}

// IsHeadless reports whether the process looks like an SSH session or has no
// TTY on stdout, matching interaction.ts's isHeadless().
func IsHeadless() bool {
	if os.Getenv("SSH_TTY") != "" || os.Getenv("SSH_CONNECTION") != "" {
		return true
	}
	return !isTTY(os.Stdout)
}

type terminalInteraction struct {
	printOnly bool
	in        *bufio.Reader
	out       io.Writer
}

// NewTerminalInteraction builds a terminal Interaction.
func NewTerminalInteraction(opts TerminalOptions) Interaction {
	in := opts.In
	if in == nil {
		in = os.Stdin
	}
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	printOnly := IsHeadless()
	if opts.PrintOnly != nil {
		printOnly = *opts.PrintOnly
	}
	return &terminalInteraction{printOnly: printOnly, in: bufio.NewReader(in), out: out}
}

func (t *terminalInteraction) readLine(ctx context.Context) (string, error) {
	// bufio.Reader.ReadString does not honor ctx; a background goroutine
	// races it against cancellation, matching pi's abort-signal-aware
	// rl.question().
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	crash.Go(func() {
		line, err := t.in.ReadString('\n')
		ch <- result{line, err}
	})
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-ch:
		if r.err != nil && r.err != io.EOF {
			return "", r.err
		}
		return strings.TrimRight(r.line, "\r\n"), nil
	}
}

func (t *terminalInteraction) Prompt(ctx context.Context, p Prompt) (string, error) {
	if p.Type == PromptSelect {
		fmt.Fprintf(t.out, "\n%s\n", p.Message)
		for i, o := range p.Options {
			desc := ""
			if o.Description != "" {
				desc = " - " + o.Description
			}
			fmt.Fprintf(t.out, "  %d) %s%s\n", i+1, o.Label, desc)
		}
		// Loop rather than error: a typo during a login flow should not
		// discard a half-finished OAuth handshake.
		for {
			fmt.Fprint(t.out, "> ")
			raw, err := t.readLine(ctx)
			if err != nil {
				return "", err
			}
			raw = strings.TrimSpace(raw)
			if idx, err := strconv.Atoi(raw); err == nil {
				if idx >= 1 && idx <= len(p.Options) {
					return p.Options[idx-1].ID, nil
				}
			}
			for _, o := range p.Options {
				if o.ID == raw || o.Label == raw {
					return o.ID, nil
				}
			}
			fmt.Fprintf(t.out, "  enter 1-%d\n", len(p.Options))
		}
	}

	suffix := ""
	if p.Placeholder != "" {
		suffix = fmt.Sprintf(" [%s]", p.Placeholder)
	}
	fmt.Fprintf(t.out, "%s%s: ", p.Message, suffix)
	// Blank means "accept the placeholder"; providers read the placeholder
	// as their default, so the empty string is passed through.
	return t.readLine(ctx)
}

func (t *terminalInteraction) Notify(e Event) {
	switch e.Type {
	case EventInfo:
		fmt.Fprintln(t.out, e.Message)
		for _, l := range e.Links {
			label := ""
			if l.Label != "" {
				label = l.Label + ": "
			}
			fmt.Fprintf(t.out, "  %s%s\n", label, l.URL)
		}
	case EventAuthURL:
		instructions := e.Instructions
		if instructions == "" {
			instructions = "Open this URL to authorize:"
		}
		fmt.Fprintf(t.out, "\n%s\n", instructions)
		fmt.Fprintf(t.out, "\n  %s\n\n", e.URL)
		if !t.printOnly {
			openBrowser(e.URL)
		}
	case EventDeviceCode:
		// Both halves matter and users miss one of them constantly, so give
		// the code its own line rather than burying it.
		fmt.Fprintf(t.out, "\nGo to: %s\n", e.VerificationURI)
		fmt.Fprintf(t.out, "Enter code: %s\n\n", e.UserCode)
		if e.ExpiresInSeconds > 0 {
			fmt.Fprintf(t.out, "(expires in %d min)\n", (e.ExpiresInSeconds+30)/60)
		}
		if !t.printOnly {
			openBrowser(e.VerificationURI)
		}
	case EventProgress:
		fmt.Fprintf(t.out, "... %s\n", e.Message)
	}
}

// openBrowser is a best-effort browser open. Never blocks the caller on
// failure: the URL is always printed as well.
func openBrowser(url string) {
	var cmd string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd = "start"
	default:
		cmd = "xdg-open"
	}
	c := exec.Command(cmd, url)
	// Detached: the opener must outlive this process's own lifetime and
	// must not hold stdio open.
	c.Stdin, c.Stdout, c.Stderr = nil, nil, nil
	_ = c.Start()
	if c.Process != nil {
		crash.Go(func() { _, _ = c.Process.Wait() })
	}
}

func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

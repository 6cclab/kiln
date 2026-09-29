//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestTUI_BracketedPasteReachesTheInput: text pasted into the terminal
// (sent as a bracketed paste, as iTerm2, Terminal.app and Warp do) lands in
// the input box, in fullscreen and inline mode.
func TestTUI_BracketedPasteReachesTheInput(t *testing.T) {
	for _, mode := range []string{"--fullscreen", "--inline"} {
		t.Run(mode, func(t *testing.T) {
			home, sessDir := scratchHome(t)
			proj := scratchProject(t)
			addr, _ := startFaux(t, "model: faux-1\nsteps: []\n")
			s := startTUI(t, 100, 30, proj, home, sessDir, addr, mode)
			defer s.Close()
			waitReady(t, s)
			s.Send("\x1b[200~pasted words here\x1b[201~")
			if err := s.WaitFor("pasted words here", 3*time.Second); err != nil {
				t.Fatalf("paste never reached the input: %v\n%s", err, strings.Join(s.Rows(), "\n"))
			}
		})
	}
}

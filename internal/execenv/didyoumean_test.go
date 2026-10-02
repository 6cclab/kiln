package execenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"
)

func TestDidYouMeanHint_NarrowNoBreakSpace(t *testing.T) {
	dir := t.TempDir()
	real := "Screenshot 2026-10-02 at 11.43.28" + " " + "AM.png"
	if err := os.WriteFile(filepath.Join(dir, real), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := New(dir)
	requested := filepath.Join(dir, "Screenshot 2026-10-02 at 11.43.28 AM.png")

	hint := env.DidYouMeanHint(requested)
	if !strings.Contains(hint, `Did you mean "`+displayInvisibles(real)+`"`) {
		t.Fatalf("hint = %q, want it to name %q", hint, real)
	}
	if !strings.Contains(hint, "U+202F NARROW NO-BREAK SPACE") {
		t.Errorf("hint = %q, want it to call out U+202F", hint)
	}
	if !strings.Contains(hint, "a regular space (U+0020)") {
		t.Errorf("hint = %q, want it to name what was written", hint)
	}
}

func TestDidYouMeanHint_NoBreakSpace(t *testing.T) {
	dir := t.TempDir()
	real := "Invoice" + " " + "Final.pdf"
	if err := os.WriteFile(filepath.Join(dir, real), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := New(dir)
	requested := filepath.Join(dir, "Invoice Final.pdf")

	hint := env.DidYouMeanHint(requested)
	if !strings.Contains(hint, `Did you mean "`+displayInvisibles(real)+`"`) {
		t.Fatalf("hint = %q, want it to name %q", hint, real)
	}
	if !strings.Contains(hint, "U+00A0 NO-BREAK SPACE") {
		t.Errorf("hint = %q, want it to call out U+00A0", hint)
	}
}

func TestDidYouMeanHint_NFDvsNFC(t *testing.T) {
	dir := t.TempDir()
	// "café.txt" written in NFD (e + combining acute), as macOS's
	// filesystem stores it regardless of which form was asked to write;
	// doing it explicitly here keeps the test true on any OS.
	realNFD := norm.NFD.String("café.txt")
	if err := os.WriteFile(filepath.Join(dir, realNFD), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := New(dir)
	// The model asks for the precomposed NFC spelling.
	requested := filepath.Join(dir, norm.NFC.String("café.txt"))

	hint := env.DidYouMeanHint(requested)
	if hint == "" {
		t.Fatal("expected a hint for the NFD/NFC spelling mismatch, got none")
	}
	if !strings.Contains(hint, "Did you mean") {
		t.Errorf("hint = %q, want a Did-you-mean hint", hint)
	}
}

func TestDidYouMeanHint_CaseOnlyIsLastResort(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := New(dir)
	requested := filepath.Join(dir, "readme.txt")

	hint := env.DidYouMeanHint(requested)
	if !strings.Contains(hint, `"README.txt"`) {
		t.Fatalf("hint = %q, want it to name README.txt", hint)
	}
}

// TestDidYouMeanHint_ExactListedBeforeCaseOnly: with both an
// exact-normalised match (after folding a narrow-no-break-space to a
// plain space) and a case-only match for the same requested name present
// in the same directory, the hint lists the exact match and does not mix
// in the case-only one ahead of it.
func TestDidYouMeanHint_ExactListedBeforeCaseOnly(t *testing.T) {
	dir := t.TempDir()
	exactReal := "notes" + " " + "final.txt" // space-variant, otherwise same case
	caseReal := "NOTES FINAL.txt"            // case-only match
	for _, name := range []string{exactReal, caseReal} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := New(dir)
	requested := filepath.Join(dir, "notes final.txt")

	hint := env.DidYouMeanHint(requested)
	exactIdx := strings.Index(hint, displayInvisibles(exactReal))
	caseIdx := strings.Index(hint, caseReal)
	if exactIdx == -1 {
		t.Fatalf("hint = %q, want the exact match named", hint)
	}
	if caseIdx == -1 {
		t.Fatalf("hint = %q, want the case-only match named too", hint)
	}
	if caseIdx < exactIdx {
		t.Errorf("hint = %q, want the exact match listed before the case-only one", hint)
	}
}

func TestDidYouMeanHint_NoMatchNoHint(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := New(dir)
	requested := filepath.Join(dir, "totally-different-name.txt")

	if hint := env.DidYouMeanHint(requested); hint != "" {
		t.Fatalf("hint = %q, want no hint for an unrelated directory", hint)
	}
}

func TestDidYouMeanHint_DeniedDirectoryProducesNoHint(t *testing.T) {
	dir := t.TempDir()
	real := "Screenshot" + " " + "AM.png"
	if err := os.WriteFile(filepath.Join(dir, real), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := New(dir)
	env.DidYouMeanDirAllowed = func(d string) bool { return false }
	requested := filepath.Join(dir, "Screenshot AM.png")

	if hint := env.DidYouMeanHint(requested); hint != "" {
		t.Fatalf("hint = %q, want no hint when DidYouMeanDirAllowed refuses the directory", hint)
	}

	// Sanity check: the same setup without the denial does produce a hint,
	// so the denial above is actually exercising the refusal path rather
	// than some other reason the hint came back empty.
	env.DidYouMeanDirAllowed = nil
	if hint := env.DidYouMeanHint(requested); hint == "" {
		t.Fatal("expected a hint once DidYouMeanDirAllowed is cleared")
	}
}

// TestDidYouMeanHint_ScanCapStopsAtLimit: a directory with more entries
// than maxDidYouMeanEntries still returns promptly and does not find a
// match placed past the cap, proving the scan actually stops rather than
// just being slow to reach it.
func TestDidYouMeanHint_ScanCapStopsAtLimit(t *testing.T) {
	dir := t.TempDir()
	oldCap := maxDidYouMeanEntries
	maxDidYouMeanEntries = 50
	defer func() { maxDidYouMeanEntries = oldCap }()

	// Filler entries sort before "zzz-match.txt" alphabetically... but
	// os.ReadDir's order, not sorted-ness, is what the cap walks, and
	// ReadDir already returns entries sorted by name. So name the match so
	// it sorts after the cap's worth of filler, proving the cap -- not
	// chance -- is what hides it.
	for i := 0; i < 200; i++ {
		name := fmt.Sprintf("aaa-filler-%04d.txt", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	match := "zzz-match AM.png"
	if err := os.WriteFile(filepath.Join(dir, match), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	env := New(dir)
	requested := filepath.Join(dir, "zzz-match"+" "+"AM.png")
	if hint := env.DidYouMeanHint(requested); hint != "" {
		t.Fatalf("hint = %q, want no hint: the match sorts past the %d-entry cap", hint, maxDidYouMeanEntries)
	}

	// With the cap lifted back past 200+1 entries, the same match is found.
	maxDidYouMeanEntries = 1000
	if hint := env.DidYouMeanHint(requested); hint == "" {
		t.Fatal("expected a hint once the cap is raised above the directory's entry count")
	}
}

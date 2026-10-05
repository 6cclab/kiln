package imgpath

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withinImgpathTest fails the test unless fn returns within the deadline:
// a size refusal built from os.Stat alone must not actually read the
// file, so it must be fast regardless of its declared size. Mirrors
// internal/gitfiles/hostile_test.go's own within.
func withinImgpathTest(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		fmt.Fprintf(os.Stderr, "--- FAIL: %s: %s did not return within half a second\n", t.Name(), what)
		os.Exit(1)
	}
}

func TestCleanPathToken(t *testing.T) {
	narrow := " " // NARROW NO-BREAK SPACE, as in a macOS screenshot name
	in := `/Users/andrepato/Desktop/Screenshot\ 2026-10-02\ at\ 11.43.28` + narrow + `AM.png`
	want := "/Users/andrepato/Desktop/Screenshot 2026-10-02 at 11.43.28" + narrow + "AM.png"
	if got := CleanPathToken(in); got != want {
		t.Errorf("CleanPathToken(%q) = %q, want %q", in, got, want)
	}

	if got := CleanPathToken(`"/tmp/has space.png"`); got != "/tmp/has space.png" {
		t.Errorf("quoted path: got %q", got)
	}
	if got := CleanPathToken(`/tmp/back\\slash.png`); got != `/tmp/back\slash.png` {
		t.Errorf(`\\ -> \: got %q`, got)
	}
	if got := CleanPathToken("  /tmp/x.png  "); got != "/tmp/x.png" {
		t.Errorf("trim: got %q", got)
	}
}

func TestHasUnescapedWhitespace(t *testing.T) {
	if HasUnescapedWhitespace(`/tmp/a\ b.png`) {
		t.Error("escaped space must not count as whitespace")
	}
	if !HasUnescapedWhitespace("/tmp/a b.png") {
		t.Error("a raw space must count as whitespace")
	}
	if HasUnescapedWhitespace("/tmp/a b.png") {
		t.Error("U+202F must not count as whitespace (would cut the filename)")
	}
}

func TestResolve(t *testing.T) {
	dir := t.TempDir()
	pngPath := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(pngPath, []byte("fake-png-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	txtPath := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(txtPath, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	bmpPath := filepath.Join(dir, "old.bmp")
	if err := os.WriteFile(bmpPath, []byte("bmp-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("existing png attaches", func(t *testing.T) {
		v, ok := Resolve(pngPath, dir)
		if !ok {
			t.Fatal("want ok")
		}
		if v.Image == nil {
			t.Fatal("want an attached image")
		}
		if v.Image.MimeType != "image/png" {
			t.Errorf("mime = %q", v.Image.MimeType)
		}
		if v.Refused != "" {
			t.Errorf("Refused = %q, want none", v.Refused)
		}
	})

	t.Run("escaped-space path with U+202F attaches", func(t *testing.T) {
		narrow := " "
		escaped := filepath.Join(dir, "Screenshot 2026-10-02 at 11.43.28"+narrow+"AM.png")
		if err := os.WriteFile(escaped, []byte("png"), 0o644); err != nil {
			t.Fatal(err)
		}
		raw := strings.ReplaceAll(escaped, " ", `\ `)
		v, ok := Resolve(raw, dir)
		if !ok || v.Image == nil {
			t.Fatalf("Resolve(%q) ok=%v image=%v, want attached", raw, ok, v.Image)
		}
		if v.CleanPath != escaped {
			t.Errorf("CleanPath = %q, want %q", v.CleanPath, escaped)
		}
	})

	t.Run("non-image file does not resolve", func(t *testing.T) {
		if _, ok := Resolve(txtPath, dir); ok {
			t.Error("a .txt path must not resolve as an image candidate")
		}
	})

	t.Run("missing file does not resolve", func(t *testing.T) {
		if _, ok := Resolve(filepath.Join(dir, "nope.png"), dir); ok {
			t.Error("a missing file must not resolve (text stays as typed)")
		}
	})

	t.Run("bmp is refused, not attached", func(t *testing.T) {
		v, ok := Resolve(bmpPath, dir)
		if !ok {
			t.Fatal("want ok (recognised extension)")
		}
		if v.Image != nil {
			t.Error("bmp must not attach")
		}
		if v.Refused == "" {
			t.Error("want a Refused reason")
		}
	})

	t.Run("oversized png is refused without being read", func(t *testing.T) {
		path := filepath.Join(dir, "huge.png")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(MaxImageBytes + 1); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}

		withinImgpathTest(t, "Resolve on an oversized sparse image", func() {
			v, ok := Resolve(path, dir)
			if !ok {
				t.Error("want ok (recognised extension)")
				return
			}
			if v.Image != nil {
				t.Error("an oversized image must not attach")
			}
			if v.Refused == "" {
				t.Error("want a Refused reason")
			}
		})
	})
}

func TestLeadingTokenIsPath(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		line string
		want bool
	}{
		{"/mcp", false},
		{"/model opus", false},
		{"/ns:cmd", false},
		{existing + " please read this", true},
		{"/tmp/does/not/exist.png more words", true}, // second "/" -> a path, not a command name
		{`/Users/x\ y.png`, true},                    // backslash escape -> a path
	}
	for _, c := range cases {
		if got := LeadingTokenIsPath(c.line, dir); got != c.want {
			t.Errorf("LeadingTokenIsPath(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

func TestResolveEmbedded(t *testing.T) {
	dir := t.TempDir()
	png := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(png, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	line := "Add the kiln " + png + " header to the readme"
	got, images, refusals := ResolveEmbedded(line, dir, 0)
	if len(images) != 1 {
		t.Fatalf("got %d images, want 1", len(images))
	}
	if refusals != nil {
		t.Errorf("refusals = %v, want none for an ordinary small image", refusals)
	}
	want := "Add the kiln [Image #1] header to the readme"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	t.Run("no match leaves line untouched", func(t *testing.T) {
		line := "just some /tmp/nonexistent.png text"
		got, images, refusals := ResolveEmbedded(line, dir, 0)
		if images != nil {
			t.Errorf("got %d images, want 0", len(images))
		}
		if refusals != nil {
			t.Errorf("refusals = %v, want none for a path that is not on disk", refusals)
		}
		if got != line {
			t.Errorf("got %q, want unchanged %q", got, line)
		}
	})

	t.Run("a refused embedded image is reported, not silently skipped", func(t *testing.T) {
		bmp := filepath.Join(dir, "old.bmp")
		if err := os.WriteFile(bmp, []byte("bmp-bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
		line := "see " + bmp + " for the mock"
		got, images, refusals := ResolveEmbedded(line, dir, 0)
		if images != nil {
			t.Errorf("got %d images, want 0 (bmp is refused)", len(images))
		}
		if got != line {
			t.Errorf("got %q, want the line left as typed", got)
		}
		if len(refusals) != 1 || !strings.Contains(refusals[0], "bmp") {
			t.Errorf("refusals = %v, want one reason naming bmp", refusals)
		}
	})
}

func TestResolveExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "shot.png"), noisyPNG(t, 8, 8), 0o644); err != nil {
		t.Fatal(err)
	}
	v, ok := Resolve("~/shot.png", t.TempDir())
	if !ok || v.Image == nil {
		t.Fatalf("Resolve(~/shot.png) = ok %v, image %v, refused %q; want the home-relative file attached", ok, v.Image != nil, v.Refused)
	}
	if want := filepath.Join(home, "shot.png"); v.CleanPath != want {
		t.Errorf("CleanPath = %q, want %q", v.CleanPath, want)
	}
	text, imgs, _ := ResolveEmbedded("see ~/shot.png please", t.TempDir(), 0)
	if len(imgs) != 1 || text != "see [Image #1] please" {
		t.Errorf("ResolveEmbedded = %q with %d images, want one [Image #1]", text, len(imgs))
	}
}

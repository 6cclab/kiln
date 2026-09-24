package execenv

import "testing"

func TestTruncateHeadNoTruncation(t *testing.T) {
	r := TruncateHead("a\nb\nc", TruncateOptions{})
	if r.Truncated {
		t.Fatalf("expected no truncation, got %+v", r)
	}
	if r.Content != "a\nb\nc" {
		t.Fatalf("content changed: %q", r.Content)
	}
}

func TestTruncateHeadByLines(t *testing.T) {
	r := TruncateHead("1\n2\n3\n4\n5\n", TruncateOptions{MaxLines: 2, MaxBytes: 1024})
	if !r.Truncated || r.TruncatedBy != TruncatedByLines {
		t.Fatalf("expected line truncation, got %+v", r)
	}
	if r.Content != "1\n2" {
		t.Fatalf("content = %q, want %q", r.Content, "1\n2")
	}
	if r.TotalLines != 5 {
		t.Fatalf("totalLines = %d, want 5", r.TotalLines)
	}
}

func TestTruncateHeadFirstLineExceedsLimit(t *testing.T) {
	r := TruncateHead("aaaaaaaaaa\nb", TruncateOptions{MaxLines: 100, MaxBytes: 5})
	if !r.FirstLineExceedsLimit {
		t.Fatalf("expected FirstLineExceedsLimit, got %+v", r)
	}
	if r.Content != "" {
		t.Fatalf("expected empty content, got %q", r.Content)
	}
}

func TestTruncateTailByLines(t *testing.T) {
	r := TruncateTail("1\n2\n3\n4\n5", TruncateOptions{MaxLines: 2, MaxBytes: 1024})
	if !r.Truncated || r.TruncatedBy != TruncatedByLines {
		t.Fatalf("expected line truncation, got %+v", r)
	}
	if r.Content != "4\n5" {
		t.Fatalf("content = %q, want %q", r.Content, "4\n5")
	}
}

func TestTruncateTailByBytes(t *testing.T) {
	r := TruncateTail("aaaa\nbbbb\ncccc", TruncateOptions{MaxLines: 100, MaxBytes: 6})
	if !r.Truncated || r.TruncatedBy != TruncatedByBytes {
		t.Fatalf("expected byte truncation, got %+v", r)
	}
	if r.Content != "cccc" {
		t.Fatalf("content = %q, want %q", r.Content, "cccc")
	}
}

func TestTruncateTailLastLinePartial(t *testing.T) {
	r := TruncateTail("0123456789", TruncateOptions{MaxLines: 100, MaxBytes: 4})
	if !r.LastLinePartial {
		t.Fatalf("expected LastLinePartial, got %+v", r)
	}
	if r.Content != "6789" {
		t.Fatalf("content = %q, want %q", r.Content, "6789")
	}
}

func TestFormatSize(t *testing.T) {
	cases := map[int]string{
		0:               "0B",
		512:             "512B",
		1024:            "1.0KB",
		1536:            "1.5KB",
		1024 * 1024:     "1.0MB",
		1024 * 1024 * 3: "3.0MB",
	}
	for bytes, want := range cases {
		if got := FormatSize(bytes); got != want {
			t.Errorf("FormatSize(%d) = %q, want %q", bytes, got, want)
		}
	}
}

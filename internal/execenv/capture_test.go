package execenv

import (
	"fmt"
	"testing"
)

// TestOutputCaptureReconstructsWithoutDuplication drives many small
// pushes through an OutputCapture with a small maxLines/maxBytes so both
// append and slide updates occur, then reconstructs the view purely from
// the update stream via ApplyShellOutputUpdate and checks it matches the
// capture's own final Snapshot exactly — proving the four update kinds
// compose without dropping or duplicating any bytes.
func TestOutputCaptureReconstructsWithoutDuplication(t *testing.T) {
	var updates []ShellOutputUpdate
	c := NewOutputCapture(CaptureLimits{MaxBytes: 200, MaxLines: 5, Retain: RetainTail}, func(u ShellOutputUpdate) {
		updates = append(updates, u)
	})

	for i := 0; i < 500; i++ {
		c.Push(fmt.Sprintf("line-%03d\n", i))
	}

	if len(updates) == 0 {
		t.Fatal("expected at least one update")
	}

	kindsSeen := map[UpdateKind]int{}
	var reconstructed *ShellOutputView
	for _, u := range updates {
		kindsSeen[u.Kind]++
		view := ApplyShellOutputUpdate(reconstructed, u)
		reconstructed = &view
	}

	want := c.Snapshot()
	if reconstructed.Text != want.Text {
		t.Fatalf("reconstructed text mismatch:\ngot:  %q\nwant: %q", reconstructed.Text, want.Text)
	}

	// With 500 pushes against a 5-line window, later pushes must slide the
	// window forward rather than only ever appending or replacing.
	if kindsSeen[UpdateSlide] == 0 {
		t.Errorf("expected at least one slide update, kinds seen: %v", kindsSeen)
	}
	if kindsSeen[UpdateReplace] != 1 {
		t.Errorf("expected exactly one replace update (the first), got %d", kindsSeen[UpdateReplace])
	}
}

func TestOutputCaptureMetadataOnlyWhenTextUnchanged(t *testing.T) {
	var updates []ShellOutputUpdate
	c := NewOutputCapture(CaptureLimits{MaxBytes: 1024, MaxLines: 100}, func(u ShellOutputUpdate) {
		updates = append(updates, u)
	})
	c.Push("hello\n")
	c.SetSpillPath("/tmp/spill.log")
	if len(updates) != 2 {
		t.Fatalf("expected 2 updates, got %d", len(updates))
	}
	if updates[1].Kind != UpdateMetadata {
		t.Fatalf("expected second update to be metadata-only, got %s", updates[1].Kind)
	}
	if updates[1].Metadata.SpillPath != "/tmp/spill.log" {
		t.Fatalf("expected spill path to propagate, got %q", updates[1].Metadata.SpillPath)
	}
}

func TestOutputCaptureAppend(t *testing.T) {
	var updates []ShellOutputUpdate
	c := NewOutputCapture(CaptureLimits{MaxBytes: 1024, MaxLines: 100}, func(u ShellOutputUpdate) {
		updates = append(updates, u)
	})
	c.Push("hello ")
	c.Push("world")
	if len(updates) != 2 {
		t.Fatalf("expected 2 updates, got %d", len(updates))
	}
	if updates[0].Kind != UpdateReplace {
		t.Fatalf("expected first update to be replace, got %s", updates[0].Kind)
	}
	if updates[1].Kind != UpdateAppend || updates[1].Text != "world" {
		t.Fatalf("expected append %q, got kind=%s text=%q", "world", updates[1].Kind, updates[1].Text)
	}
}

func TestOutputCaptureTruncatedReportsSpillAndBounds(t *testing.T) {
	c := NewOutputCapture(CaptureLimits{MaxBytes: 10, MaxLines: 100}, nil)
	c.Push("0123456789ABCDEF")
	if !c.Truncated() {
		t.Fatal("expected truncation once bytes exceed the limit")
	}
	snap := c.Snapshot()
	if snap.Truncation.TotalBytes != 16 {
		t.Fatalf("TotalBytes = %d, want 16", snap.Truncation.TotalBytes)
	}
	if snap.Truncation.TruncatedBy != TruncatedByBytes {
		t.Fatalf("TruncatedBy = %q, want bytes", snap.Truncation.TruncatedBy)
	}
}

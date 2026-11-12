package tools

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestTaskArtifactDetailsPreserveBaseline(t *testing.T) {
	artifact := &TaskArtifact{BaseCommit: "head", PatchPath: "changes.patch", Baseline: &TaskBaseline{Tree: "tree", State: "state"}}
	details, err := taskArtifactDetails(artifact)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Artifact TaskArtifact `json:"artifact"`
	}
	if err := json.Unmarshal(details, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Artifact.Baseline == nil || decoded.Artifact.Baseline.Tree != "tree" || decoded.Artifact.Baseline.State != "state" {
		t.Fatalf("baseline lost: %s", details)
	}
	if !bytes.Contains(details, []byte(`"baseline"`)) {
		t.Fatalf("baseline omitted from tool details: %s", details)
	}
}

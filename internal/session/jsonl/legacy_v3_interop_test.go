package jsonl

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/session"
)

// harnessNodeRepo is the TypeScript reference checkout whose node_modules
// carries the actual pi-agent-core this port mirrors. Read-only: this test
// never writes into it.
const harnessNodeRepo = "/Users/andrepato/projects/harness"

// nodeLegacyV3UpgradeScript opens the same legacy v3 fixture pi's own way
// (JsonlSessionRepo.open, which detects the v3 header and replays it via
// LegacyV3Source, exactly what storage.js's openLegacyV3 does) and prints
// one JSON line describing the structure and derived values it produced —
// the same things TestOpenLegacyV3Fixture asserts about the Go side. Ids
// are deliberately not printed: both sides mint fresh uuidv7 ids for
// retained entries (legacy-v3.js:239 vs. uuidv7At in this package), so
// they can never be expected to match, only the topology and derived
// values can.
const nodeLegacyV3UpgradeScript = `
import { JsonlSessionRepo } from "@earendil-works/pi-agent-core/harness/session";
import { NodeExecutionEnv } from "@earendil-works/pi-agent-core/node";
import { BACKGROUND_CONTEXT } from "@earendil-works/pi-agent-core/harness/context";

const fixturePath = process.env.HARNESS_INTEROP_FIXTURE;
const cwd = process.env.HARNESS_INTEROP_CWD;
const id = process.env.HARNESS_INTEROP_ID;
const createdAt = Number(process.env.HARNESS_INTEROP_CREATED_AT);

const env = new NodeExecutionEnv({ cwd });
const repo = new JsonlSessionRepo({ fileSystem: env, sessionsRoot: cwd });
const metadata = { id, cwd, path: fixturePath, createdAt, storageVersion: 1, modifiedAt: 0 };
const session = await repo.open(metadata, BACKGROUND_CONTEXT);

const entries = await session.findEntries({ order: "asc" }, BACKGROUND_CONTEXT);
const labelTargetId = entries[1].id; // e2, seq 2: the entry legacy-v3-fixture.jsonl labels "important"
const tip = await session.getValue({ namespace: "pi.branch.tip", key: "main", kind: "value" }, BACKGROUND_CONTEXT);
const cfg = await session.getValue({ namespace: "pi.lane.config", key: "main", kind: "value" }, BACKGROUND_CONTEXT);
const state = await session.getValue({ namespace: "pi.lane.state", key: "main", kind: "value" }, BACKGROUND_CONTEXT);
const label = await session.getValue({ namespace: "pi.entry.label", key: labelTargetId, kind: "value" }, BACKGROUND_CONTEXT);
const name = await session.getValue({ namespace: "pi.session.name", key: "", kind: "value" }, BACKGROUND_CONTEXT);
const stats = await session.getStats(BACKGROUND_CONTEXT);

console.log(JSON.stringify({
	entries: entries.map((e) => ({ type: e.type, customType: e.customType ?? "", seq: e.seq, hasParent: e.parentId !== null })),
	tipPresent: tip !== undefined && tip.value !== null,
	cfg: cfg === undefined ? null : cfg.value,
	state: state === undefined ? null : state.value,
	label: label === undefined ? null : label.value,
	name: name === undefined ? null : name.value,
	usage: stats.usage,
}));
await session.close(BACKGROUND_CONTEXT);
`

// legacyV3InteropResult is the shape nodeLegacyV3UpgradeScript prints, and
// what this Go side's Storage is queried into for comparison.
type legacyV3InteropResult struct {
	Entries []struct {
		Type       string `json:"type"`
		CustomType string `json:"customType"`
		Seq        int64  `json:"seq"`
		HasParent  bool   `json:"hasParent"`
	} `json:"entries"`
	TipPresent bool                       `json:"tipPresent"`
	Cfg        *session.LaneConfiguration `json:"cfg"`
	State      *session.LaneState         `json:"state"`
	Label      *string                    `json:"label"`
	Name       *string                    `json:"name"`
	Usage      struct {
		Input       int `json:"input"`
		Output      int `json:"output"`
		CacheRead   int `json:"cacheRead"`
		CacheWrite  int `json:"cacheWrite"`
		TotalTokens int `json:"totalTokens"`
	} `json:"usage"`
}

func goLegacyV3InteropResult(t *testing.T, path string) legacyV3InteropResult {
	t.Helper()
	st, err := Open(path, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	var out legacyV3InteropResult
	for _, e := range st.ScanEntries(session.EntryScan{Order: "asc"}) {
		out.Entries = append(out.Entries, struct {
			Type       string `json:"type"`
			CustomType string `json:"customType"`
			Seq        int64  `json:"seq"`
			HasParent  bool   `json:"hasParent"`
		}{Type: string(e.Type), CustomType: e.CustomType, Seq: e.Seq, HasParent: e.ParentID != nil})
	}
	if tipRaw, _, ok := st.GetValue(session.NamespaceBranchTip, "main"); ok {
		tip, err := session.GetTypedValue[*string](tipRaw)
		if err != nil {
			t.Fatal(err)
		}
		out.TipPresent = tip != nil
	}
	if cfgRaw, _, ok := st.GetValue(session.NamespaceLaneConfig, "main"); ok {
		cfg, err := session.GetTypedValue[session.LaneConfiguration](cfgRaw)
		if err != nil {
			t.Fatal(err)
		}
		out.Cfg = &cfg
	}
	if stateRaw, _, ok := st.GetValue(session.NamespaceLaneState, "main"); ok {
		state, err := session.GetTypedValue[session.LaneState](stateRaw)
		if err != nil {
			t.Fatal(err)
		}
		out.State = &state
	}
	if len(out.Entries) >= 2 {
		e2ID := entryIDAtSeq(st, 2)
		if labelRaw, _, ok := st.GetValue(session.NamespaceEntryLabel, e2ID); ok {
			label, err := session.GetTypedValue[string](labelRaw)
			if err != nil {
				t.Fatal(err)
			}
			out.Label = &label
		}
	}
	if nameRaw, _, ok := st.GetValue(session.NamespaceSessionName, ""); ok {
		name, err := session.GetTypedValue[string](nameRaw)
		if err != nil {
			t.Fatal(err)
		}
		out.Name = &name
	}
	stats := st.GetStats()
	out.Usage.Input = stats.Usage.Input
	out.Usage.Output = stats.Usage.Output
	out.Usage.CacheRead = stats.Usage.CacheRead
	out.Usage.CacheWrite = stats.Usage.CacheWrite
	out.Usage.TotalTokens = stats.Usage.TotalTokens
	return out
}

func entryIDAtSeq(st *Storage, seq int64) string {
	for _, e := range st.ScanEntries(session.EntryScan{Order: "asc"}) {
		if e.Seq == seq {
			return e.ID
		}
	}
	return ""
}

// TestInterop_NodeAndGoUpgradeTheSameLegacyV3Fixture runs pi's own
// JsonlSessionRepo.open (via Node, importing the real
// @earendil-works/pi-agent-core the TypeScript harness depends on) against
// a pristine copy of testdata/sessions/legacy-v3-fixture.jsonl, and this
// Go package's Open (which upgrades its own pristine copy in place) against
// another, then diffs the two structurally: entry kinds/seq/parent-presence
// in file order, the derived pi.lane.config/pi.lane.state/pi.session.name/
// pi.entry.label values, whether a branch tip was written at all, and the
// imported usage totals. It does not — cannot — compare literal entry ids
// or the tip's literal value: both sides mint fresh uuidv7 ids for retained
// entries (legacy-v3.js:239 vs. uuidv7At in this package), so an id match
// would only prove the same fixture's timestamps colreletad, not that the
// upgrade logic matches.
//
// Gated behind HARNESS_NODE_INTEROP=1 like test/e2e/interop_test.go,
// because it requires a Node checkout of harnessNodeRepo with node_modules
// installed (read-only; this package never writes into that tree).
func TestInterop_NodeAndGoUpgradeTheSameLegacyV3Fixture(t *testing.T) {
	if os.Getenv("HARNESS_NODE_INTEROP") != "1" {
		t.Skip("set HARNESS_NODE_INTEROP=1 to run (requires a Node checkout with node_modules at " + harnessNodeRepo + ")")
	}
	if _, err := os.Stat(harnessNodeRepo); err != nil {
		t.Fatalf("Node interop checkout not found at %s: %v", harnessNodeRepo, err)
	}

	const (
		fixtureCwd       = "/tmp/legacy-project"
		fixtureID        = "legacy-fixture-1"
		fixtureCreatedAt = "1704067200000"
	)

	nodePath := filepath.Join(t.TempDir(), "legacy.jsonl")
	copyFile(t, legacyV3Fixture, nodePath)

	cmd := exec.Command("node", "--experimental-strip-types", "-e", nodeLegacyV3UpgradeScript)
	cmd.Dir = harnessNodeRepo
	cmd.Env = append(os.Environ(),
		"HARNESS_INTEROP_FIXTURE="+nodePath,
		"HARNESS_INTEROP_CWD="+fixtureCwd,
		"HARNESS_INTEROP_ID="+fixtureID,
		"HARNESS_INTEROP_CREATED_AT="+fixtureCreatedAt,
	)
	out, err := cmd.CombinedOutput()
	t.Logf("node interop output:\n%s", out)
	if err != nil {
		t.Fatalf("node interop script failed: %v\n%s", err, out)
	}

	var nodeResult legacyV3InteropResult
	if err := json.Unmarshal(lastLine(out), &nodeResult); err != nil {
		t.Fatalf("parse node interop output: %v\n%s", err, out)
	}

	goPath := filepath.Join(t.TempDir(), "legacy.jsonl")
	copyFile(t, legacyV3Fixture, goPath)
	goResult := goLegacyV3InteropResult(t, goPath)

	if len(nodeResult.Entries) != len(goResult.Entries) {
		t.Fatalf("entry count: node=%d go=%d", len(nodeResult.Entries), len(goResult.Entries))
	}
	for i := range nodeResult.Entries {
		n, g := nodeResult.Entries[i], goResult.Entries[i]
		if n.Type != g.Type || n.CustomType != g.CustomType || n.Seq != g.Seq || n.HasParent != g.HasParent {
			t.Fatalf("entries[%d]: node=%+v go=%+v", i, n, g)
		}
	}
	if nodeResult.TipPresent != goResult.TipPresent {
		t.Fatalf("tipPresent: node=%v go=%v", nodeResult.TipPresent, goResult.TipPresent)
	}
	if (nodeResult.Cfg == nil) != (goResult.Cfg == nil) {
		t.Fatalf("cfg presence: node=%v go=%v", nodeResult.Cfg, goResult.Cfg)
	}
	if nodeResult.Cfg != nil && !equalLaneConfig(*nodeResult.Cfg, *goResult.Cfg) {
		t.Fatalf("cfg: node=%+v go=%+v", *nodeResult.Cfg, *goResult.Cfg)
	}
	if (nodeResult.State == nil) != (goResult.State == nil) {
		t.Fatalf("state presence: node=%v go=%v", nodeResult.State, goResult.State)
	}
	if (nodeResult.Label == nil) != (goResult.Label == nil) || (nodeResult.Label != nil && *nodeResult.Label != *goResult.Label) {
		t.Fatalf("label: node=%v go=%v", derefStr(nodeResult.Label), derefStr(goResult.Label))
	}
	if (nodeResult.Name == nil) != (goResult.Name == nil) || (nodeResult.Name != nil && *nodeResult.Name != *goResult.Name) {
		t.Fatalf("name: node=%v go=%v", derefStr(nodeResult.Name), derefStr(goResult.Name))
	}
	if nodeResult.Usage != goResult.Usage {
		t.Fatalf("usage: node=%+v go=%+v", nodeResult.Usage, goResult.Usage)
	}
}

func equalLaneConfig(a, b session.LaneConfiguration) bool {
	if a.Model != b.Model || a.ThinkingLevel != b.ThinkingLevel || len(a.ActiveToolNames) != len(b.ActiveToolNames) {
		return false
	}
	for i := range a.ActiveToolNames {
		if a.ActiveToolNames[i] != b.ActiveToolNames[i] {
			return false
		}
	}
	return true
}

func derefStr(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// lastLine returns the final non-empty line of out (nodeLegacyV3UpgradeScript
// prints exactly one JSON line, but --experimental-strip-types may emit a
// warning line first on some Node builds).
func lastLine(out []byte) []byte {
	lines := splitTerminatedLinesOrTail(out)
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		if len(l) > 0 && l[0] == '{' {
			return l
		}
	}
	if len(lines) > 0 {
		return lines[len(lines)-1]
	}
	return out
}

func splitTerminatedLinesOrTail(data []byte) [][]byte {
	if lines := splitTerminatedLines(data); lines != nil {
		return lines
	}
	return [][]byte{data}
}

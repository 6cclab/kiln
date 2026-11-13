package mods

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Tier 7: $.process.spawn, $.mcp.connect, $.plugin, plugin.register and
// engine.create. Every child a test starts is a script written into the
// test's own temp dir and run with that dir as cwd.

// gateRecorder is a Services.GateProcess that records what it was asked and
// answers deny ("" allows).
type gateRecorder struct {
	mu    sync.Mutex
	deny  string
	calls []gateCall
}

type gateCall struct {
	Plugin string
	Argv   []string
	Cwd    string
	Env    map[string]string
}

func (g *gateRecorder) gate(_ context.Context, plugin string, argv []string, cwd string, env map[string]string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, gateCall{plugin, argv, cwd, env})
	return g.deny, nil
}

func (g *gateRecorder) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

// script writes an executable shell script into dir and returns its path.
func script(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func spawnReq(argv []string, extra map[string]any) string {
	m := map[string]any{"argv": argv}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return string(b)
}

type spawnOut struct {
	Ok  map[string]any `json:"ok"`
	Err string         `json:"err"`
}

func runSpawn(t *testing.T, h *harness, command, req string) spawnOut {
	t.Helper()
	var out spawnOut
	if err := json.Unmarshal([]byte(runCommand(t, h, command, req)), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestProcessSpawnStreamsAndEnds(t *testing.T) {
	dir := t.TempDir()
	g := &gateRecorder{}
	h := newHarness(t, Options{Cwd: dir, Services: Services{GateProcess: g.gate}})
	h.load(t, "spawner")
	h.e.SessionStart(ctxT(t))
	sh := script(t, dir, "run.sh", `echo "$FOO"; cat; echo oops >&2; exit 3`)
	out := runSpawn(t, h, "spawn", spawnReq([]string{sh}, map[string]any{"env": map[string]string{"FOO": "bar"}, "input": "in"}))
	if out.Err != "" {
		t.Fatalf("spawn failed: %s\n%s", out.Err, h.logs)
	}
	if out.Ok["stdout"] != "bar\nin" || out.Ok["stderr"] != "oops\n" {
		t.Errorf("streams = %q / %q, want %q / %q", out.Ok["stdout"], out.Ok["stderr"], "bar\nin", "oops\n")
	}
	if want := map[string]any{"code": float64(3), "signal": nil}; !reflect.DeepEqual(out.Ok["result"], want) {
		t.Errorf("result = %v, want %v", out.Ok["result"], want)
	}
	if g.count() != 1 {
		t.Fatalf("gate asked %d times, want 1", g.count())
	}
	c := g.calls[0]
	if c.Plugin != "spawner" || !reflect.DeepEqual(c.Argv, []string{sh}) || c.Cwd != dir || c.Env["FOO"] != "bar" {
		t.Errorf("gate saw %+v, want plugin spawner, argv [%s], cwd %s, env FOO=bar", c, sh, dir)
	}
}

func TestProcessSpawnSignalEndsWithSignalName(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, Options{Cwd: dir, Services: Services{GateProcess: (&gateRecorder{}).gate}})
	h.load(t, "spawner")
	h.e.SessionStart(ctxT(t))
	sh := script(t, dir, "sig.sh", `kill -TERM $$; sleep 5`)
	out := runSpawn(t, h, "spawn", spawnReq([]string{sh}, nil))
	if want := map[string]any{"code": nil, "signal": "SIGTERM"}; !reflect.DeepEqual(out.Ok["result"], want) {
		t.Errorf("result = %v (err %q), want %v", out.Ok["result"], out.Err, want)
	}
}

// A refusal by the gate rejects the stream, and the program never starts.
func TestProcessSpawnRefusedByGateRunsNothing(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	g := &gateRecorder{deny: "Bash(*) is denied"}
	h := newHarness(t, Options{Cwd: dir, Services: Services{GateProcess: g.gate}})
	h.load(t, "spawner")
	h.e.SessionStart(ctxT(t))
	sh := script(t, dir, "mark.sh", `echo x > `+marker)
	out := runSpawn(t, h, "spawn", spawnReq([]string{sh}, nil))
	if !strings.Contains(out.Err, "refused") || !strings.Contains(out.Err, "Bash(*) is denied") {
		t.Errorf("err = %q, want the refusal with the gate's reason", out.Err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("the refused program ran")
	}
}

// With no gate wired kiln refuses rather than run the process ungated.
func TestProcessSpawnWithoutGateRunsNothing(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	h := newHarness(t, Options{Cwd: dir})
	h.load(t, "spawner")
	h.e.SessionStart(ctxT(t))
	sh := script(t, dir, "mark.sh", `echo x > `+marker)
	out := runSpawn(t, h, "spawn", spawnReq([]string{sh}, nil))
	if !strings.Contains(out.Err, "permission gate") {
		t.Errorf("err = %q, want a refusal naming the missing permission gate", out.Err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("the program ran with no gate")
	}
}

func TestProcessSpawnCannotStartRejectsFirstPull(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, Options{Cwd: dir, Services: Services{GateProcess: (&gateRecorder{}).gate}})
	h.load(t, "spawner")
	h.e.SessionStart(ctxT(t))
	if out := runSpawn(t, h, "spawn", spawnReq([]string{filepath.Join(dir, "nope")}, nil)); out.Err == "" {
		t.Error("a program that does not exist resolved; want its first pull rejected")
	}
	if out := runSpawn(t, h, "spawn", spawnReq(nil, nil)); !strings.Contains(out.Err, "argv") {
		t.Errorf("empty argv: err = %q, want one naming argv", out.Err)
	}
}

func pidFrom(t *testing.T, file string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(file); err == nil && strings.HasSuffix(string(b), "\n") {
			var pid int
			if _, err := fmt.Sscan(strings.TrimSpace(string(b)), &pid); err == nil {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no pid in %s", file)
	return 0
}

func waitDead(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("process %d still runs", pid)
}

func TestProcessSpawnLeavingTheLoopKillsTheChild(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, Options{Cwd: dir, Services: Services{GateProcess: (&gateRecorder{}).gate}})
	h.load(t, "spawner")
	h.e.SessionStart(ctxT(t))
	pidFile := filepath.Join(dir, "pid")
	sh := script(t, dir, "long.sh", `echo $$ > `+pidFile+`; echo started; exec sleep 60`)
	out := runSpawn(t, h, "spawnleft", spawnReq([]string{sh}, nil))
	if out.Ok["left"] != true || out.Ok["stdout"] != "started\n" {
		t.Fatalf("loop = %v, want one piece then left", out.Ok)
	}
	waitDead(t, pidFrom(t, pidFile))
}

func TestProcessSpawnClosingTheEngineKillsTheChild(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, Options{Cwd: dir, Services: Services{GateProcess: (&gateRecorder{}).gate}})
	h.load(t, "spawner")
	h.e.SessionStart(ctxT(t))
	pidFile := filepath.Join(dir, "pid")
	sh := script(t, dir, "long.sh", `echo $$ > `+pidFile+`; echo started; exec sleep 60`)
	runSpawn(t, h, "spawnbg", spawnReq([]string{sh}, nil))
	pid := pidFrom(t, pidFile)
	if !pidAlive(pid) {
		t.Fatal("the child ended with the hook; it should live on")
	}
	h.e.Close()
	waitDead(t, pid)
}

func TestMcpConnectReportsKilnsAnswer(t *testing.T) {
	var asked [][2]string
	h := newHarness(t, Options{Services: Services{McpConnect: func(_ context.Context, plugin, server string) McpConnectAnswer {
		asked = append(asked, [2]string{plugin, server})
		if server == "browser" {
			return McpConnectAnswer{Connected: true, Server: "plugin_spawner_browser"}
		}
		return McpConnectAnswer{Reason: "unlisted", Message: "no such server"}
	}}})
	h.load(t, "spawner")
	h.e.SessionStart(ctxT(t))
	got := runSpawn(t, h, "connect", "browser")
	if want := map[string]any{"isConnected": true, "server": "plugin_spawner_browser"}; !reflect.DeepEqual(got.Ok, want) {
		t.Errorf("connect browser = %v (err %q), want %v", got.Ok, got.Err, want)
	}
	got = runSpawn(t, h, "connect", "ghost")
	if want := map[string]any{"isConnected": false, "reason": "unlisted", "message": "no such server"}; !reflect.DeepEqual(got.Ok, want) {
		t.Errorf("connect ghost = %v, want %v", got.Ok, want)
	}
	if !reflect.DeepEqual(asked, [][2]string{{"spawner", "browser"}, {"spawner", "ghost"}}) {
		t.Errorf("service asked %v, want the calling plugin and each server", asked)
	}
}

func TestMcpConnectWithoutServiceRejects(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "spawner")
	h.e.SessionStart(ctxT(t))
	if got := runSpawn(t, h, "connect", "browser"); !strings.Contains(got.Err, "mcp.connect") {
		t.Errorf("err = %q, want a rejection naming mcp.connect", got.Err)
	}
}

func TestPluginNameAndRootAreProperties(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "spawner")
	h.e.SessionStart(ctxT(t))
	var got map[string]string
	if err := json.Unmarshal([]byte(runCommand(t, h, "plug", "")), &got); err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.Abs(filepath.Join("testdata", "mods", "spawner"))
	if got["name"] != "spawner" || got["root"] != root {
		t.Errorf("$.plugin = %v, want name spawner and root %s", got, root)
	}
}

func TestPluginRegisterJudgesEachModuleBeforeItJoins(t *testing.T) {
	h := newHarness(t, Options{})
	mods := []Module{fixture(t, "judge"), fixture(t, "spawner"), fixture(t, "victim")}
	mods[1].Version, mods[1].Provenance = "1.2.3", "spawner@market"
	errs := h.e.Load(mods)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "managed only") || !strings.Contains(errs[0].Error(), "victim") {
		t.Fatalf("load errors = %v, want one: victim refused with the judge's reason", errs)
	}
	h.e.SessionStart(ctxT(t))
	for _, c := range h.e.Commands() {
		if c.Name == "victim-ran" {
			t.Error("the refused module's session.start hook ran")
		}
	}
	var seen []map[string]any
	if err := json.Unmarshal([]byte(runCommand(t, h, "dump", "")), &seen); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0]["name"] != "spawner" || seen[1]["name"] != "victim" {
		t.Fatalf("judge saw %v, want spawner then victim (modules admitted after it)", seen)
	}
	sp := seen[0]
	if sp["tier"] != "user" || sp["version"] != "1.2.3" || sp["provenance"] != "spawner@market" || sp["root"] == "" {
		t.Errorf("spawner input = %v", sp)
	}
	uses := sp["uses"].(map[string]any)
	calls := uses["calls"].([]any)
	if !anyHas(calls, "process.spawn") || !anyHas(calls, "mcp.connect") || !anyHas(uses["events"].([]any), "command.run") {
		t.Errorf("spawner uses = %v, want its $ calls and event patterns as scanned", uses)
	}
	vuses := seen[1]["uses"].(map[string]any)
	if env, _ := vuses["env"].(map[string]any); !anyHas(env["reads"].([]any), "HOME") {
		t.Errorf("victim uses = %v, want env read HOME", vuses)
	}
	if seen[1]["version"] != nil {
		t.Errorf("victim version = %v, want none (the manifest states none)", seen[1]["version"])
	}
}

func anyHas(l []any, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

func TestEngineCreateStepAddsANoun(t *testing.T) {
	h := newHarness(t, Options{})
	h.load(t, "folder", "spawner")
	h.e.SessionStart(ctxT(t))
	if got := runCommand(t, h, "say", "hi"); got != "said hi" {
		t.Errorf("$.voice.say = %q, want %q", got, "said hi")
	}
	var facts map[string]any
	if err := json.Unmarshal([]byte(runCommand(t, h, "facts", "")), &facts); err != nil {
		t.Fatal(err)
	}
	if facts["keysOfDollar"] != float64(0) || facts["builtHasUi"] != true ||
		!reflect.DeepEqual(facts["plugins"], []any{"folder", "spawner"}) {
		t.Errorf("fold facts = %v, want an empty $, core's table from next(e), and both plugins", facts)
	}
}

func TestEngineCreateStepReplacingANounUnloads(t *testing.T) {
	h := newHarness(t, Options{})
	errs := h.e.Load([]Module{fixture(t, "replacer"), fixture(t, "folder")})
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "replacer") || !strings.Contains(errs[0].Error(), "$.ui") {
		t.Fatalf("load errors = %v, want the replacer unloaded for replacing $.ui", errs)
	}
	h.e.SessionStart(ctxT(t))
	for _, c := range h.e.Commands() {
		if c.Name == "replacer-ran" {
			t.Error("the unloaded plugin's session.start hook ran")
		}
	}
	if got := runCommand(t, h, "say", "ok"); got != "said ok" {
		t.Errorf("the other plugin's noun is gone: %q", got)
	}
}

package eval

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andrepato/harness/internal/auth"
	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/crash"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/provider/builtin"
	"github.com/andrepato/harness/internal/provider/ollama"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/session/jsonl"
	"github.com/andrepato/harness/internal/testkit/faux"
	"github.com/andrepato/harness/internal/testkit/scenario"
)

// defaultFauxTimeout/defaultLiveTimeout are used when a scenario declares
// no caps.timeout of its own.
const (
	defaultFauxTimeout = 120 * time.Second
	defaultLiveTimeout = 600 * time.Second
)

// ModelConfig is one model the runner drives a scenario against, parsed
// from a "provider/model" string.
type ModelConfig struct {
	Provider string
	Model    string
	// Faux is true when Provider == "faux": the runner starts a scripted
	// faux server for this model rather than requiring live credentials.
	Faux bool
}

// Ref renders a ModelConfig back as "provider/model".
func (m ModelConfig) Ref() string { return m.Provider + "/" + m.Model }

// ParseModelConfig parses "provider/model" into a ModelConfig.
func ParseModelConfig(s string) (ModelConfig, error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ModelConfig{}, fmt.Errorf("eval: invalid model %q, want provider/model", s)
	}
	return ModelConfig{Provider: parts[0], Model: parts[1], Faux: parts[0] == "faux"}, nil
}

// RunOptions configures Run.
type RunOptions struct {
	KilnBin      string
	ScenariosDir string
	FixturesDir  string
	ResultsDir   string

	Models []ModelConfig
	Roles  map[string]string

	Repeat   int
	Parallel int
	Keep     bool

	// Judge is the "provider/model" reference used to grade a live run
	// against a scenario's judge rubric. Empty means live runs are not
	// judged (only checked). Faux runs always use the scenario's
	// faux_verdict regardless of this field.
	Judge string

	// Filter, when non-empty, keeps only scenarios whose Name equals (or
	// contains) one of these strings, or whose Tags contain one exactly.
	Filter []string
}

// RunSummary is what Run hands back once every job has finished.
type RunSummary struct {
	ResultsFile string
	KilnVersion string
	Records     []Record
	Failed      int
}

// job is one (scenario, model, repeat) unit of work.
type job struct {
	sc     *scenario.Scenario
	mc     ModelConfig
	repeat int
}

// Run drives every (scenario x model x repeat) combination through the
// kiln binary, grades it, and writes the results to a new JSONL file under
// opts.ResultsDir.
func Run(ctx context.Context, opts RunOptions) (RunSummary, error) {
	if err := validateModels(opts.Models); err != nil {
		return RunSummary{}, err
	}
	if opts.KilnBin == "" {
		return RunSummary{}, fmt.Errorf("eval: KilnBin is required")
	}
	// Resolved to an absolute path up front: jobs exec it with cmd.Dir set
	// to each run's scratch project directory, and a relative path is
	// resolved against that Dir, not this process's cwd.
	if abs, err := filepath.Abs(opts.KilnBin); err == nil {
		opts.KilnBin = abs
	}
	version, err := KilnVersion(opts.KilnBin)
	if err != nil {
		return RunSummary{}, fmt.Errorf("eval: checking kiln binary: %w", err)
	}

	scenarios, err := scenario.List(opts.ScenariosDir)
	if err != nil {
		return RunSummary{}, err
	}
	scenarios = filterScenarios(scenarios, opts.Filter)
	if len(scenarios) == 0 {
		return RunSummary{}, fmt.Errorf("eval: no scenarios matched under %s", opts.ScenariosDir)
	}

	repeat := opts.Repeat
	if repeat < 1 {
		repeat = 1
	}
	parallel := opts.Parallel
	if parallel < 1 {
		parallel = 1
	}

	var jobs []job
	for _, sc := range scenarios {
		models := opts.Models
		if mc, ok := pinnedModel(sc); ok {
			models = []ModelConfig{mc}
		}
		for _, mc := range models {
			for r := 1; r <= repeat; r++ {
				jobs = append(jobs, job{sc: sc, mc: mc, repeat: r})
			}
		}
	}

	batchID, err := newRunID()
	if err != nil {
		return RunSummary{}, err
	}
	resultsFile := filepath.Join(opts.ResultsDir, batchID+".jsonl")

	rt := &runtime{
		opts:    opts,
		version: version,
		batchID: batchID,
		liveReg: newLazyRegistry(),
	}

	jobCh := make(chan job)
	recCh := make(chan Record)
	var wg sync.WaitGroup
	for w := 0; w < parallel; w++ {
		wg.Add(1)
		idx := w
		crash.Go(func() {
			defer wg.Done()
			rt.worker(ctx, idx, jobCh, recCh)
		})
	}
	crash.Go(func() {
		for _, j := range jobs {
			jobCh <- j
		}
		close(jobCh)
	})
	crash.Go(func() {
		wg.Wait()
		close(recCh)
	})

	var records []Record
	failed := 0
	for r := range recCh {
		records = append(records, r)
		if !r.OK {
			failed++
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Scenario != records[j].Scenario {
			return records[i].Scenario < records[j].Scenario
		}
		if records[i].Model != records[j].Model {
			return records[i].Model < records[j].Model
		}
		return records[i].Repeat < records[j].Repeat
	})

	if err := WriteRecords(resultsFile, records); err != nil {
		return RunSummary{}, err
	}

	return RunSummary{ResultsFile: resultsFile, KilnVersion: version, Records: records, Failed: failed}, nil
}

func validateModels(models []ModelConfig) error {
	if len(models) == 0 {
		return fmt.Errorf("eval: no models given")
	}
	if os.Getenv("KILN_EVAL_LIVE") == "1" {
		return nil
	}
	var refused []string
	for _, m := range models {
		if !m.Faux {
			refused = append(refused, m.Ref())
		}
	}
	if len(refused) > 0 {
		return fmt.Errorf("eval: live model(s) refused (set KILN_EVAL_LIVE=1 to allow): %s", strings.Join(refused, ", "))
	}
	return nil
}

// pinnedModel reads a scenario's model pin from a "pin-model:provider/model"
// tag (scenario.Scenario has no dedicated field for this, and this phase
// does not touch internal/testkit/scenario, which is shared with the
// concurrent test/e2e work). A scenario carrying this tag always runs
// against that one model, ignoring the run's --models matrix — used by
// scenarios that only make sense on a specific model (e.g.
// long-context-compaction, which needs faux-2's small window).
func pinnedModel(sc *scenario.Scenario) (ModelConfig, bool) {
	const prefix = "pin-model:"
	for _, tag := range sc.Tags {
		if strings.HasPrefix(tag, prefix) {
			mc, err := ParseModelConfig(strings.TrimPrefix(tag, prefix))
			if err == nil {
				return mc, true
			}
		}
	}
	return ModelConfig{}, false
}

func filterScenarios(scenarios []*scenario.Scenario, filter []string) []*scenario.Scenario {
	if len(filter) == 0 {
		return scenarios
	}
	var out []*scenario.Scenario
	for _, sc := range scenarios {
		if matchesFilter(sc, filter) {
			out = append(out, sc)
		}
	}
	return out
}

func matchesFilter(sc *scenario.Scenario, filter []string) bool {
	for _, f := range filter {
		if f == sc.Name || strings.Contains(sc.Name, f) {
			return true
		}
		for _, tag := range sc.Tags {
			if tag == f {
				return true
			}
		}
	}
	return false
}

func newRunID() (string, error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("eval: generating run id: %w", err)
	}
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(buf), nil
}

// runtime holds everything shared read-only (or safely concurrent) across
// workers for one Run call.
type runtime struct {
	opts    RunOptions
	version string
	batchID string
	liveReg *lazyRegistry

	mcpBinOnce sync.Once
	mcpBinPath string
	mcpBinErr  error
}

// wantsMCPFixture reports whether a scenario needs cmd/mcpfixture wired in
// as an MCP server named "fixture" (a "mcp-fixture" tag, since
// scenario.Scenario carries no dedicated field for this and this phase does
// not touch internal/testkit/scenario).
func wantsMCPFixture(sc *scenario.Scenario) bool {
	for _, tag := range sc.Tags {
		if tag == "mcp-fixture" {
			return true
		}
	}
	return false
}

// mcpFixtureBin builds cmd/mcpfixture once per Run call and caches its
// path, since several scenarios (or repeats) may need it.
func (rt *runtime) mcpFixtureBin() (string, error) {
	rt.mcpBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "kiln-eval-mcpfixture-")
		if err != nil {
			rt.mcpBinErr = err
			return
		}
		bin := filepath.Join(dir, "mcpfixture")
		out, err := exec.Command("go", "build", "-o", bin, "github.com/andrepato/harness/cmd/mcpfixture").CombinedOutput()
		if err != nil {
			rt.mcpBinErr = fmt.Errorf("eval: building cmd/mcpfixture: %w: %s", err, out)
			return
		}
		rt.mcpBinPath = bin
	})
	return rt.mcpBinPath, rt.mcpBinErr
}

// writeMCPConfig writes a --mcp-config file wiring cmd/mcpfixture in as a
// server named "fixture" (its tools are addressed as mcp__fixture__<name>).
func writeMCPConfig(path, mcpfixtureBin string) error {
	cfg := fmt.Sprintf(`{"mcpServers":{"fixture":{"command":%q}}}`, mcpfixtureBin)
	return os.WriteFile(path, []byte(cfg), 0o600)
}

func (rt *runtime) worker(ctx context.Context, idx int, jobs <-chan job, out chan<- Record) {
	var fauxSrv *faux.Server
	var fauxAddr string
	defer func() {
		if fauxSrv != nil {
			_ = fauxSrv.Close()
		}
	}()

	workerRoot, err := os.MkdirTemp("", fmt.Sprintf("kiln-eval-w%d-", idx))
	if err != nil {
		return
	}
	defer os.RemoveAll(workerRoot)

	for j := range jobs {
		if j.mc.Faux {
			apiShape := fauxAPIShape(j.sc)
			scriptYAML, err := j.sc.FauxScript(apiShape)
			if err != nil {
				out <- rt.errorRecord(j, err)
				continue
			}
			if fauxSrv == nil {
				fauxSrv, err = faux.New(faux.Options{ScriptYAML: scriptYAML})
				if err != nil {
					out <- rt.errorRecord(j, err)
					continue
				}
				fauxAddr, err = fauxSrv.Start()
				if err != nil {
					out <- rt.errorRecord(j, err)
					continue
				}
			} else if err := fauxSrv.LoadScriptYAML(scriptYAML); err != nil {
				out <- rt.errorRecord(j, err)
				continue
			}
			fauxSrv.Reset()
		}

		rec := rt.runJob(ctx, workerRoot, j, fauxSrv, fauxAddr)
		out <- rec
	}
}

func fauxAPIShape(sc *scenario.Scenario) string {
	if len(sc.Faux) == 1 {
		for k := range sc.Faux {
			return k
		}
	}
	return "anthropic-messages"
}

func (rt *runtime) errorRecord(j job, err error) Record {
	rec := rt.baseRecord(j)
	rec.OK = false
	rec.Reason = err.Error()
	rec.Score = 0
	return rec
}

func (rt *runtime) baseRecord(j job) Record {
	return Record{
		RunID:       rt.batchID,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		KilnVersion: rt.version,
		Scenario:    j.sc.Name,
		Tags:        j.sc.Tags,
		Provider:    j.mc.Provider,
		Model:       j.mc.Model,
		Roles:       rt.opts.Roles,
		Repeat:      j.repeat,
	}
}

// runJob executes one scenario x model x repeat combination end to end:
// scratch setup, exec, artifact collection, checks, judge, scoring.
func (rt *runtime) runJob(ctx context.Context, workerRoot string, j job, fauxSrv *faux.Server, fauxAddr string) Record {
	rec := rt.baseRecord(j)

	root, err := os.MkdirTemp(workerRoot, "run-")
	if err != nil {
		rec.OK, rec.Reason = false, err.Error()
		return rec
	}
	if !rt.opts.Keep {
		defer os.RemoveAll(root)
	}

	home := filepath.Join(root, "home")
	sessDir := filepath.Join(root, "sessions")
	logDir := filepath.Join(root, "logs")
	projectDir := filepath.Join(root, "project")
	for _, d := range []string{home, sessDir, logDir, projectDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			rec.OK, rec.Reason = false, err.Error()
			return rec
		}
	}

	fixtureDir := filepath.Join(root, "fixture-missing")
	if j.sc.Fixture != "" {
		fixtureDir = filepath.Join(rt.opts.FixturesDir, j.sc.Fixture)
		if err := scenario.CopyFixture(fixtureDir, projectDir); err != nil {
			rec.OK, rec.Reason = false, err.Error()
			return rec
		}
	}

	settings := mergeRoles(j.sc.Settings, rt.opts.Roles)
	if err := scenario.WriteSettings(projectDir, settings); err != nil {
		rec.OK, rec.Reason = false, err.Error()
		return rec
	}

	// caps.timeout is the faux cap; a live run gets caps.live_timeout or
	// the live default, never the faux number (a 60s faux cap killed every
	// real-model run longer than a minute on the first live run).
	timeout := j.sc.Caps.Timeout
	if !j.mc.Faux {
		timeout = j.sc.Caps.LiveTimeout
	}
	if timeout == 0 {
		if j.mc.Faux {
			timeout = defaultFauxTimeout
		} else {
			timeout = defaultLiveTimeout
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"-p", j.sc.Prompt, "--output-format", "json"}
	if j.sc.PermissionMode != "" {
		args = append(args, "--permission-mode", j.sc.PermissionMode)
	}
	if len(j.sc.AllowedTools) > 0 {
		args = append(args, "--allowed-tools", strings.Join(j.sc.AllowedTools, ","))
	}
	args = append(args, "--model", j.mc.Ref())
	if j.sc.Caps.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(j.sc.Caps.MaxTurns))
	}
	if wantsMCPFixture(j.sc) {
		bin, err := rt.mcpFixtureBin()
		if err != nil {
			rec.OK, rec.Reason = false, err.Error()
			return rec
		}
		cfgPath := filepath.Join(root, "mcp.json")
		if err := writeMCPConfig(cfgPath, bin); err != nil {
			rec.OK, rec.Reason = false, err.Error()
			return rec
		}
		args = append(args, "--strict-mcp-config", "--mcp-config", cfgPath)
	}

	env := filterEnv(os.Environ())
	env = append(env, "HOME="+home, "HARNESS_SESSIONS_DIR="+sessDir, "HARNESS_LOG_DIR="+logDir,
		// The session scratchpad stays inside the job's own HOME.
		"KILN_TMPDIR="+filepath.Join(home, "tmp"))
	apiShape := fauxAPIShape(j.sc)
	if j.mc.Faux {
		env = append(env, "HARNESS_FAUX_ADDR="+fauxAddr, "HARNESS_FAUX_API="+apiShape)
	}

	cmd := exec.CommandContext(runCtx, rt.opts.KilnBin, args...)
	cmd.Dir = projectDir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start)

	exitCode := 0
	if runErr != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			// A timed-out run has no result JSON: record the cause and the
			// wall clock it consumed rather than a parse error and zero.
			rec.OK = false
			rec.ExitCode = -1
			rec.DurationMS = elapsed.Milliseconds()
			rec.Reason = fmt.Sprintf("timeout after %s", timeout)
			rec.Checks = []CheckResult{}
			rec.Score = 0
			rt.keepArtifacts(j, root, "", "", stdout.String())
			return rec
		}
		if ee, ok := runErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			rec.OK = false
			rec.ExitCode = -1
			rec.DurationMS = elapsed.Milliseconds()
			rec.Reason = fmt.Sprintf("exec: %v (stderr: %s)", runErr, truncate(stderr.String(), 400))
			rec.Checks = []CheckResult{}
			rec.Score = 0
			rt.keepArtifacts(j, root, "", "", stdout.String())
			return rec
		}
	}
	rec.ExitCode = exitCode

	var res ResultJSON
	resultParseErr := json.Unmarshal(stdout.Bytes(), &res)
	rec.OK = res.OK
	rec.DurationMS = res.DurationMS
	if rec.DurationMS == 0 {
		rec.DurationMS = elapsed.Milliseconds()
	}
	rec.Turns = res.NumTurns
	rec.Reason = res.Reason
	rec.CostUSD = res.TotalCostUSD
	rec.Usage.Input = res.Usage.Input
	rec.Usage.Output = res.Usage.Output
	rec.Usage.CacheRead = res.Usage.CacheRead
	rec.Usage.CacheWrite = res.Usage.CacheWrite

	names := map[string]bool{}
	var nameList []string
	for _, tc := range res.ToolCalls {
		if !names[tc.Name] {
			names[tc.Name] = true
			nameList = append(nameList, tc.Name)
		}
	}
	sort.Strings(nameList)
	rec.ToolCalls = ToolCallsSummary{Count: len(res.ToolCalls), Names: nameList}

	rec.Tier = tierName(rt, j.mc)

	art := Artifacts{ProjectDir: projectDir, FixtureDir: fixtureDir, ExitCode: exitCode, Result: res}

	sessionPath := findSessionFile(sessDir)
	if sessionPath != "" {
		if st, err := jsonl.Open(sessionPath, nil); err == nil {
			art.SessionStats = st.GetStats()
			entries := st.ScanEntries(session.EntryScan{Type: session.EntryCompaction})
			art.CompactionOccurred = len(entries) > 0
			st.Close()
		}
	}

	logPath := findLogFile(logDir)
	if logPath != "" {
		art.LogEvents = parseLogFile(logPath)
	}
	for _, ev := range art.LogEvents {
		if ev.Msg == "subagent model resolved" {
			rec.Subagents.Count++
			rec.Subagents.Models = appendUnique(rec.Subagents.Models, ev.KV["provider"]+"/"+ev.KV["model"])
		}
	}

	if j.mc.Faux && fauxSrv != nil {
		art.FauxRequests = fauxSrv.Requests()
	}

	rec.Checks = RunChecks(j.sc, art)

	if j.sc.Judge != nil {
		rec.Judge = rt.runJudge(runCtx, j, res, projectDir, fixtureDir)
	}

	rec.Score = ComputeScore(rec.Checks, rec.Judge)

	if resultParseErr != nil && rec.Reason == "" {
		rec.Reason = fmt.Sprintf("invalid json result: %v", resultParseErr)
	}

	rt.keepArtifacts(j, root, sessionPath, logPath, stdout.String())
	if rt.opts.Keep {
		rec.Artifacts = ArtifactPaths{
			Session: sessionPath,
			Log:     logPath,
		}
	}

	return rec
}

// simpleRubric adapts a plain string to the rubricScenario interface
// buildRubricPrompt expects.
type simpleRubric string

func (s simpleRubric) Rubric() string { return string(s) }

func (rt *runtime) runJudge(ctx context.Context, j job, res ResultJSON, projectDir, fixtureDir string) *JudgeResult {
	if j.mc.Faux {
		v, err := parseVerdict(j.sc.Judge.FauxVerdict)
		if err != nil {
			return nil
		}
		return &JudgeResult{Score: v.Score, Reasons: v.Reasons, Model: "faux-verdict"}
	}
	if rt.opts.Judge == "" {
		return nil
	}
	judgeCfg, err := ParseModelConfig(rt.opts.Judge)
	if err != nil {
		return &JudgeResult{Model: rt.opts.Judge, Error: err.Error()}
	}
	reg, err := rt.liveReg.get(ctx)
	if err != nil {
		return &JudgeResult{Model: rt.opts.Judge, Error: err.Error()}
	}
	// A dynamic provider (Ollama) lists no models until refreshed, and
	// Resolve would then fail for a model that exists.
	if p, ok := reg.Provider(judgeCfg.Provider); ok {
		if err := p.RefreshModels(ctx); err != nil {
			return &JudgeResult{Model: rt.opts.Judge, Error: "refresh: " + err.Error()}
		}
	}
	diff := TreeDiff(fixtureDir, projectDir)
	prompt := buildRubricPrompt(simpleRubric(j.sc.Judge.Rubric), JudgeInputs{
		Prompt:    j.sc.Prompt,
		FinalText: res.Text,
		Diff:      diff,
		ToolCalls: res.ToolCalls,
	})
	v, _, err := RunJudge(ctx, reg, judgeCfg.Provider, judgeCfg.Model, prompt)
	if err != nil {
		return &JudgeResult{Model: rt.opts.Judge, Error: err.Error()}
	}
	return &JudgeResult{Score: v.Score, Reasons: v.Reasons, Model: rt.opts.Judge}
}

func (rt *runtime) keepArtifacts(j job, root, sessionPath, logPath, stdout string) {
	if !rt.opts.Keep {
		return
	}
	dest := filepath.Join(rt.opts.ResultsDir, "runs", rt.batchID, j.sc.Name, j.mc.Provider+"-"+j.mc.Model, strconv.Itoa(j.repeat))
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dest, "stdout.json"), []byte(stdout), 0o644)
	if sessionPath != "" {
		if data, err := os.ReadFile(sessionPath); err == nil {
			_ = os.WriteFile(filepath.Join(dest, "session.jsonl"), data, 0o644)
		}
	}
	if logPath != "" {
		if data, err := os.ReadFile(logPath); err == nil {
			_ = os.WriteFile(filepath.Join(dest, "run.log"), data, 0o644)
		}
	}
}

// mergeRoles copies scenario settings and sets "modelRoles" from roles,
// but only when the scenario did not already declare its own: a scenario
// that names a role (subagent-dispatch-with-role) knows exactly what that
// role must map to for its faux script to work, and must not have it
// silently replaced by whatever roles the evaluating machine's own
// ~/.claude/settings.json happens to carry.
func mergeRoles(settings map[string]any, roles map[string]string) map[string]any {
	out := map[string]any{}
	for k, v := range settings {
		out[k] = v
	}
	if _, declared := out["modelRoles"]; !declared && len(roles) > 0 {
		out["modelRoles"] = roles
	}
	return out
}

// filterEnv drops HOME/HARNESS_*/faux env vars from base so runJob can set
// its own isolated values without a stale ambient one winning (exec.Cmd
// keeps the first occurrence of a duplicate key on most platforms, so a
// stale entry must be removed rather than merely shadowed).
func filterEnv(base []string) []string {
	drop := map[string]bool{
		"HOME": true, "HARNESS_SESSIONS_DIR": true, "HARNESS_LOG_DIR": true,
		"HARNESS_FAUX_ADDR": true, "HARNESS_FAUX_API": true,
	}
	out := make([]string, 0, len(base))
	for _, kv := range base {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			out = append(out, kv)
			continue
		}
		if drop[kv[:i]] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func findSessionFile(sessDir string) string {
	matches, _ := filepath.Glob(filepath.Join(sessDir, "*", "*.jsonl"))
	if len(matches) == 1 {
		return matches[0]
	}
	return ""
}

func findLogFile(logDir string) string {
	matches, _ := filepath.Glob(filepath.Join(logDir, "harness-*.log"))
	if len(matches) == 1 {
		return matches[0]
	}
	if len(matches) > 1 {
		sort.Strings(matches)
		return matches[len(matches)-1]
	}
	return ""
}

func parseLogFile(path string) []LogEvent {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []LogEvent
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		if ev, ok := parseLogLine(line); ok {
			out = append(out, ev)
		}
	}
	return out
}

func parseLogLine(line string) (LogEvent, bool) {
	ev := LogEvent{KV: map[string]string{}}
	for _, tok := range splitLogTokens(line) {
		i := strings.IndexByte(tok, '=')
		if i < 0 {
			continue
		}
		k, v := tok[:i], tok[i+1:]
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			if uq, err := strconv.Unquote(v); err == nil {
				v = uq
			}
		}
		if k == "msg" {
			ev.Msg = v
			continue
		}
		ev.KV[k] = v
	}
	return ev, ev.Msg != ""
}

func splitLogTokens(line string) []string {
	var toks []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '"':
			inQuote = !inQuote
			cur.WriteByte(c)
		case c == ' ' && !inQuote:
			if cur.Len() > 0 {
				toks = append(toks, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		toks = append(toks, cur.String())
	}
	return toks
}

func appendUnique(list []string, v string) []string {
	if v == "" || v == "/" {
		return list
	}
	for _, existing := range list {
		if existing == v {
			return list
		}
	}
	return append(list, v)
}

// --- tiers -----------------------------------------------------------

// fauxContextWindows mirrors internal/provider/faux's model catalog
// (faux-1: 128k, faux-2: 32k), duplicated here rather than imported
// because building a faux.Provider requires HARNESS_FAUX_ADDR to already
// be set process-wide, which the runner deliberately avoids (each worker
// runs its own faux server at its own address).
var fauxContextWindows = map[string]int{"faux-1": 128000, "faux-2": 32768}

func tierName(rt *runtime, mc ModelConfig) string {
	if mc.Faux {
		cw, ok := fauxContextWindows[mc.Model]
		if !ok {
			return ""
		}
		return budget.TierFor(cw).Name
	}
	reg, err := rt.liveReg.get(context.Background())
	if err != nil {
		return ""
	}
	m, ok := reg.GetModel(mc.Provider, mc.Model)
	if !ok {
		return ""
	}
	return budget.TierFor(m.ContextWindow).Name
}

// lazyRegistry builds a live-provider registry (no faux) on first use and
// caches it, since building the provider catalog is not free and most
// eval runs (faux only) never need it.
type lazyRegistry struct {
	once sync.Once
	reg  *provider.Registry
	err  error
}

func newLazyRegistry() *lazyRegistry { return &lazyRegistry{} }

func (l *lazyRegistry) get(ctx context.Context) (*provider.Registry, error) {
	l.once.Do(func() {
		store := auth.NewFileCredentialStore("")
		reg := provider.NewRegistry(store)
		builtin.RegisterAll(reg, store)
		reg.Register(ollama.New(ollamaOptionsFromEnv()))
		l.reg = reg
	})
	return l.reg, l.err
}

func ollamaOptionsFromEnv() ollama.Options {
	url := os.Getenv("OLLAMA_HOST")
	if url == "" {
		url = os.Getenv("OLLAMA_BASE_URL")
	}
	var serverDefault int
	if v := os.Getenv("OLLAMA_CONTEXT_LENGTH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			serverDefault = n
		}
	}
	// No HTTPClient: ollama's default streaming client bounds connecting,
	// not the response. A 3-minute whole-request Timeout here cut off
	// compactions and turns on a slow local model mid-stream.
	return ollama.Options{URL: url, ServerDefaultContext: serverDefault}
}

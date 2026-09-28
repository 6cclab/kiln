// Package agent is the Go port of harness/src/agent/session.ts: the agent
// session (resident tools plus the harness loop), headless. See doc.go.
package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/session/jsonl"
	"github.com/andrepato/harness/internal/tool"
	"github.com/andrepato/harness/internal/tools"
)

// MainLane is the lane the interactive conversation runs on. Sub-agents
// would take their own, matching session.ts's MAIN_LANE.
const MainLane = "main"

// defaultSystemPrompt mirrors session.ts's DEFAULT_SYSTEM_PROMPT.
const defaultSystemPrompt = "You are a coding assistant operating in a terminal. " +
	"Use the provided tools to inspect and modify the user's code. " +
	"Prefer reading a file before editing it. Keep responses short."

// defaultSessionsDirEnv is the environment variable that overrides the
// session store root when Options.SessionsRoot is left empty, matching
// this repo's convention (session.ts has no such override; it always
// defaults to ~/.harness/sessions).
const defaultSessionsDirEnv = "HARNESS_SESSIONS_DIR"

// Options configures Start. It is the Go analogue of session.ts's
// StartSessionOptions.
type Options struct {
	Registry *provider.Registry
	// Resolved is the chosen model plus its Tier and reasoning Suppression,
	// as returned by provider.Registry.Resolve. Suppression is not used
	// here: unlike session.ts's toProviderMessages hook, this port applies
	// suppression inside each provider's own Stream call (see
	// internal/provider/ollama), computed fresh from the model rather than
	// carried from Resolve. See doc.go.
	Resolved provider.Resolved

	// Cwd is the working directory the tools operate in. Defaults to
	// os.Getwd().
	Cwd string
	// SessionsRoot is the session store root. Defaults to the
	// HARNESS_SESSIONS_DIR environment variable if set, else
	// ~/.harness/sessions (jsonl.NewRepo's own default).
	SessionsRoot string

	// Resume opens the session whose id equals Resume, or, failing an
	// exact match, the one session under Cwd whose id has Resume as a
	// prefix. Ambiguous prefixes (more than one match) are an error rather
	// than a silent guess; this is stricter than session.ts, which only
	// ever matched by exact id. See doc.go.
	Resume string
	// ResumeLatest opens the most recently modified session under Cwd.
	ResumeLatest bool
	// SessionID creates the session with this id (ignored when Resume or
	// ResumeLatest actually finds a session to open or fork).
	SessionID string
	// ParentSessionID, when set, is written into a freshly created
	// session's header (jsonl.CreateOptions.ParentSessionID) so a
	// subagent's own session file is distinguishable from a top-level one
	// (internal/cli/tui.go's buildRecentSessionRows filters the banner's
	// recent-sessions list on exactly this field). Left empty for the
	// top-level CLI session; dispatch.go sets it to the dispatching
	// session's own SessionID. Ignored when Resume/ResumeLatest actually
	// opens or forks an existing session — Fork already sets a forked
	// session's ParentSessionID to its *source* session's id
	// (jsonl/fork.go), which is a different relationship than "dispatched
	// by".
	ParentSessionID string
	// ForkSession branches the resumed session into a new file instead of
	// continuing in place. Only takes effect together with Resume or
	// ResumeLatest, matching session.ts's `fork` option.
	ForkSession bool

	// Name sets the session's display name.
	Name string
	// ThinkingLevel is the reasoning effort, from --effort: "low", "medium",
	// "high", "xhigh", "max", or "" (harness.New defaults that to "off").
	ThinkingLevel string

	// ExtraTools are registered alongside the four resident tools (bash,
	// read, edit, write) but, unless named in ActiveToolNames, stay
	// dormant: registration is free, activation costs schema tokens.
	ExtraTools []*tool.Tool
	// ActiveToolNames are the tools actually sent to the model. Defaults to
	// the resident tools' names (bash, read, edit, write) — ExtraTools are
	// registered but not activated unless named here explicitly.
	ActiveToolNames []string

	SystemPrompt string

	// Env is the filesystem/shell context the built-in tools run against.
	// Defaults to execenv.New(Cwd). Callers pass their own mainly in tests.
	Env *execenv.Env

	// Now, if set, replaces time.Now for session/entry timestamps (tests
	// only).
	Now func() time.Time
}

// Started is what Start returns: everything a caller needs to drive the
// conversation and to satisfy hooks that need the session's identity.
type Started struct {
	Harness *harness.Harness
	// Lane is the conversation lane: Prompt, Abort, Steer and
	// SetActiveTools live here, not on the Harness.
	Lane *harness.Lane
	// SessionID is this session's id, as hooks would receive it in
	// session_id.
	SessionID string
	// TranscriptPath is the absolute path of this session's JSONL log.
	TranscriptPath string
	Model          provider.Model
	Tier           budget.Tier
	// Created reports whether Start created a brand-new session file
	// (true) or opened/forked an existing one (false).
	Created bool

	// OnModelChanged, if set, is called by SetModel once the lane's model
	// and the harness's compaction settings have been switched. The TUI
	// hooks this to re-gate active tools against the new tier's strategy;
	// SetModel itself does not touch ActiveToolNames.
	OnModelChanged func(ctx context.Context, resolved provider.Resolved)
}

// Start resolves which session file to open (or creates one), builds the
// harness over it, and returns the ready main lane. It is the Go port of
// session.ts's startSession.
func Start(ctx context.Context, opts Options) (*Started, error) {
	cwd := opts.Cwd
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("agent: resolve cwd: %w", err)
		}
		cwd = wd
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}

	root := opts.SessionsRoot
	if root == "" {
		root = os.Getenv(defaultSessionsDirEnv)
	}
	repo, err := jsonl.NewRepo(root)
	if err != nil {
		return nil, fmt.Errorf("agent: open sessions root: %w", err)
	}
	repo.Now = now

	storage, meta, created, err := resolveSession(repo, opts, cwd, now)
	if err != nil {
		return nil, err
	}

	env := opts.Env
	if env == nil {
		env = execenv.New(cwd)
	}

	builtins := tools.Builtins(env)
	residentNames := builtins.Names()
	toolSet := builtins
	for _, t := range opts.ExtraTools {
		toolSet.Add(t)
	}
	activeToolNames := opts.ActiveToolNames
	if activeToolNames == nil {
		activeToolNames = residentNames
	}

	systemPrompt := opts.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = defaultSystemPrompt
	}

	model := opts.Resolved.Model
	tier := opts.Resolved.Tier

	h, err := harness.New(harness.Options{
		Storage:         storage,
		Registry:        opts.Registry,
		Model:           session.ModelRef{Provider: model.Provider, ModelID: model.ID},
		ThinkingLevel:   opts.ThinkingLevel,
		Tools:           toolSet,
		ActiveToolNames: activeToolNames,
		SystemPrompt:    systemPrompt,
		Compaction:      compactionFromTier(tier),
		// Every tool result is capped at the tier's share of the window
		// before it is sent; the tools' own fixed limits are only a first line.
		ToolOutputTokens: tier.ToolOutputTokens,
		Cwd:              cwd,
		Now:              now,
	})
	if err != nil {
		_ = storage.Close()
		return nil, fmt.Errorf("agent: build harness: %w", err)
	}

	if opts.Name != "" {
		if err := h.SetName(opts.Name); err != nil {
			return nil, fmt.Errorf("agent: set session name: %w", err)
		}
	}

	lane, err := h.Lane(MainLane)
	if err != nil {
		return nil, fmt.Errorf("agent: create main lane: %w", err)
	}
	// The level this run asks for (--effort, or Claude Code's effortLevel)
	// applies to a resumed session too: effort is a setting, not part of
	// the conversation. Sessions saved before the unset level stopped
	// meaning "off" would otherwise keep thinking pinned off.
	if opts.ThinkingLevel != "" {
		if err := lane.SetThinkingLevel(opts.ThinkingLevel); err != nil {
			return nil, fmt.Errorf("agent: set thinking level: %w", err)
		}
	}

	return &Started{
		Harness:        h,
		Lane:           lane,
		SessionID:      meta.ID,
		TranscriptPath: meta.Path,
		Model:          model,
		Tier:           tier,
		Created:        created,
	}, nil
}

// ResumeIncomplete completes an operation a previous process left running
// on started's lane — typically because that process crashed or was
// killed mid-tool-call — before the caller issues its own first turn.
// harness.Lane.Prompt always starts a brand-new operation and has no idea
// a prior one never reached a terminal state (finishOperation, turn.go,
// always clears pi.lane.state.currentOperationId on completion, abort or
// failure), so this has to run first: it detects that leftover state via
// harness.Lane.PendingOperation and, if found, drives it to completion via
// harness.Lane.Resume (which re-executes whichever tool calls in the
// interrupted batch never reached "outcome_ready", gets the model's
// follow-up, and finishes the operation like any other).
//
// Callers must invoke this only once the lane's tool set and hooks are
// fully wired (Resume re-runs tool calls, which needs both) — chat.go
// calls it from print mode after the hook registrations and from the TUI
// before program.Run(), both well after agent.Start and the hook wiring
// that follows it.
//
// notice, if non-nil, receives one message before the attempt and, on
// failure, one more naming the error; it is meant to be the same sink a
// caller already uses for hook activity (print mode's stderr notice, the
// TUI's transcript). A clean lane (PendingOperation reports ok=false) is a
// silent no-op — resumed reports false and err is nil — so this is safe to
// call unconditionally on every run, resumed or not.
func ResumeIncomplete(ctx context.Context, started *Started, notice func(string)) (resumed bool, err error) {
	if started == nil || started.Lane == nil {
		return false, nil
	}
	operationID, pending := started.Lane.PendingOperation()
	if !pending {
		return false, nil
	}
	if notice != nil {
		notice("resuming interrupted operation " + operationID)
	}
	if _, err := started.Lane.Resume(ctx); err != nil {
		if notice != nil {
			notice("failed to resume interrupted operation " + operationID + ": " + err.Error())
		}
		return false, err
	}
	return true, nil
}

// SetModel switches a started session's model, mirroring cli.ts's model
// switch: the lane's own model configuration and the harness's compaction
// settings both move to resolved's tier. Tool re-gating (which tools stay
// active under the new tier's strategy) is left to started.OnModelChanged,
// which SetModel calls last, if set.
func SetModel(ctx context.Context, started *Started, resolved provider.Resolved) error {
	if started == nil || started.Lane == nil || started.Harness == nil {
		return fmt.Errorf("agent: SetModel requires a started session")
	}
	modelRef := session.ModelRef{Provider: resolved.Model.Provider, ModelID: resolved.Model.ID}
	if err := started.Lane.SetModel(modelRef, ""); err != nil {
		return fmt.Errorf("agent: switch model: %w", err)
	}
	started.Harness.SetCompactionSettings(compactionFromTier(resolved.Tier))
	started.Model = resolved.Model
	started.Tier = resolved.Tier
	if started.OnModelChanged != nil {
		started.OnModelChanged(ctx, resolved)
	}
	return nil
}

// compactionFromTier projects a budget.Tier's compaction knobs onto
// harness.CompactionSettings (an alias for compaction.Settings). The two
// types are structurally identical; this is a field-by-field copy rather
// than a cast because they are distinct named types in distinct packages.
func compactionFromTier(tier budget.Tier) harness.CompactionSettings {
	return harness.CompactionSettings{
		Enabled:          tier.Compaction.Enabled,
		ReserveTokens:    tier.Compaction.ReserveTokens,
		KeepRecentTokens: tier.Compaction.KeepRecentTokens,
	}
}

// resolveSession picks which session file Start opens, mirroring
// startSession's resume/fork/create decision tree in session.ts:
//
//   - Resume or ResumeLatest set: list this cwd's sessions and find a
//     match (by Resume's id/prefix, or the most recently modified). A
//     match forks (ForkSession) or opens in place.
//   - No match (including neither Resume nor ResumeLatest requested):
//     create a fresh session, with SessionID if given.
func resolveSession(repo *jsonl.Repo, opts Options, cwd string, now func() time.Time) (*jsonl.Storage, jsonl.Metadata, bool, error) {
	if opts.Resume != "" || opts.ResumeLatest {
		candidates, err := repo.List(cwd)
		if err != nil {
			return nil, jsonl.Metadata{}, false, fmt.Errorf("agent: list sessions: %w", err)
		}
		match, err := selectResumeCandidate(candidates, opts.Resume, opts.ResumeLatest)
		if err != nil {
			return nil, jsonl.Metadata{}, false, err
		}
		if match != nil {
			if opts.ForkSession {
				storage, meta, err := forkSession(repo, *match, opts.SessionID, now)
				if err != nil {
					return nil, jsonl.Metadata{}, false, fmt.Errorf("agent: fork session: %w", err)
				}
				return storage, meta, false, nil
			}
			storage, meta, err := repo.Open(match.Path)
			if err != nil {
				return nil, jsonl.Metadata{}, false, fmt.Errorf("agent: open session %s: %w", match.ID, err)
			}
			return storage, meta, false, nil
		}
		// No match: fall through and create fresh, matching session.ts's
		// "a stale id from a deleted session does not block startup".
	}

	storage, meta, err := repo.Create(jsonl.CreateOptions{Cwd: cwd, ID: opts.SessionID, ParentSessionID: opts.ParentSessionID})
	if err != nil {
		return nil, jsonl.Metadata{}, false, fmt.Errorf("agent: create session: %w", err)
	}
	return storage, meta, true, nil
}

// selectResumeCandidate picks the session Resume/ResumeLatest names, or nil
// if none matches (the caller then creates fresh). resumeID, if non-empty,
// is tried as an exact id match first, then as a unique id prefix; two or
// more sessions sharing a prefix is an error rather than a silent pick.
func selectResumeCandidate(candidates []jsonl.Metadata, resumeID string, resumeLatest bool) (*jsonl.Metadata, error) {
	if resumeID != "" {
		for i := range candidates {
			if candidates[i].ID == resumeID {
				return &candidates[i], nil
			}
		}
		var prefixMatches []jsonl.Metadata
		for _, c := range candidates {
			if len(resumeID) > 0 && len(c.ID) >= len(resumeID) && c.ID[:len(resumeID)] == resumeID {
				prefixMatches = append(prefixMatches, c)
			}
		}
		switch len(prefixMatches) {
		case 0:
			return nil, nil
		case 1:
			return &prefixMatches[0], nil
		default:
			return nil, fmt.Errorf("agent: session id prefix %q is ambiguous: matches %d sessions", resumeID, len(prefixMatches))
		}
	}
	if !resumeLatest || len(candidates) == 0 {
		return nil, nil
	}
	best := &candidates[0]
	for i := 1; i < len(candidates); i++ {
		if candidates[i].ModifiedAt > best.ModifiedAt {
			best = &candidates[i]
		}
	}
	return best, nil
}

// forkSession copies source's whole tree into a new session file in the
// same directory, via jsonl.Fork, and opens the copy. The source is opened
// read-only to capture its header and NextSeq boundary, then closed before
// Fork re-reads the file, matching fork_test.go's own usage of the package
// function (Repo has no Fork method of its own; see doc.go).
func forkSession(repo *jsonl.Repo, source jsonl.Metadata, sessionID string, now func() time.Time) (*jsonl.Storage, jsonl.Metadata, error) {
	src, err := jsonl.Open(source.Path, nil)
	if err != nil {
		return nil, jsonl.Metadata{}, err
	}
	header := src.Header()
	nextSeq := src.NextSeq()
	if err := src.Close(); err != nil {
		return nil, jsonl.Metadata{}, err
	}

	createdAt := now().UnixMilli()
	id := sessionID
	if id == "" {
		id, err = newSessionID(createdAt)
		if err != nil {
			return nil, jsonl.Metadata{}, err
		}
	}
	destPath := filepath.Join(filepath.Dir(source.Path), jsonl.FileName(createdAt, id))

	if err := jsonl.Fork(source.Path, header, nextSeq, destPath, jsonl.ForkOptions{
		Scope: session.ForkScopeTree,
		ID:    id,
	}, func() int64 { return createdAt }); err != nil {
		return nil, jsonl.Metadata{}, err
	}

	return repo.Open(destPath)
}

// newSessionID returns a UUIDv7 string whose 48-bit timestamp field is
// createdAtMs. This duplicates jsonl's own (unexported) uuidv7At, needed
// here to fix the destination session's id before Fork runs, so the
// destination file's name (which embeds the id) and its header agree. See
// doc.go.
func newSessionID(createdAtMs int64) (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}
	b := [16]byte(id)
	t := uint64(createdAtMs) & 0xFFFFFFFFFFFF
	b[0] = byte(t >> 40)
	b[1] = byte(t >> 32)
	b[2] = byte(t >> 24)
	b[3] = byte(t >> 16)
	b[4] = byte(t >> 8)
	b[5] = byte(t)
	b[6] = 0x70 | (b[6] & 0x0F)
	b[8] = 0x80 | (b[8] & 0x3F)
	return uuid.UUID(b).String(), nil
}

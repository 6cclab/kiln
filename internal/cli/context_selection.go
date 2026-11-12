package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	slashcommands "github.com/andrepato/harness/internal/commands"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tools"
)

const maxContextSelections = 64

// contextSelectionLedger holds only decisions absent from committed content:
// transient suggestions and retrieved memory, exclusions, and search budget
// drops. Included tool content is derived from the current projected branch.
type contextSelectionLedger struct {
	mu          sync.Mutex
	excluded    map[string]slashcommands.ContextSelection
	transient   map[string]slashcommands.ContextSelection
	searchDrops map[string][]slashcommands.ContextSelection // committed tool-call ID
	dropOrder   []string
	requestUser string
}

func newContextSelectionLedger() *contextSelectionLedger {
	return &contextSelectionLedger{
		excluded:    map[string]slashcommands.ContextSelection{},
		transient:   map[string]slashcommands.ContextSelection{},
		searchDrops: map[string][]slashcommands.ContextSelection{},
	}
}

func selectionKey(kind, material, name string) string {
	return kind + "\x00" + material + "\x00" + name
}

func selection(kind, material, name, reason string, included bool) slashcommands.ContextSelection {
	return slashcommands.ContextSelection{Kind: kind, Material: material, Name: name, Reason: reason, Included: included}
}

func (l *contextSelectionLedger) Exclude(kind, name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := selection(kind, "metadata", name, "excluded", false)
	l.excluded[selectionKey(kind, e.Material, name)] = e
}

// ExcludeConfiguredSkills reports only names that survived loading and are
// model-invocable before the exclusion filter runs.
func (l *contextSelectionLedger) ExcludeConfiguredSkills(records []tools.SkillRecord, configured []string) {
	available := make(map[string]bool, len(records))
	for _, record := range records {
		available[record.Name] = true
	}
	for _, name := range configured {
		if available[name] {
			l.Exclude("skill", name)
		}
	}
}

// lastTypedUser identifies the newest typed request by a digest, so the same
// key can be stored in the session without keeping prompt text.
func lastTypedUser(transcript []msg.Message) string {
	for i := len(transcript) - 1; i >= 0; i-- {
		user, ok := transcript[i].(msg.UserMessage)
		if !ok || user.KilnHook != "" {
			continue
		}
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s", user.Timestamp, user.KilnTyped, msg.TextOf(user.Content))))
		return hex.EncodeToString(sum[:])
	}
	return ""
}

func (l *contextSelectionLedger) BeginRequest(transcript []msg.Message) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.transient = map[string]slashcommands.ContextSelection{}
	l.requestUser = lastTypedUser(transcript)
}

// Record is called only by context transforms that add this request's
// transient metadata or memory records after the byte budget is applied.
func (l *contextSelectionLedger) Record(kind, name, reason string, included bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	material := "metadata"
	if kind == "memory" {
		material = "record"
	}
	e := selection(kind, material, name, reason, included)
	l.transient[selectionKey(kind, material, name)] = e
}

// RecordSearch keeps bounded budget-drop evidence keyed to the tool-result ID.
// Search hits themselves can be recovered from the committed result body.
func (l *contextSelectionLedger) RecordSearch(callID, kind, name, reason string, included bool) {
	if reason != "budget-dropped" || callID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.searchDrops[callID]; !exists {
		l.dropOrder = append(l.dropOrder, callID)
		if len(l.dropOrder) > maxContextSelections {
			delete(l.searchDrops, l.dropOrder[0])
			l.dropOrder = l.dropOrder[1:]
		}
	}
	if len(l.searchDrops[callID]) < 10 {
		l.searchDrops[callID] = append(l.searchDrops[callID], selection(kind, "metadata", name, reason, false))
	}
}

var searchResultLine = regexp.MustCompile(`(?m)^- (.+) \[(skill|agent|mcp)\]:`)
var memoryPath = regexp.MustCompile(`<memory path=("(?:\\.|[^"])*")`)
var autoMemoryDir = regexp.MustCompile(`<auto-memory dir=("(?:\\.|[^"])*")`)

// SnapshotForRequest uses the same projected transcript and active system
// prompt as the next model request. Old tool results disappear on compaction;
// restored results are found without relying on process-local observations.
func (l *contextSelectionLedger) SnapshotForRequest(transcript []msg.Message, systemPrompt string, skills []tools.SkillRecord, catalogMode bool) []slashcommands.ContextSelection {
	l.mu.Lock()
	excluded := make(map[string]slashcommands.ContextSelection, len(l.excluded))
	for k, v := range l.excluded {
		excluded[k] = v
	}
	transient := make(map[string]slashcommands.ContextSelection, len(l.transient))
	for k, v := range l.transient {
		transient[k] = v
	}
	drops := make(map[string][]slashcommands.ContextSelection, len(l.searchDrops))
	for k, v := range l.searchDrops {
		drops[k] = append([]slashcommands.ContextSelection(nil), v...)
	}
	requestUser := l.requestUser
	l.mu.Unlock()

	rows := excluded
	put := func(e slashcommands.ContextSelection) { rows[selectionKey(e.Kind, e.Material, e.Name)] = e }
	if requestUser != "" && requestUser == lastTypedUser(transcript) {
		for _, e := range transient {
			put(e)
		}
	}

	calls := map[string]msg.ToolCall{}
	for _, m := range transcript {
		if a, ok := m.(msg.AssistantMessage); ok {
			for _, call := range msg.ToolCallsOf(a.Content) {
				calls[call.ID] = call
			}
		}
	}
	for _, m := range transcript {
		r, ok := m.(msg.ToolResultMessage)
		if !ok || r.IsError {
			continue
		}
		call, ok := calls[r.ToolCallID]
		if !ok || call.Name != r.ToolName {
			continue
		}
		switch call.Name {
		case "skill":
			if name, ok := call.Arguments["skill"].(string); ok && name != "" {
				put(selection("skill", "body", name, "exact-name invocation", true))
			}
		case "task":
			if name, ok := call.Arguments["subagent_type"].(string); ok && name != "" {
				put(selection("agent", "result", name, "exact-name invocation", true))
			}
		case "capability_search":
			for _, match := range searchResultLine.FindAllStringSubmatch(msg.TextOf(r.Content), 10) {
				put(selection(match[2], "metadata", match[1], "search hit", true))
			}
			for _, drop := range drops[r.ToolCallID] {
				put(drop)
			}
		}
	}
	if catalogMode {
		catalogText := ""
		if _, tail, ok := strings.Cut(systemPrompt, "<available_skills>"); ok {
			catalogText, _, _ = strings.Cut(tail, "</available_skills>")
		}
		overflow := map[string]bool{}
		if _, tail, ok := strings.Cut(catalogText, "invoke any of them by exact name with the skill tool: "); ok {
			line, _, _ := strings.Cut(tail, " -->")
			for _, name := range strings.Split(line, ", ") {
				overflow[strings.TrimSpace(name)] = true
			}
		}
		for _, skill := range skills {
			if _, blocked := excluded[selectionKey("skill", "metadata", skill.Name)]; blocked {
				continue
			}
			if strings.Contains(catalogText, "<name>"+escapeXML(skill.Name)+"</name>") {
				put(selection("skill", "metadata", skill.Name, "catalog listing", true))
			} else if overflow[skill.Name] {
				put(selection("skill", "metadata", skill.Name, "budget-dropped", false))
			}
		}
	}
	for _, match := range memoryPath.FindAllStringSubmatch(systemPrompt, maxContextSelections) {
		if path, err := strconv.Unquote(match[1]); err == nil {
			put(selection("memory", "body", path, "Claude instructions", true))
		}
	}
	for _, match := range autoMemoryDir.FindAllStringSubmatch(systemPrompt, 1) {
		if dir, err := strconv.Unquote(match[1]); err == nil {
			put(selection("memory", "index", filepath.Join(dir, "MEMORY.md"), "Claude auto memory", true))
		}
	}
	out := make([]slashcommands.ContextSelection, 0, len(rows))
	for _, v := range rows {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Material < out[j].Material
	})
	if len(out) > maxContextSelections {
		out = out[:maxContextSelections]
	}
	return out
}

// ContextSelectionNamespace holds the last request's selection decisions in
// the session value store (key contextSelectionKey).
const (
	ContextSelectionNamespace = "kiln.context.selection"
	contextSelectionKey       = "last"
)

type persistedSelection struct {
	Kind     string `json:"kind"`
	Material string `json:"material"`
	Name     string `json:"name"`
	Reason   string `json:"reason"`
	Included bool   `json:"included"`
}

type persistedDrops struct {
	CallID string               `json:"callId"`
	Rows   []persistedSelection `json:"rows"`
}

// persistedContextSelection stores names, reason codes and call IDs only:
// never prompt text or memory content. Request is a digest of the typed
// request, so a branch switch or compaction that removes that request from
// the projected transcript also hides its decisions.
type persistedContextSelection struct {
	Version int                  `json:"v"`
	Request string               `json:"request"`
	Rows    []persistedSelection `json:"rows"`
	Drops   []persistedDrops     `json:"drops"`
}

// Marshal captures the current request's transient decisions and the bounded
// search-drop ledger. It returns nil when there is no typed request yet.
func (l *contextSelectionLedger) Marshal() json.RawMessage {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.requestUser == "" {
		return nil
	}
	rec := persistedContextSelection{Version: 1, Request: l.requestUser, Rows: []persistedSelection{}, Drops: []persistedDrops{}}
	for _, e := range l.transient {
		rec.Rows = append(rec.Rows, toPersisted(e))
	}
	sort.Slice(rec.Rows, func(i, j int) bool {
		a, b := rec.Rows[i], rec.Rows[j]
		return a.Kind+"\x00"+a.Material+"\x00"+a.Name < b.Kind+"\x00"+b.Material+"\x00"+b.Name
	})
	if len(rec.Rows) > maxContextSelections {
		rec.Rows = rec.Rows[:maxContextSelections]
	}
	for _, id := range l.dropOrder {
		d := persistedDrops{CallID: id}
		for _, e := range l.searchDrops[id] {
			d.Rows = append(d.Rows, toPersisted(e))
		}
		rec.Drops = append(rec.Drops, d)
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return nil
	}
	return raw
}

// Restore loads a Marshal record into an empty ledger. A malformed or unknown
// record is ignored, which is today's behaviour for sessions without one.
func (l *contextSelectionLedger) Restore(raw json.RawMessage) {
	var rec persistedContextSelection
	if json.Unmarshal(raw, &rec) != nil || rec.Version != 1 || rec.Request == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requestUser = rec.Request
	l.transient = map[string]slashcommands.ContextSelection{}
	for _, r := range rec.Rows {
		if len(l.transient) >= maxContextSelections {
			break
		}
		e := fromPersisted(r)
		l.transient[selectionKey(e.Kind, e.Material, e.Name)] = e
	}
	for _, d := range rec.Drops {
		if d.CallID == "" || len(l.dropOrder) >= maxContextSelections {
			continue
		}
		if _, dup := l.searchDrops[d.CallID]; dup {
			continue
		}
		l.dropOrder = append(l.dropOrder, d.CallID)
		for _, r := range d.Rows {
			if len(l.searchDrops[d.CallID]) < 10 {
				l.searchDrops[d.CallID] = append(l.searchDrops[d.CallID], fromPersisted(r))
			}
		}
	}
}

func toPersisted(e slashcommands.ContextSelection) persistedSelection {
	return persistedSelection{Kind: e.Kind, Material: e.Material, Name: e.Name, Reason: e.Reason, Included: e.Included}
}

func fromPersisted(p persistedSelection) slashcommands.ContextSelection {
	return selection(p.Kind, p.Material, p.Name, p.Reason, p.Included)
}

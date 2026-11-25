package harness

import (
	"sort"
	"strings"

	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
)

// repairToolCalls fixes slips weaker models make in final's tool calls
// before the reply is committed, so the transcript, the permission gate,
// the hooks and the tool all see the same repaired call: a tool name off
// only by case, "-" for "_" or a wrapper prefix ("functions.bash"), and
// argument types the schema contradicts (tool.CoerceArgs). Adapted from
// hermes-agent's tool-call repair (NousResearch, MIT).
func (l *Lane) repairToolCalls(final *msg.AssistantMessage) {
	if final == nil {
		return
	}
	tools := l.h.toolSet()
	copied := false
	for i, c := range final.Content {
		call, ok := c.(msg.ToolCall)
		if !ok {
			continue
		}
		before := call.Name
		var coerced []string
		if _, known := tools.Get(call.Name); !known {
			if name, found := repairToolName(call.Name, l.callableNames(tools)); found {
				diag.L().Info("tool call repaired", "lane", l.name, "from", call.Name, "to", name)
				call.Name = name
			}
		}
		if t, known := tools.Get(call.Name); known && call.InvalidArgs == "" {
			if args, changed := tool.CoerceArgs(t.Parameters, call.Arguments); len(changed) > 0 {
				diag.L().Info("tool call repaired", "lane", l.name, "tool", call.Name, "coerced", strings.Join(changed, ","))
				call.Arguments = args
				coerced = changed
			}
		}
		if call.Name == before && len(coerced) == 0 {
			continue
		}
		if !copied {
			// Stream events may share the provider's slice; never write
			// into it.
			final.Content = append(msg.Blocks(nil), final.Content...)
			copied = true
		}
		final.Content[i] = call
	}
}

// callableNames is the tools a call may name: the lane's active set, or
// every registered tool when the lane records none (toolActive).
func (l *Lane) callableNames(tools *tool.Set) []string {
	if cfg, err := l.config(); err == nil && len(cfg.ActiveToolNames) > 0 {
		return cfg.ActiveToolNames
	}
	return tools.Names()
}

// toolNameKey folds the differences repairToolName forgives.
func toolNameKey(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimSuffix(name, "()")
	for _, p := range []string{"functions.", "functions:", "default_api.", "default_api:", "tools.", "tool."} {
		if len(name) > len(p) && strings.EqualFold(name[:len(p)], p) {
			name = name[len(p):]
			break
		}
	}
	return strings.ReplaceAll(strings.ToLower(name), "-", "_")
}

// repairToolName finds the one callable name that name differs from only
// by case, "-" for "_", or a wrapper prefix. Two or more such names, or
// none, is no repair: a guess could run the wrong tool.
func repairToolName(name string, callable []string) (string, bool) {
	key := toolNameKey(name)
	found := ""
	for _, c := range callable {
		if toolNameKey(c) == key {
			if found != "" && found != c {
				return "", false
			}
			found = c
		}
	}
	return found, found != ""
}

// closeToolNames are up to three callable names within a small edit
// distance of name, closest first, for the unknown-tool refusal.
func closeToolNames(name string, callable []string) []string {
	key := toolNameKey(name)
	limit := max(2, len(key)/3)
	type cand struct {
		name string
		d    int
	}
	var cs []cand
	for _, c := range callable {
		if d := editDistance(key, toolNameKey(c)); d <= limit {
			cs = append(cs, cand{c, d})
		} else if k := toolNameKey(c); len(key) >= 4 && (strings.Contains(k, key) || strings.Contains(key, k) && len(k) >= 4) {
			cs = append(cs, cand{c, limit})
		}
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].d < cs[j].d })
	var out []string
	for i := 0; i < len(cs) && i < 3; i++ {
		out = append(out, cs[i].name)
	}
	return out
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

package jsonl

import (
	"encoding/json"
	"fmt"

	"github.com/andrepato/harness/internal/session"
)

// HeaderFormat is which header shape a parsed line-1 turned out to be.
type HeaderFormat string

const (
	FormatV4       HeaderFormat = "v4"
	FormatV3Legacy HeaderFormat = "v3-legacy"
)

// LegacyV3Header is the line-1 shape of a format-3 session.
type LegacyV3Header struct {
	Type          string `json:"type"`    // "session"
	Version       int    `json:"version"` // 3
	ID            string `json:"id"`
	Cwd           string `json:"cwd"`
	Timestamp     string `json:"timestamp"`
	ParentSession string `json:"parentSession,omitempty"`
}

// ParsedHeader is the result of ParseHeader: exactly one of V4 or V3Legacy
// is set, matching Format.
type ParsedHeader struct {
	Format   HeaderFormat
	V4       *session.Header
	V3Legacy *LegacyV3Header
}

// IsV4Header reports whether the decoded JSON value has every field
// isJsonlStorageHeader (codec.js) checks.
func isV4Header(m map[string]any) bool {
	if kind, _ := m["kind"].(string); kind != "header" {
		return false
	}
	v, ok := m["v"].(float64)
	if !ok || int(v) != session.FormatVersion {
		return false
	}
	if _, ok := m["id"].(string); !ok {
		return false
	}
	if _, ok := m["cwd"].(string); !ok {
		return false
	}
	sv, ok := m["storageVersion"].(float64)
	if !ok || sv < 1 || sv != float64(int64(sv)) {
		return false
	}
	ca, ok := m["createdAt"].(float64)
	if !ok || ca < 0 || ca != float64(int64(ca)) {
		return false
	}
	if ns, present := m["nextSeq"]; present {
		f, ok := ns.(float64)
		if !ok || f < 1 || f != float64(int64(f)) {
			return false
		}
	}
	if p, present := m["parentSessionId"]; present {
		if _, ok := p.(string); !ok {
			return false
		}
	}
	if p, present := m["legacyParentSessionPath"]; present {
		if _, ok := p.(string); !ok {
			return false
		}
	}
	return true
}

func isLegacyV3Header(m map[string]any) bool {
	if t, _ := m["type"].(string); t != "session" {
		return false
	}
	v, ok := m["version"].(float64)
	if !ok || v != 3 {
		return false
	}
	if _, ok := m["id"].(string); !ok {
		return false
	}
	if _, ok := m["cwd"].(string); !ok {
		return false
	}
	if _, ok := m["timestamp"].(string); !ok {
		return false
	}
	if p, present := m["parentSession"]; present {
		if _, ok := p.(string); !ok {
			return false
		}
	}
	return true
}

// ParseHeader parses line 1 of a JSONL session file and identifies its
// format. It mirrors parseJsonlSessionHeader in codec.js.
func ParseHeader(line string) (ParsedHeader, error) {
	var generic map[string]any
	if err := json.Unmarshal([]byte(line), &generic); err != nil {
		return ParsedHeader{}, fmt.Errorf("jsonl: invalid session header: not valid JSON: %w", err)
	}
	if isV4Header(generic) {
		var h session.Header
		if err := json.Unmarshal([]byte(line), &h); err != nil {
			return ParsedHeader{}, err
		}
		return ParsedHeader{Format: FormatV4, V4: &h}, nil
	}
	if isLegacyV3Header(generic) {
		var h LegacyV3Header
		if err := json.Unmarshal([]byte(line), &h); err != nil {
			return ParsedHeader{}, err
		}
		return ParsedHeader{Format: FormatV3Legacy, V3Legacy: &h}, nil
	}
	return ParsedHeader{}, fmt.Errorf("jsonl: unsupported session header")
}

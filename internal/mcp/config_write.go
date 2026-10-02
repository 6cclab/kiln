package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/andrepato/harness/internal/claude/paths"
)

// ErrNotFound is returned by RemoveServer when the name is not configured
// in the given scope.
var ErrNotFound = errors.New("no such MCP server")

// AddServer writes one server into scope the way `claude mcp add --scope`
// does, so either tool sees it: local and user go into ~/.claude.json
// (projects[cwd].mcpServers and the top-level mcpServers), project into
// <cwd>/.mcp.json. An existing entry of the same name is replaced. It
// returns the file it wrote.
func AddServer(scope, cwd, name string, cfg ServerConfig) (string, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return editScope(scope, cwd, func(servers map[string]json.RawMessage) error {
		servers[name] = raw
		return nil
	})
}

// RemoveServer deletes name from scope; ErrNotFound when it is not there.
func RemoveServer(scope, cwd, name string) (string, error) {
	return editScope(scope, cwd, func(servers map[string]json.RawMessage) error {
		if _, ok := servers[name]; !ok {
			return ErrNotFound
		}
		delete(servers, name)
		return nil
	})
}

// editScope loads scope's mcpServers map, applies edit, and writes the file
// back, returning its path.
//
// ~/.claude.json is Claude Code's own state file and holds far more than
// MCP servers, so only the objects on the way to one mcpServers map are
// decoded; every other value is carried through as raw JSON, byte for
// byte, and numbers are never round-tripped through float64. The write is
// atomic (temp file + rename) and keeps the file's permissions.
func editScope(scope, cwd string, edit func(map[string]json.RawMessage) error) (string, error) {
	switch scope {
	case ScopeProject:
		path := filepath.Join(cwd, ".mcp.json")
		doc, err := readObject(path)
		if err != nil {
			return "", err
		}
		servers, err := object(doc["mcpServers"])
		if err != nil {
			return "", fmt.Errorf("%s: mcpServers: %w", path, err)
		}
		if err := edit(servers); err != nil {
			return "", err
		}
		if doc["mcpServers"], err = marshalObject(servers); err != nil {
			return "", err
		}
		return path, writeObject(path, doc, 0o644)
	case ScopeUser, ScopeLocal:
		path := paths.ClaudeJSONPath()
		doc, err := readObject(path)
		if err != nil {
			return "", err
		}
		if scope == ScopeUser {
			servers, err := object(doc["mcpServers"])
			if err != nil {
				return "", fmt.Errorf("%s: mcpServers: %w", path, err)
			}
			if err := edit(servers); err != nil {
				return "", err
			}
			if doc["mcpServers"], err = marshalObject(servers); err != nil {
				return "", err
			}
			return path, writeObject(path, doc, 0o600)
		}
		projects, err := object(doc["projects"])
		if err != nil {
			return "", fmt.Errorf("%s: projects: %w", path, err)
		}
		key := projectKey(projects, cwd)
		project, err := object(projects[key])
		if err != nil {
			return "", fmt.Errorf("%s: projects[%s]: %w", path, key, err)
		}
		servers, err := object(project["mcpServers"])
		if err != nil {
			return "", fmt.Errorf("%s: projects[%s].mcpServers: %w", path, key, err)
		}
		if err := edit(servers); err != nil {
			return "", err
		}
		if project["mcpServers"], err = marshalObject(servers); err != nil {
			return "", err
		}
		if projects[key], err = marshalObject(project); err != nil {
			return "", err
		}
		if doc["projects"], err = marshalObject(projects); err != nil {
			return "", err
		}
		return path, writeObject(path, doc, 0o600)
	}
	return "", fmt.Errorf("unknown scope %q (want local, project or user)", scope)
}

// projectKey is the projects entry for cwd: an existing key matching cwd
// as given or with symlinks resolved, else cwd with symlinks resolved (what
// Claude Code records).
func projectKey(projects map[string]json.RawMessage, cwd string) string {
	if _, ok := projects[cwd]; ok {
		return cwd
	}
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return cwd
	}
	return real
}

// readObject decodes path as a JSON object of raw values; a missing file is
// an empty object, an unparseable one an error (never silently replaced).
func readObject(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON; not changing it: %w", path, err)
	}
	if doc == nil {
		doc = map[string]json.RawMessage{}
	}
	return doc, nil
}

func object(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return map[string]json.RawMessage{}, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	return m, nil
}

// marshalObject encodes m without json.Marshal's HTML escaping, which
// would rewrite "<" in other tools' values as \u003c.
func marshalObject(m map[string]json.RawMessage) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

// writeObject writes doc indented with two spaces, atomically. An existing
// file keeps its mode; a new one gets mode.
func writeObject(path string, doc map[string]json.RawMessage, mode os.FileMode) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return err
	}
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

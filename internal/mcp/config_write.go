package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/andrepato/harness/internal/claude/paths"
	"github.com/andrepato/harness/internal/claude/writesettings"
)

// ErrNotFound is returned by RemoveServer when the name is not configured
// in the given scope's kiln file.
var ErrNotFound = errors.New("no such MCP server")

// AddServer writes one server into scope, in kiln's own files: user and
// local go into ~/.kiln/mcp.json (the top-level "mcpServers" and
// "projects"[cwd].mcpServers, mirroring how Claude Code shapes
// ~/.claude.json), project into <cwd>/.kiln/mcp.json ("mcpServers"), meant
// to be committed like Claude Code's own .mcp.json. kiln never writes
// ~/.claude.json or .mcp.json - Claude Code's own files - only reads them
// (see Resolve). An existing kiln entry of the same name is replaced. It
// returns the file it wrote.
func AddServer(scope, cwd, name string, cfg ServerConfig) (string, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return kilnEditScope(scope, cwd, func(servers map[string]json.RawMessage) error {
		servers[name] = raw
		return nil
	})
}

// RemoveServer deletes name from scope's kiln file; ErrNotFound when it is
// not there. It never touches a Claude Code file - a name that exists
// only in one of those is the caller's (mcp_cmd.go's) job to detect and
// report, via KilnServerIn/ServerIn, before calling this.
func RemoveServer(scope, cwd, name string) (string, error) {
	return kilnEditScope(scope, cwd, func(servers map[string]json.RawMessage) error {
		if _, ok := servers[name]; !ok {
			return ErrNotFound
		}
		delete(servers, name)
		return nil
	})
}

// kilnEditScope loads scope's mcpServers map from kiln's own file, applies
// edit, and writes the file back, returning its path. Mirrors editScope's
// shape (raw-message pass-through for every other key) but targets
// paths.KilnUserMCPPath / paths.KilnProjectMCPPath instead of Claude
// Code's ~/.claude.json / .mcp.json.
func kilnEditScope(scope, cwd string, edit func(map[string]json.RawMessage) error) (string, error) {
	switch scope {
	case ScopeProject:
		return editServersAt(paths.KilnProjectMCPPath(cwd), "", edit)
	case ScopeUser:
		return editServersAt(paths.KilnUserMCPPath(), "", edit)
	case ScopeLocal:
		return editServersAt(paths.KilnUserMCPPath(), cwd, edit)
	}
	return "", fmt.Errorf("unknown scope %q (want local, project or user)", scope)
}

// editServersAt loads path's mcpServers map - top-level, or
// projects[cwd].mcpServers when cwd is non-empty (kiln's local scope,
// mirroring ~/.claude.json's own projects[cwd].mcpServers) - applies edit,
// and writes the file back atomically through writesettings.WriteJSON,
// which refuses to write through a symlinked file or .kiln directory
// first.
func editServersAt(path, cwd string, edit func(map[string]json.RawMessage) error) (string, error) {
	doc, err := readObject(path)
	if err != nil {
		return "", err
	}
	if cwd == "" {
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
		return path, writesettings.WriteJSON(path, doc)
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
	return path, writesettings.WriteJSON(path, doc)
}

// projectKey is the projects entry for cwd: an existing key matching cwd
// as given or with symlinks resolved, else cwd with symlinks resolved (what
// Claude Code records, and what kiln mirrors for its own local scope).
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
// would rewrite "<" in other tools' values as <.
func marshalObject(m map[string]json.RawMessage) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

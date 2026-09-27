package jsonl

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/session"
	"github.com/google/uuid"
)

// Metadata is one session's directory-listing entry: identity plus where it
// lives and when it last changed.
type Metadata struct {
	ID                      string
	CreatedAt               int64
	StorageVersion          int
	Cwd                     string
	Path                    string
	ModifiedAt              int64
	ParentSessionID         string
	LegacyParentSessionPath string
}

func metadataFromHeader(h session.Header, path string, modifiedAt int64) Metadata {
	return Metadata{
		ID:                      h.ID,
		CreatedAt:               h.CreatedAt,
		StorageVersion:          h.StorageVersion,
		Cwd:                     h.Cwd,
		Path:                    path,
		ModifiedAt:              modifiedAt,
		ParentSessionID:         h.ParentSessionID,
		LegacyParentSessionPath: h.LegacyParentSessionPath,
	}
}

var dirSeparators = regexp.MustCompile(`[/\\:]`)
var leadingSeparator = regexp.MustCompile(`^[/\\]`)

// DirectoryName is sessionDirectoryName(cwd) from repo.js:23: "--" + cwd
// with a leading "/" or "\" stripped and every "/", "\" and ":" replaced by
// "-", then "--" appended. This encoding is lossy across path styles (e.g.
// "/a/b" and "/a-b" both map to "--a-b--").
func DirectoryName(cwd string) string {
	stripped := leadingSeparator.ReplaceAllString(cwd, "")
	replaced := dirSeparators.ReplaceAllString(stripped, "-")
	return "--" + replaced + "--"
}

// FileName is sessionFileName(createdAt, id) from repo.js:27: createdAt as
// an ISO-8601 UTC timestamp with ":" and "." replaced by "-", an underscore,
// the URL-encoded session id, and ".jsonl".
func FileName(createdAt int64, id string) string {
	t := time.UnixMilli(createdAt).UTC()
	iso := t.Format("2006-01-02T15:04:05.000Z")
	ts := strings.NewReplacer(":", "-", ".", "-").Replace(iso)
	return fmt.Sprintf("%s_%s.jsonl", ts, url.QueryEscape(id))
}

// Repo is a JSONL-file-backed session.SessionRepo-equivalent lifecycle:
// create, open, list, delete and fork, rooted at one sessions directory. It
// mirrors JsonlSessionRepo in repo.js.
type Repo struct {
	Root string // defaults to "~/.harness/sessions" via NewRepo
	Now  func() time.Time
}

// NewRepo returns a Repo rooted at root, or "~/.harness/sessions" if root is
// "".
func NewRepo(root string) (*Repo, error) {
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		root = filepath.Join(home, ".harness", "sessions")
	}
	return &Repo{Root: root, Now: time.Now}, nil
}

func (r *Repo) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// CreateOptions configures Repo.Create.
type CreateOptions struct {
	Cwd             string
	ID              string // "" generates a fresh uuidv7
	ParentSessionID string
}

// Create makes a brand-new session directory (if needed) and file, and
// returns its opened Storage and Metadata.
func (r *Repo) Create(opts CreateOptions) (*Storage, Metadata, error) {
	createdAt := r.now().UnixMilli()
	id := opts.ID
	if id == "" {
		var err error
		id, err = uuidv7At(createdAt)
		if err != nil {
			return nil, Metadata{}, err
		}
	}
	// Store (and bucket) the symlink-resolved cwd, not opts.Cwd verbatim:
	// this is what keeps a project reached via a symlinked path (e.g.
	// macOS's /tmp -> /private/tmp) from splitting into a second, separate
	// project every time it is reached via a different spelling. Older
	// sessions recorded before this still carry their original,
	// unresolved cwd; List's resolved-to-resolved comparison is what keeps
	// those findable rather than requiring every session to be rewritten.
	resolvedCwd := resolveCwd(opts.Cwd)
	dir := filepath.Join(r.Root, DirectoryName(resolvedCwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, Metadata{}, fmt.Errorf("jsonl: failed to create sessions directory %s: %w", dir, err)
	}
	if err := assertSessionIDAvailable(dir, id); err != nil {
		return nil, Metadata{}, err
	}
	path := filepath.Join(dir, FileName(createdAt, id))
	header := session.Header{
		V:               session.FormatVersion,
		Kind:            "header",
		ID:              id,
		StorageVersion:  session.StorageVersion,
		CreatedAt:       createdAt,
		Cwd:             resolvedCwd,
		ParentSessionID: opts.ParentSessionID,
	}
	// Create writes only the header; a lane's initial pi.branch.tip /
	// pi.lane.config / pi.lane.state values are written by
	// harness.Harness.Lane on that lane's first use, matching pi's
	// repo.create (which writes only the header) and moving the lane
	// bootstrap into the harness layer where the lane's actual
	// configuration (model, tools) is known.
	storage, err := Create(path, header, nil, r.Now)
	if err != nil {
		_ = os.Remove(path)
		return nil, Metadata{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, Metadata{}, err
	}
	return storage, metadataFromHeader(header, path, info.ModTime().UnixMilli()), nil
}

func assertSessionIDAvailable(dir, id string) error {
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	suffix := "_" + url.QueryEscape(id) + ".jsonl"
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), suffix) {
			return fmt.Errorf("jsonl: session already exists: %s", id)
		}
	}
	return nil
}

// Open opens a session file by path.
func (r *Repo) Open(path string) (*Storage, Metadata, error) {
	storage, err := Open(path, r.Now)
	if err != nil {
		return nil, Metadata{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, Metadata{}, err
	}
	return storage, metadataFromHeader(storage.Header(), path, info.ModTime().UnixMilli()), nil
}

// resolveCwd returns cwd's symlink-resolved form so that two spellings of
// the same directory (e.g. /tmp/x and its macOS-canonical /private/tmp/x)
// compare equal. Resolution can fail — the directory may no longer exist,
// or cwd may already be gone — so on error this falls back to the merely
// cleaned path rather than propagating the error: a session-matching
// helper must always return something comparable, never bubble up a
// filesystem error for what is ultimately a best-effort match. Empty stays
// empty (List's "no filter" sentinel).
func resolveCwd(cwd string) string {
	if cwd == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		return resolved
	}
	return filepath.Clean(cwd)
}

// List returns every session under Root (optionally filtered to one cwd),
// newest createdAt first.
//
// A cwd filter compares resolved-to-resolved (resolveCwd on both the
// query and each candidate's stored header.Cwd), not by exact string
// equality: this is what lets a session recorded via one spelling of a
// directory (e.g. an older session's unresolved /tmp/x, from before Create
// started storing the resolved path) still be found when queried via
// another spelling (/private/tmp/x). Because a stored cwd's directory
// bucket (DirectoryName) is keyed off its own literal string, sessions for
// the "same" resolved directory can live in two buckets — the resolved
// spelling's and the queried spelling's — and both are scanned.
func (r *Repo) List(cwd string) ([]Metadata, error) {
	if _, err := os.Stat(r.Root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	resolvedQuery := resolveCwd(cwd)
	var dirs []string
	if cwd == "" {
		entries, err := os.ReadDir(r.Root)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, filepath.Join(r.Root, e.Name()))
			}
		}
	} else {
		// A project's sessions live in the resolved spelling's bucket
		// (Create stores that) and, for sessions recorded before Create
		// resolved symlinks, in the bucket of the spelling they were made
		// under. Scanning just those two keeps List proportional to the
		// project rather than to every session on the machine.
		dirs = append(dirs, filepath.Join(r.Root, DirectoryName(resolvedQuery)))
		if lit := DirectoryName(cwd); lit != DirectoryName(resolvedQuery) {
			dirs = append(dirs, filepath.Join(r.Root, lit))
		}
	}
	var out []Metadata
	for _, dir := range dirs {
		files, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			path := filepath.Join(dir, f.Name())
			meta, err := readSessionMetadata(path)
			if err != nil {
				continue // matches repo.js: skip files whose header fails to parse
			}
			if cwd == "" || resolveCwd(meta.Cwd) == resolvedQuery {
				out = append(out, meta)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Cwd < out[j].Cwd
	})
	return out, nil
}

func readSessionMetadata(path string) (Metadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return Metadata{}, err
	}
	defer f.Close()
	line, err := readFirstLine(f)
	if err != nil {
		return Metadata{}, err
	}
	parsed, err := ParseHeader(line)
	if err != nil {
		return Metadata{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Metadata{}, err
	}
	if parsed.Format == FormatV3Legacy {
		return Metadata{
			ID:         parsed.V3Legacy.ID,
			CreatedAt:  parseLegacyTimestamp(parsed.V3Legacy.Timestamp),
			Cwd:        parsed.V3Legacy.Cwd,
			Path:       path,
			ModifiedAt: info.ModTime().UnixMilli(),
		}, nil
	}
	return metadataFromHeader(*parsed.V4, path, info.ModTime().UnixMilli()), nil
}

func parseLegacyTimestamp(s string) int64 {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

func readFirstLine(f *os.File) (string, error) {
	buf := make([]byte, 0, 4096)
	chunk := make([]byte, 4096)
	for {
		n, err := f.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			if idx := indexByte(buf, '\n'); idx >= 0 {
				return string(buf[:idx]), nil
			}
		}
		if err != nil {
			if len(buf) > 0 {
				return string(buf), nil
			}
			return "", err
		}
	}
}

func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}

// Delete removes a session file. It refuses to delete a currently-open
// storage; callers must Close it first (this package does not track open
// handles the way repo.js's JsonlSessionRepo does).
func (r *Repo) Delete(meta Metadata) error {
	if _, err := os.Stat(meta.Path); err != nil {
		return fmt.Errorf("jsonl: session file does not exist: %s", meta.Path)
	}
	return os.Remove(meta.Path)
}

// uuidv7At returns a UUIDv7 string whose 48-bit timestamp field is
// createdAtMs, matching pi's uuidv7(timestampMs) (used for session and
// entry ids derived from a specific createdAt rather than "now").
func uuidv7At(createdAtMs int64) (string, error) {
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
	out := uuid.UUID(b)
	return out.String(), nil
}

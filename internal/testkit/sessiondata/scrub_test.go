package sessiondata

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// kindTypeNamespace collects the set of (kind,type,namespace) triples
// present across every operation on every non-header line of a session
// file. type and namespace are empty string when the operation doesn't
// carry that field (e.g. a "value" op has no "type", an "entry" op has no
// "namespace").
func kindTypeNamespace(t *testing.T, data []byte) map[string]bool {
	t.Helper()
	set := map[string]bool{}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if lineNo == 1 {
			// Header line: not an operation array.
			continue
		}
		// A session line is either a single operation object, or a JSON
		// array of operation objects.
		var raw any
		if err := json.Unmarshal(line, &raw); err != nil {
			t.Fatalf("line %d: not valid JSON: %v", lineNo, err)
		}
		var ops []map[string]any
		switch v := raw.(type) {
		case map[string]any:
			ops = []map[string]any{v}
		case []any:
			for _, e := range v {
				m, ok := e.(map[string]any)
				if !ok {
					t.Fatalf("line %d: array element is not an object: %T", lineNo, e)
				}
				ops = append(ops, m)
			}
		default:
			t.Fatalf("line %d: unexpected JSON shape %T", lineNo, v)
		}
		for _, op := range ops {
			kind, _ := op["kind"].(string)
			typ, _ := op["type"].(string)
			ns, _ := op["namespace"].(string)
			set[fmt.Sprintf("%s|%s|%s", kind, typ, ns)] = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanning: %v", err)
	}
	return set
}

func lineCount(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := bytes.Count(data, []byte("\n"))
	if !bytes.HasSuffix(data, []byte("\n")) {
		n++
	}
	return n
}

func header(t *testing.T, data []byte) map[string]any {
	t.Helper()
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	if !scanner.Scan() {
		t.Fatalf("no lines in data")
	}
	var h map[string]any
	if err := json.Unmarshal(scanner.Bytes(), &h); err != nil {
		t.Fatalf("header line is not a JSON object: %v", err)
	}
	return h
}

// runScrubFixture scrubs a fixture session file end to end and asserts the
// documented invariants: same line count, same (kind,type,namespace) set
// per line, same header fields other than cwd (which is path-scrubbed),
// and no leftover "andrepato" anywhere in the output.
func runScrubFixture(t *testing.T, path string) []byte {
	t.Helper()
	in, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening fixture %s: %v", path, err)
	}
	defer in.Close()

	var out bytes.Buffer
	if err := Scrub(in, &out); err != nil {
		t.Fatalf("Scrub(%s): %v", path, err)
	}
	scrubbed := out.Bytes()

	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fixture %s: %v", path, err)
	}

	if got, want := lineCount(scrubbed), lineCount(orig); got != want {
		t.Errorf("%s: line count = %d, want %d", path, got, want)
	}

	origHeader := header(t, orig)
	scrubbedHeader := header(t, scrubbed)
	for k, v := range origHeader {
		if k == "cwd" {
			continue
		}
		if scrubbedHeader[k] != v {
			t.Errorf("%s: header field %q changed: got %v, want %v", path, k, scrubbedHeader[k], v)
		}
	}
	if _, ok := scrubbedHeader["cwd"]; !ok {
		t.Errorf("%s: scrubbed header lost cwd field entirely", path)
	}

	origSet := kindTypeNamespace(t, orig)
	scrubbedSet := kindTypeNamespace(t, scrubbed)
	if len(origSet) != len(scrubbedSet) {
		t.Errorf("%s: (kind,type,namespace) set size changed: got %d, want %d", path, len(scrubbedSet), len(origSet))
	}
	for k := range origSet {
		if !scrubbedSet[k] {
			t.Errorf("%s: missing (kind,type,namespace) triple %q after scrubbing", path, k)
		}
	}

	if strings.Contains(string(scrubbed), "andrepato") {
		t.Errorf("%s: scrubbed output still contains \"andrepato\"", path)
	}

	return scrubbed
}

func TestScrubAgainstRealSessions(t *testing.T) {
	sessionsDir := "/Users/andrepato/.harness/sessions"
	if _, err := os.Stat(sessionsDir); err != nil {
		t.Skipf("real session directory not available in this environment: %v", err)
	}

	paths := []string{
		filepath.Join(sessionsDir, "--Users-andrepato-projects-harness--", "2026-09-23T11-37-47-498Z_01a0ce0e-bcea-7701-a97e-cc374e8c56d1.jsonl"),
		filepath.Join(sessionsDir, "--Users-andrepato-projects-harness--", "2026-09-23T13-15-57-415Z_01a0ce68-9c67-7740-9867-7150069d61e6.jsonl"),
		filepath.Join(sessionsDir, "--private-tmp-harness-demo--", "2026-09-23T04-00-59-438Z_01a0cc6c-862e-70d7-b46f-cf4005693013.jsonl"),
	}
	for _, p := range paths {
		p := p
		t.Run(filepath.Base(p), func(t *testing.T) {
			if _, err := os.Stat(p); err != nil {
				t.Skipf("fixture source not available: %v", err)
			}
			runScrubFixture(t, p)
		})
	}
}

func TestScrubRedactsKnownPatterns(t *testing.T) {
	header := `{"v":4,"kind":"header","id":"01a0cc6c-862e-70d7-b46f-cf4005693013","storageVersion":1,"createdAt":1790136059438,"cwd":"/Users/andrepato/projects/harness"}` + "\n"

	// Each pattern is exercised in its own short (<200 char) string so that
	// length-based truncation cannot hide it from the assertion below —
	// truncation and redaction are independent behaviours and are tested
	// independently.
	cases := []struct {
		name    string
		text    string
		want    string
		notWant string
	}{
		{"email", "contact me at person@example.com please", "scrubbed@example.com", "person@example.com"},
		{"api key", "key is sk-abcdefghijklmno for the service", "sk-[scrubbed]", "sk-abcdefghijklmno"},
		{"bearer", "sent token Bearer abc123def456 in header", "Bearer [scrubbed]", "abc123def456"},
		{"github token", "uses ghp_abcDEF123456 as credential", "ghp_[scrubbed]", "ghp_abcDEF123456"},
		{"slack token", "slack token xoxb-abc123-def456 leaked", "xox-[scrubbed]", "xoxb-abc123-def456"},
		{"home path", "see /Users/andrepato/secret for details", "/Users/tester/secret", "andrepato"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := fmt.Sprintf(`[{"kind":"entry","id":"e1","parentId":null,"type":"message","message":{"role":"user","content":[{"type":"text","text":%q}],"timestamp":1},"seq":1,"namespace":"ns","key":"k1"}]`, c.text) + "\n"

			var out bytes.Buffer
			if err := Scrub(strings.NewReader(header+body), &out); err != nil {
				t.Fatalf("Scrub: %v", err)
			}
			got := out.String()

			if !strings.Contains(got, c.want) {
				t.Errorf("expected output to contain %q, got:\n%s", c.want, got)
			}
			if strings.Contains(got, c.notWant) {
				t.Errorf("expected output to NOT contain %q, got:\n%s", c.notWant, got)
			}
		})
	}
}

func TestScrubTruncatesLongProse(t *testing.T) {
	header := `{"v":4,"kind":"header","id":"01a0cc6c-862e-70d7-b46f-cf4005693013","storageVersion":1,"createdAt":1790136059438,"cwd":"/Users/andrepato/projects/harness"}` + "\n"
	long := strings.Repeat("x", 250)
	body := fmt.Sprintf(`[{"kind":"entry","id":"e1","parentId":null,"type":"message","message":{"role":"user","content":[{"type":"text","text":"%s"}],"timestamp":1},"seq":1,"namespace":"ns","key":"k1"}]`, long) + "\n"

	var out bytes.Buffer
	if err := Scrub(strings.NewReader(header+body), &out); err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "…[scrubbed 210 chars]") {
		t.Errorf("expected truncation marker for 250-char string, got:\n%s", got)
	}
	if strings.Contains(got, long) {
		t.Errorf("long string was not truncated:\n%s", got)
	}
	// id-like fields must survive untouched, including when long.
	if !strings.Contains(got, `"id":"01a0cc6c-862e-70d7-b46f-cf4005693013"`) {
		t.Errorf("header id was altered:\n%s", got)
	}
}

func TestScrubExemptsIDLikeFieldsFromTruncation(t *testing.T) {
	longID := strings.Repeat("a", 300)
	line := fmt.Sprintf(`[{"kind":"value","op":"set","seq":1,"namespace":"ns","key":"%s","value":null}]`, longID)
	var out bytes.Buffer
	if err := Scrub(strings.NewReader(line+"\n"), &out); err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	if !strings.Contains(out.String(), longID) {
		t.Errorf("long id-like 'key' field value was truncated, want preserved:\n%s", out.String())
	}
}

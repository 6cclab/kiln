package jsonl

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func fixturePaths(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob("../../../testdata/sessions/*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no fixture files found")
	}
	return matches
}

// asAny decodes b into a generic any for structural (not byte) comparison.
func asAny(t *testing.T, label string, b []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	return v
}

// TestRoundTripFixtures decodes then re-encodes every line of every fixture
// and asserts the two are semantically identical (decode->any DeepEqual),
// reporting whether they also happen to be byte-identical.
func TestRoundTripFixtures(t *testing.T) {
	for _, path := range fixturePaths(t) {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			scanner := bufio.NewScanner(f)
			scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
			lineNo := 0
			byteIdentical := true
			for scanner.Scan() {
				lineNo++
				original := append([]byte(nil), scanner.Bytes()...)
				var reencoded []byte
				if lineNo == 1 {
					parsed, err := ParseHeader(string(original))
					if err != nil {
						t.Fatalf("line %d: parse header: %v", lineNo, err)
					}
					if parsed.Format != FormatV4 {
						t.Fatalf("line %d: expected v4 header", lineNo)
					}
					reencoded, err = json.Marshal(parsed.V4)
					if err != nil {
						t.Fatalf("line %d: marshal header: %v", lineNo, err)
					}
				} else {
					writes, err := ParseTransaction(original)
					if err != nil {
						t.Fatalf("line %d: parse transaction: %v", lineNo, err)
					}
					reencoded, err = SerializeTransaction(writes)
					if err != nil {
						t.Fatalf("line %d: serialize transaction: %v", lineNo, err)
					}
				}
				origAny := asAny(t, "original", original)
				reAny := asAny(t, "reencoded", reencoded)
				if !reflect.DeepEqual(origAny, reAny) {
					t.Fatalf("line %d: semantic mismatch:\noriginal:  %s\nreencoded: %s", lineNo, original, reencoded)
				}
				if !bytes.Equal(original, reencoded) {
					byteIdentical = false
				}
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
			t.Logf("%s: %d lines, byte-identical=%v", filepath.Base(path), lineNo, byteIdentical)
		})
	}
}

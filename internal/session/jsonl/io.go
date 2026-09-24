package jsonl

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/andrepato/harness/internal/session"
)

// ParseTransaction decodes one transaction line: a bare CommittedWrite
// object (one write) or a JSON array of them (several). It mirrors
// parseJsonlTransaction in io.js.
func ParseTransaction(line []byte) ([]session.CommittedWrite, error) {
	trimmed := skipLeadingSpace(line)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("jsonl: invalid transaction: empty line")
	}
	if trimmed[0] == '[' {
		var writes []session.CommittedWrite
		if err := json.Unmarshal(line, &writes); err != nil {
			return nil, fmt.Errorf("jsonl: invalid transaction: %w", err)
		}
		return writes, nil
	}
	var w session.CommittedWrite
	if err := json.Unmarshal(line, &w); err != nil {
		return nil, fmt.Errorf("jsonl: invalid transaction: %w", err)
	}
	return []session.CommittedWrite{w}, nil
}

func skipLeadingSpace(b []byte) []byte {
	i := 0
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\n', '\r':
			i++
			continue
		}
		break
	}
	return b[i:]
}

// SerializeTransaction encodes one transaction: a bare object when there is
// exactly one write, an array otherwise. It mirrors serializeJsonlTransaction
// in io.js.
func SerializeTransaction(writes []session.CommittedWrite) ([]byte, error) {
	if len(writes) == 1 {
		return json.Marshal(writes[0])
	}
	return json.Marshal(writes)
}

// PublishFileAtomically writes destinationPath's whole new content by first
// writing "<destinationPath>.tmp" via writeContent, then renaming it over
// destinationPath. If writeContent (or the rename) fails, the tmp file is
// removed and the original destinationPath is left untouched. It mirrors
// publishFileAtomically in io.js.
func PublishFileAtomically(destinationPath string, writeContent func(f *os.File) error) (err error) {
	tempPath := destinationPath + ".tmp"
	f, err := os.Create(tempPath)
	if err != nil {
		return fmt.Errorf("jsonl: failed to stage %s: %w", destinationPath, err)
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tempPath)
		}
	}()
	if werr := writeContent(f); werr != nil {
		return fmt.Errorf("jsonl: failed to append %s: %w", destinationPath, werr)
	}
	if cerr := f.Close(); cerr != nil {
		return fmt.Errorf("jsonl: failed to close %s: %w", destinationPath, cerr)
	}
	if rerr := os.Rename(tempPath, destinationPath); rerr != nil {
		err = fmt.Errorf("jsonl: failed to publish %s: %w", destinationPath, rerr)
		return err
	}
	return nil
}

// PublishJSONL streams a header and a sequence of transactions through
// PublishFileAtomically. It mirrors publishJsonl in io.js.
func PublishJSONL(destinationPath string, header session.Header, writeTransactions func(append func(writes []session.CommittedWrite) error) error) error {
	return PublishFileAtomically(destinationPath, func(f *os.File) error {
		headerBytes, err := json.Marshal(header)
		if err != nil {
			return err
		}
		if _, err := f.Write(append(headerBytes, '\n')); err != nil {
			return err
		}
		return writeTransactions(func(writes []session.CommittedWrite) error {
			line, err := SerializeTransaction(writes)
			if err != nil {
				return err
			}
			_, err = f.Write(append(line, '\n'))
			return err
		})
	})
}

// AppendTransaction appends one transaction line to an already-published
// file (a plain append, no fsync, matching storage.js's commit path).
func AppendTransaction(path string, writes []session.CommittedWrite) error {
	line, err := SerializeTransaction(writes)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("jsonl: failed to append %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("jsonl: failed to append %s: %w", path, err)
	}
	return nil
}

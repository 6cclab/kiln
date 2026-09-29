// Package storage writes small records to disk.
//
// Deliberate error-handling gap (for the QA audit prompt): Save ignores the
// error from Close, so a flush failure after a successful Write is never
// reported and the file can end up truncated.
package storage

import "os"

func Save(path string, data []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	f.Close() // bug: error from Close is discarded
	return nil
}

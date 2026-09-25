package faux

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// disconnectWriter wraps an http.ResponseWriter to implement the
// disconnect_after fault: after a given number of response body bytes
// have been written, or after a given duration has elapsed since the
// response began (whichever the script specified), the connection is
// closed instead of completing the write, so a real client observes an
// abrupt EOF partway through the response rather than a well-formed end.
//
// The cut is performed via http.Hijacker: the handler takes over the raw
// net.Conn and closes it directly, which the net/http server treats as
// the connection simply going away, with nothing further sent (no
// trailing chunk, no clean TLS close_notify). If the underlying
// ResponseWriter does not support hijacking (uncommon; net/http's default
// ResponseWriter always does for non-HTTP/2 connections), cut falls back
// to panic(http.ErrAbortHandler), which net/http recognizes specially: it
// aborts the response immediately without writing more and without
// logging a stack trace, which likewise surfaces to the client as a
// broken connection instead of a complete response.
type disconnectWriter struct {
	http.ResponseWriter
	spec    *disconnectSpec
	onCut   func()
	start   time.Time
	written int
	done    bool
}

// newDisconnectWriter wraps w so that, once spec's byte count or duration
// is reached, the connection is cut instead of the response completing.
// onCut, if non-nil, is called exactly once, at the moment of the cut, so
// callers can record the fault (e.g. bump a per-model disconnect count).
func newDisconnectWriter(w http.ResponseWriter, spec *disconnectSpec, onCut func()) *disconnectWriter {
	return &disconnectWriter{ResponseWriter: w, spec: spec, onCut: onCut, start: time.Now()}
}

// Write implements io.Writer. For a duration-based spec, it blocks on the
// first call until the duration has elapsed since the writer was created
// (matching "N ms after the response begins" as a hard deadline
// independent of how fast the in-memory response body happens to be
// produced, which would otherwise make the cut point racy against a
// near-instant write loop) and then cuts without writing p. For a
// byte-based spec, it performs the write and then checks the byte bound
// afterward, so a byte-based cut always includes whatever bytes crossed
// the threshold, matching "streams normally up to that point and then
// closes". Once cut, every subsequent Write returns an error without
// touching the underlying ResponseWriter.
func (d *disconnectWriter) Write(p []byte) (int, error) {
	if d.done {
		return 0, io.ErrClosedPipe
	}
	if d.spec.Duration > 0 {
		if remaining := d.spec.Duration - time.Since(d.start); remaining > 0 {
			time.Sleep(remaining)
		}
		d.cut()
		return 0, io.ErrClosedPipe
	}
	n, err := d.ResponseWriter.Write(p)
	d.written += n
	if err != nil {
		return n, err
	}
	if d.spec.Bytes > 0 && d.written >= d.spec.Bytes {
		d.cut()
		return n, io.ErrClosedPipe
	}
	return n, nil
}

// Flush implements http.Flusher by delegating to the wrapped
// ResponseWriter, so streaming responses (which flush after every SSE
// frame) keep working through this wrapper.
func (d *disconnectWriter) Flush() {
	if d.done {
		return
	}
	if f, ok := d.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack implements http.Hijacker by delegating to the wrapped
// ResponseWriter.
func (d *disconnectWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := d.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("faux: underlying ResponseWriter does not support hijacking")
	}
	return hj.Hijack()
}

// cut performs the disconnect exactly once: it flushes whatever has
// already been written, then hijacks and closes the raw connection, or
// falls back to panic(http.ErrAbortHandler) if hijacking isn't available.
func (d *disconnectWriter) cut() {
	if d.done {
		return
	}
	d.done = true
	if d.onCut != nil {
		d.onCut()
	}
	if f, ok := d.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
	if hj, ok := d.ResponseWriter.(http.Hijacker); ok {
		conn, _, err := hj.Hijack()
		if err == nil {
			_ = conn.Close()
			return
		}
	}
	panic(http.ErrAbortHandler)
}

// writeWithDisconnect writes body to w. With no spec, it's a plain
// Write. With a spec, it writes body one byte at a time through a
// disconnectWriter so the byte-count cut point is exact, stopping as soon
// as the cut fires (a duration-based cut may fire before any byte is
// written at all).
func writeWithDisconnect(w http.ResponseWriter, body []byte, spec *disconnectSpec, onCut func()) {
	if spec == nil {
		_, _ = w.Write(body)
		return
	}
	dw := newDisconnectWriter(w, spec, onCut)
	for i := range body {
		if _, err := dw.Write(body[i : i+1]); err != nil {
			return
		}
	}
}

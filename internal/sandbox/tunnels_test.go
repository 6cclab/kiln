package sandbox

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// heldUpstream accepts one TCP connection and holds it open, reading
// until the other side closes it; eof is closed then.
type heldUpstream struct {
	ln       net.Listener
	accepted chan net.Conn
	eof      chan struct{}
}

func newHeldUpstream(t *testing.T) *heldUpstream {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	u := &heldUpstream{ln: ln, accepted: make(chan net.Conn, 1), eof: make(chan struct{})}
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		u.accepted <- c
		_, _ = io.Copy(io.Discard, c)
		close(u.eof)
	}()
	t.Cleanup(func() {
		ln.Close()
		select {
		case c := <-u.accepted:
			c.Close()
		default:
		}
	})
	return u
}

func (u *heldUpstream) port() int { return u.ln.Addr().(*net.TCPAddr).Port }

// waitAccepted waits for the tunnel to reach the upstream and returns the
// upstream's end, putting it back for cleanup.
func (u *heldUpstream) waitAccepted(t *testing.T) {
	t.Helper()
	select {
	case c := <-u.accepted:
		u.accepted <- c
	case <-time.After(5 * time.Second):
		t.Fatal("the tunnel never reached the upstream")
	}
}

// closeWithin runs closeFn and fails the test if it has not returned
// within 10s.
func closeWithin(t *testing.T, what string, closeFn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		closeFn()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not return", what)
	}
}

// expectClosed fails the test unless reading c ends (EOF or a reset)
// within 5s: the far end closed it.
func expectClosed(t *testing.T, what string, c net.Conn) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err := c.Read(make([]byte, 1))
	var ne net.Error
	if err == nil || (errors.As(err, &ne) && ne.Timeout()) {
		t.Fatalf("%s is still open after Close (read: %v)", what, err)
	}
}

// expectNoGoroutine fails the test if a goroutine whose stack mentions fn
// is still running 2s after Close returned.
func expectNoGoroutine(t *testing.T, fn string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		buf := make([]byte, 1<<20)
		stacks := string(buf[:runtime.Stack(buf, true)])
		if !strings.Contains(stacks, fn) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("a goroutine in %s outlived Close", fn)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestProxyCloseEndsConnectTunnels: http.Server.Close does not touch
// hijacked connections, so a CONNECT tunnel open when the proxy closed
// kept both its sockets and both its goroutines until one far end hung
// up. Close must end the tunnel: the client and the upstream both see the
// connection close, and no tunnel goroutine is left.
func TestProxyCloseEndsConnectTunnels(t *testing.T) {
	up := newHeldUpstream(t)
	target := fmt.Sprintf("127.0.0.1:%d", up.port())
	p := NewProxy([]string{target}, nil, false)
	p.LookupIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			p.Close()
		}
	}()

	client, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", p.Port()))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	auth := base64.StdEncoding.EncodeToString([]byte(p.Userinfo()))
	fmt.Fprintf(client, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n", target, target, auth)
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	status, err := bufio.NewReader(client).ReadString('\n')
	if err != nil || !strings.Contains(status, " 200 ") {
		t.Fatalf("CONNECT: %q, %v", status, err)
	}
	up.waitAccepted(t)

	closeWithin(t, "Proxy.Close", func() { _ = p.Close() })
	closed = true
	expectClosed(t, "the client side of the tunnel", client)
	select {
	case <-up.eof:
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream side of the tunnel is still open after Close")
	}
	expectNoGoroutine(t, "sandbox.(*Proxy).serveConnect")
}

// TestManagerCloseEndsBridgeRelays: Manager.Close closed each Linux
// bridge's listener, which stops new connections but leaves accepted
// relays copying until a far end hangs up. Close must end them too.
func TestManagerCloseEndsBridgeRelays(t *testing.T) {
	up := newHeldUpstream(t)
	// A short directory: a Unix socket path is limited to ~104 bytes.
	dir, err := os.MkdirTemp("", "kb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	m := New(Config{}, Options{Cwd: dir, Home: dir})
	closed := false
	defer func() {
		if !closed {
			m.Close()
		}
	}()
	path, err := m.bridge(up.port(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, filepath.Clean(dir)) {
		t.Fatalf("bridge socket %s is outside %s", path, dir)
	}
	client, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	up.waitAccepted(t)

	closeWithin(t, "Manager.Close", m.Close)
	closed = true
	expectClosed(t, "the sandbox side of the relay", client)
	select {
	case <-up.eof:
	case <-time.After(5 * time.Second):
		t.Fatal("the proxy side of the relay is still open after Close")
	}
	expectNoGoroutine(t, "sandbox.relay")
	expectNoGoroutine(t, "sandbox.(*Manager).bridge")
}

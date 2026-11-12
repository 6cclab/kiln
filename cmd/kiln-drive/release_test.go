package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/testkit/faux"
)

// TestReleaseSendsToTheFauxServer: RELEASE <name> frees a request held at a
// faux `hold: <name>` step, and refuses without a faux address.
func TestReleaseSendsToTheFauxServer(t *testing.T) {
	srv, err := faux.New(faux.Options{ScriptYAML: "steps:\n  - hold: gate\n  - text: ok\n"})
	if err != nil {
		t.Fatal(err)
	}
	addr, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	done := make(chan error, 1)
	go func() {
		resp, err := http.Post("http://"+addr+"/v1/messages", "application/json",
			strings.NewReader(`{"model":"faux-1","messages":[{"role":"user","content":"hi"}]}`))
		if err == nil {
			_, err = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		done <- err
	}()
	for srv.Held("gate") == 0 {
		time.Sleep(2 * time.Millisecond)
	}

	d := &driver{fauxAddr: addr}
	if err := d.cmdRelease("gate"); err != nil {
		t.Fatalf("RELEASE gate: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("held request failed after release: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("RELEASE did not free the held request")
	}
	if err := (&driver{}).cmdRelease("gate"); err == nil {
		t.Error("RELEASE without a faux address succeeded, want an error")
	}
	if err := d.cmdRelease(""); err == nil {
		t.Error("RELEASE without a name succeeded, want an error")
	}
}

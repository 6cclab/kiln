package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var stubBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "harness-drive-stub-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "harness-drive_test: mkdtemp:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	stubBinary = filepath.Join(dir, "stubtui")
	cmd := exec.Command("go", "build", "-o", stubBinary, "github.com/andrepato/harness/internal/testkit/stubtui/cmd/stubtui")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "harness-drive_test: build stub:", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// TestStubSmoke pipes testdata/drive/stub-smoke.txt through the driver
// against the stub binary, one command at a time, waiting for each
// command's "." sync line before sending the next, and fails on the first
// "error: ..." reply.
func TestStubSmoke(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "testdata", "drive", "stub-smoke.txt"))
	if err != nil {
		t.Fatalf("read script: %v", err)
	}

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	done := make(chan int, 1)
	go func() {
		code := run([]string{"--bin", stubBinary, "--cols", "40", "--rows", "10"}, stdinR, stdoutW)
		_ = stdoutW.Close()
		done <- code
	}()

	out := bufio.NewScanner(stdoutR)
	readReply := func() []string {
		var lines []string
		for out.Scan() {
			line := out.Text()
			if line == "." {
				return lines
			}
			lines = append(lines, line)
		}
		return lines
	}

	lines := strings.Split(strings.TrimRight(string(script), "\n"), "\n")
	for _, cmdLine := range lines {
		if strings.TrimSpace(cmdLine) == "" {
			continue
		}
		if _, err := fmt.Fprintln(stdinW, cmdLine); err != nil {
			t.Fatalf("write command %q: %v", cmdLine, err)
		}
		reply := readReply()
		for _, r := range reply {
			if strings.HasPrefix(r, "error:") {
				t.Fatalf("command %q failed: %s", cmdLine, r)
			}
		}
		t.Logf("%s -> %v", cmdLine, reply)
	}

	_ = stdinW.Close()

	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("harness-drive exited with code %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("harness-drive did not exit after EXIT command")
	}
}

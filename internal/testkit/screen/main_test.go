package screen_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// stubBinary is the path to the compiled internal/testkit/stubtui binary,
// built once in TestMain into a temp directory.
var stubBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "harness-screen-stub-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "screen_test: mkdtemp:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	stubBinary = filepath.Join(dir, "stubtui")
	cmd := exec.Command("go", "build", "-o", stubBinary, "github.com/andrepato/harness/internal/testkit/stubtui/cmd/stubtui")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "screen_test: build stub:", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

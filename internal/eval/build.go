package eval

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// KilnVersion runs "<bin> --version" and returns its trimmed stdout.
func KilnVersion(bin string) (string, error) {
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("eval: %s --version: %w", bin, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// DefaultKilnBin returns "./bin/kiln" if it exists, else "".
func DefaultKilnBin() string {
	const path = "bin/kiln"
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return path
	}
	return ""
}

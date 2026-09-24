// Command faux runs the scripted model server from a shell so a person, an
// agent or harness-drive can point the harness at it. Usage:
//
//	faux [-addr host:port] script.yaml
//
// It prints the listening address on the first line of stdout.
package main

import (
	"fmt"
	"os"

	"github.com/andrepato/harness/internal/testkit/faux"
)

func main() {
	if err := faux.Main(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "faux:", err)
		os.Exit(1)
	}
}

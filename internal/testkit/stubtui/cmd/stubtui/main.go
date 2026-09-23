// Command stubtui builds the phase 0 stub program used by
// internal/testkit/screen and cmd/harness-drive tests.
package main

import (
	"os"

	"github.com/andrepato/harness/internal/testkit/stubtui"
)

func main() {
	os.Exit(stubtui.Run(os.Args[1:]))
}

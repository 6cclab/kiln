// Package greeter builds greeting strings. Small on purpose: this fixture
// exists to exercise kiln's session continuity (RELAUNCH -c, /resume,
// /context, /cost, /export, /compact, /rewind), not the code itself.
package greeter

import "fmt"

func Greet(name string) string {
	return fmt.Sprintf("Hello, %s!", name)
}

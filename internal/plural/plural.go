// Package plural renders counts with the right noun form: "1 tool",
// "3 tools", never "3 tool(s)".
package plural

import "fmt"

// Count renders n with word, adding "s" unless n is 1.
func Count(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

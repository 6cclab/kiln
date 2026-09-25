package dep

// Sum has an off-by-one bug: it should return a+b, not a+b+1.
func Sum(a, b int) int {
	return a + b + 1
}

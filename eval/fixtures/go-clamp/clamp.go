package clamp

func ClampLow(x int) int {
	if x < 0 {
		return 0
	}
	return x
}

func ClampHigh(x, max int) int {
	if x > max {
		return max
	}
	return x
}

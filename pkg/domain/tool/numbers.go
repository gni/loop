package tool

import (
	"fmt"
	"math"
)

// IntegerArgument validates JSON numeric options before converting to int.
// Zero remains available for omitted options and their existing defaults.
func IntegerArgument(name string, value float64) (int, error) {
	maximum := min(float64(^uint(0)>>1), float64(1<<53-1))
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || math.Trunc(value) != value || value > maximum {
		return 0, fmt.Errorf("%s must be a nonnegative integer no greater than %.0f", name, maximum)
	}
	return int(value), nil
}

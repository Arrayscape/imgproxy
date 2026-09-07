package wix

import "math"

// The Wix geometry rules were derived against a Python reference, and two of
// Python's arithmetic conventions differ from Go's in ways that change results
// on the exact values this package sees:
//
//   - Python's `//` floors; Go's `/` truncates toward zero. They disagree on
//     negatives, and Drop's operand is negative by construction.
//   - The reference rounds with `math.floor(v + 0.5)`, which is half-UP.
//     Go's math.Round is half-away-from-zero. They disagree on negatives, and
//     FusedCrop rounds values that can be negative after subtraction.
//
// Every integer operation in this package goes through these helpers so the
// convention is explicit at each call site. Never use imath.* here: it rounds
// half-away-from-zero throughout, which is what imgproxy's own pipeline wants
// and the opposite of what these rules need.

// floorF is math.Floor as an int.
func floorF(f float64) int { return int(math.Floor(f)) }

// ceilF is math.Ceil as an int.
func ceilF(f float64) int { return int(math.Ceil(f)) }

// roundHalfUp matches the reference's `math.floor(v + 0.5)`. This is NOT
// math.Round: on a negative exact half, roundHalfUp(-2.5) is -2 where
// math.Round(-2.5) is -3.
func roundHalfUp(f float64) int { return int(math.Floor(f + 0.5)) }

// floorDiv matches Python's `//` on ints: it floors rather than truncating, so
// floorDiv(-3, 2) is -2 where Go's -3/2 is -1.
func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// clamp constrains v to [lo, hi]. If hi < lo the low bound wins, which is what
// the focal-point rule needs when the crop window fills the axis exactly and
// the slack is zero.
func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

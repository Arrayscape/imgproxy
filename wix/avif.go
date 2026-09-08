package wix

import "sort"

// AVIF quantizer selection. OP-SPEC §7.5.

// AVIFDefaultQuality is what a url with no q_ segment behaves as.
const AVIFDefaultQuality = 90

// AVIFAlphaQuantizers are fixed regardless of q: base_q_idx floors at 108 and
// ceils at 140 whatever q says, and 108 and 140 are entries 27 and 35 of
// libaom's quantizer_to_qindex table.
var AVIFAlphaQuantizers = [2]int{27, 35}

// AVIF encoder constants that are not derived from the url.
const (
	AVIFSpeed = 9

	// AVIFThreads must be >= 2. libavif sets AV1E_SET_ROW_MT only when
	// maxThreads > 1, and libaom's row-MT encode is a DIFFERENT bitstream. The
	// count itself is irrelevant -- 2, 3, 4, 8, 16 and 32 give one identical
	// file -- so this is tuned for throughput, never for output.
	AVIFThreads = 4
)

// avifQMap maps a url q to libavif's minQuantizer.
//
// Measured by reading base_q_idx off renditions small enough that libaom's rate
// control sits on the floor of the range. base_q_idx is 4x these for every
// entry, i.e. they are indices into libaom's quantizer_to_qindex table.
//
// The curve is NOT linear and no closed form fits all 28 measured points, so
// this is a table rather than a formula.
var avifQMap = map[int]int{
	0: 56, 1: 55, 2: 55, 3: 54, 5: 53, 10: 51, 15: 48, 20: 46, 25: 44,
	30: 41, 35: 39, 40: 37, 45: 34, 50: 32, 55: 29, 60: 28, 65: 25,
	70: 22, 75: 20, 80: 18, 85: 16, 88: 14, 90: 13, 92: 12, 95: 10,
	97: 10, 99: 9, 100: 9,
}

var avifQKeys = func() []int {
	ks := make([]int, 0, len(avifQMap))
	for k := range avifQMap {
		ks = append(ks, k)
	}
	sort.Ints(ks)
	return ks
}()

// AVIFQuantizers returns (minQuantizer, maxQuantizer) for a url q.
//
// The span is always 8. maxQuantizer is in fact INERT -- every value from minQ
// to 63 produces identical bytes -- so minQ+8 is an observation about what Wix
// sends rather than a requirement, and it is reproduced for exactness rather
// than effect.
//
// exact reports whether q was one of the 28 measured points. For anything else
// the nearest measured point is used, which is an approximation: the curve has
// no closed form, so an unmeasured q cannot be derived, only guessed at. No
// production url uses one.
func AVIFQuantizers(q int) (minQ, maxQ int, exact bool) {
	if v, ok := avifQMap[q]; ok {
		return v, v + 8, true
	}
	near := avifQKeys[0]
	for _, k := range avifQKeys {
		if abs(k-q) < abs(near-q) {
			near = k
		}
	}
	v := avifQMap[near]
	return v, v + 8, false
}

func abs(i int) int {
	if i < 0 {
		return -i
	}
	return i
}

// AVIFQuality is the url q the AVIF quantizers are selected from: the last
// segment's q_N, or 90 when no segment carries one.
func (e Encoding) AVIFQuality() int {
	if e.hasLastQ {
		return e.lastQ
	}
	return AVIFDefaultQuality
}

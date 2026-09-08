package wix

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// JPEG container fix-ups. OP-SPEC §8.5.
//
// The CDN emits exactly
//
//	SOI  APP1(Exif)  [APP2(ICC_PROFILE)]  DQT ... EOI
//
// with no APP0 JFIF and no trailing bytes. The coded stream from the first DQT
// to EOI is already exact; only the header needs fixing.
//
// This is the largest metadata leak of any output format if skipped: libvips
// forwards the master's full camera EXIF -- 6429 bytes including an embedded
// JPEG thumbnail on one measured master -- plus 41720 bytes of XMP.

// ErrNotJPEG is returned when the input does not start with SOI.
var ErrNotJPEG = errors.New("wix: not a JPEG")

const (
	jpegSOI  = 0xD8
	jpegEOI  = 0xD9
	jpegAPP0 = 0xE0
	jpegAPP1 = 0xE1
	jpegAPP2 = 0xE2
	jpegSOS  = 0xDA
)

// jpegSegment is one marker segment. Data excludes the 2-byte length field.
type jpegSegment struct {
	Marker byte
	Data   []byte
}

// splitJPEGHeader walks marker segments up to the first non-APPn marker, and
// returns the remainder (the coded stream) untouched.
func splitJPEGHeader(b []byte) (segs []jpegSegment, rest []byte, err error) {
	if len(b) < 2 || b[0] != 0xFF || b[1] != jpegSOI {
		return nil, nil, ErrNotJPEG
	}

	off := 2
	for off+4 <= len(b) {
		if b[off] != 0xFF {
			return nil, nil, fmt.Errorf("wix: expected a JPEG marker at %d", off)
		}
		marker := b[off+1]

		// Everything from the first non-APPn marker onward is the coded
		// stream, which is already byte-exact and must not be touched.
		if marker < jpegAPP0 || marker > 0xEF {
			return segs, b[off:], nil
		}

		n := int(binary.BigEndian.Uint16(b[off+2 : off+4]))
		if n < 2 || off+2+n > len(b) {
			return nil, nil, fmt.Errorf("wix: truncated APP%d segment at %d", marker-jpegAPP0, off)
		}
		segs = append(segs, jpegSegment{
			Marker: marker,
			Data:   append([]byte(nil), b[off+4:off+2+n]...),
		})
		off += 2 + n
		if marker == jpegSOS {
			break
		}
	}
	return nil, nil, errors.New("wix: JPEG has no coded stream")
}

// isICCSegment reports the APP2 ICC_PROFILE form.
func isICCSegment(s jpegSegment) bool {
	return s.Marker == jpegAPP2 && bytes.HasPrefix(s.Data, []byte("ICC_PROFILE\x00"))
}

// jfifDensity reads a JFIF APP0's density, converted to pixels per metre.
//
// §8.5: a JPEG has no pHYs, so §8.3 step 3 reads the MASTER's JFIF density
// instead. Unit 2 (dots per cm) with density 28 gives 2800 px/m and 71119/1000,
// exactly as the pHYs round-trip would; falling back to the 2834 default gives
// 71983 and misses by four bytes.
func jfifDensity(masterHead []byte) (uint32, bool) {
	i := bytes.Index(masterHead, []byte("JFIF\x00"))
	if i < 0 || i+12 > len(masterHead) {
		return 0, false
	}
	unit := masterHead[i+7]
	x := binary.BigEndian.Uint16(masterHead[i+8 : i+10])
	if x == 0 {
		return 0, false
	}
	switch unit {
	case 1: // dots per inch
		return uint32(float64(x)/0.0254 + 0.5), true
	case 2: // dots per cm
		return uint32(x) * 100, true
	}
	return 0, false
}

// FinalizeJPEG rewrites a libvips JPEG into the CDN's container form.
//
// masterHead is the first HeadSize bytes of the SOURCE image, feeding both the
// §8.3 resolution precedence and its JPEG-specific step 3.
func FinalizeJPEG(jpeg []byte, masterHead []byte, width, height int) ([]byte, error) {
	segs, rest, err := splitJPEGHeader(jpeg)
	if err != nil {
		return nil, err
	}

	// libvips forwards the master's own EXIF here; the block is rebuilt.
	var srcEXIF []byte
	for _, s := range segs {
		if s.Marker == jpegAPP1 && bytes.HasPrefix(s.Data, []byte("Exif\x00\x00")) {
			srcEXIF = s.Data[6:]
			break
		}
	}

	pxPerM := uint32(defaultPxPerM)
	if d, ok := jfifDensity(masterHead); ok {
		pxPerM = d
	}

	num, den, have := MasterResolution(masterHead)
	if !have && !JFIFIsMetric(masterHead) {
		num, den, have = ReadEXIFResolution(srcEXIF)
	}

	var out bytes.Buffer
	out.Write([]byte{0xFF, jpegSOI})

	write := func(marker byte, data []byte) {
		out.Write([]byte{0xFF, marker})
		var n [2]byte
		binary.BigEndian.PutUint16(n[:], uint16(len(data)+2))
		out.Write(n[:])
		out.Write(data)
	}

	// 1. APP1 is replaced with the canonical block.
	write(jpegAPP1, append([]byte("Exif\x00\x00"),
		CanonicalEXIF(width, height, pxPerM, num, den, have)...))

	// 2 & 3. Keep APP2 ICC verbatim; drop everything else -- the XMP APP1, the
	// APP0 JFIF, and any other APPn.
	for _, s := range segs {
		if isICCSegment(s) {
			write(s.Marker, s.Data)
		}
	}

	// The coded stream is already exact.
	out.Write(rest)
	return out.Bytes(), nil
}

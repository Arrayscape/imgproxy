package wix

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

// PNG container fix-ups. OP-SPEC §8.
//
// libvips output differs from the CDN in two ways that have nothing to do with
// pixels -- the IDAT streams already match:
//
//  1. libvips writes a zTXt chunk. The CDN does not.
//  2. libvips forwards the master's EXIF. The CDN replaces it with a fixed
//     11-tag whitelist.
//
// (2) is a SECURITY CONTROL, not a formatting quirk: the whitelist carries no
// GPS, camera make or model, timestamps, software or serial numbers, and
// libvips forwards source metadata by default. An implementation that skips
// this leaks whatever the uploader's camera recorded.

// CanonicalEXIFLen is the fixed size of the synthesised EXIF block.
const CanonicalEXIFLen = 180

// defaultPxPerM is assumed when the rendition carries no pHYs chunk.
const defaultPxPerM = 2834

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// ErrNotPNG is returned when the input does not begin with the PNG signature.
var ErrNotPNG = errors.New("wix: not a PNG")

// Chunk is one PNG chunk. CRCs are recomputed on write.
type Chunk struct {
	Type []byte
	Data []byte
}

// SplitPNG walks the chunk stream.
func SplitPNG(b []byte) ([]Chunk, error) {
	if len(b) < 8 || !bytes.HasPrefix(b, pngMagic) {
		return nil, ErrNotPNG
	}
	var chunks []Chunk
	off := 8
	for off+8 <= len(b) {
		ln := int(binary.BigEndian.Uint32(b[off : off+4]))
		typ := b[off+4 : off+8]
		if ln < 0 || off+12+ln > len(b) {
			return nil, fmt.Errorf("wix: truncated PNG chunk %q at %d", typ, off)
		}
		chunks = append(chunks, Chunk{
			Type: append([]byte(nil), typ...),
			Data: append([]byte(nil), b[off+8:off+8+ln]...),
		})
		if string(typ) == "IEND" {
			break
		}
		off += 12 + ln
	}
	return chunks, nil
}

// JoinPNG reassembles a chunk stream, recomputing every CRC.
func JoinPNG(cs []Chunk) []byte {
	var out bytes.Buffer
	out.Write(pngMagic)
	for _, c := range cs {
		var hdr [4]byte
		binary.BigEndian.PutUint32(hdr[:], uint32(len(c.Data)))
		out.Write(hdr[:])
		out.Write(c.Type)
		out.Write(c.Data)

		crc := crc32.NewIEEE()
		crc.Write(c.Type)
		crc.Write(c.Data)
		var sum [4]byte
		binary.BigEndian.PutUint32(sum[:], crc.Sum32())
		out.Write(sum[:])
	}
	return out.Bytes()
}

// CanonicalEXIF builds the CDN's 180-byte eXIf payload for an output of this
// size. Little-endian, constant except the resolution and the pixel dimensions.
//
//	IFD0  Orientation=1, XResolution, YResolution, ResolutionUnit=2 (inch),
//	      YCbCrPositioning=1, ExifIFD->102
//	Exif  ExifVersion="0210", ComponentsConfiguration=01 02 03 00,
//	      FlashpixVersion="0100", ColorSpace=65535 (uncalibrated),
//	      PixelXDimension, PixelYDimension
//
// The block is SYNTHESISED: masters carrying no EXIF at all still come back
// from the CDN with the full set.
func CanonicalEXIF(width, height int, pxPerM uint32, num, den uint32, haveRes bool) []byte {
	if !haveRes {
		// Derived from pHYs, TRUNCATED rather than rounded: 2834 px/m gives
		// 71983/1000 because 2834*0.0254*1000 = 71983.6, and rounding to 71984
		// misses by one.
		num, den = uint32(float64(pxPerM)*0.0254*1000), 1000
	}

	const (
		ifd0At     = 8
		ifd0Len    = 2 + 6*12 + 4 // 78
		rationalAt = ifd0At + ifd0Len
		exifAt     = rationalAt + 16
	)

	var b bytes.Buffer
	le := binary.LittleEndian

	b.WriteString("II")
	binary.Write(&b, le, uint16(42))
	binary.Write(&b, le, uint32(ifd0At))

	// A SHORT stored inline is the value followed by a zero SHORT, not a
	// zero-padded 4-byte field. That distinction is load-bearing.
	short := func(tag uint16, v uint16) {
		binary.Write(&b, le, tag)
		binary.Write(&b, le, uint16(3))
		binary.Write(&b, le, uint32(1))
		binary.Write(&b, le, v)
		binary.Write(&b, le, uint16(0))
	}
	long := func(tag uint16, v uint32) {
		binary.Write(&b, le, tag)
		binary.Write(&b, le, uint16(4))
		binary.Write(&b, le, uint32(1))
		binary.Write(&b, le, v)
	}
	rational := func(tag uint16, at uint32) {
		binary.Write(&b, le, tag)
		binary.Write(&b, le, uint16(5))
		binary.Write(&b, le, uint32(1))
		binary.Write(&b, le, at)
	}
	undef4 := func(tag uint16, v []byte) {
		binary.Write(&b, le, tag)
		binary.Write(&b, le, uint16(7))
		binary.Write(&b, le, uint32(4))
		var p [4]byte
		copy(p[:], v)
		b.Write(p[:])
	}

	binary.Write(&b, le, uint16(6)) // IFD0 entry count
	short(274, 1)                   // Orientation = top-left
	rational(282, rationalAt)       // XResolution
	rational(283, rationalAt+8)     // YResolution
	short(296, 2)                   // ResolutionUnit = inch
	short(531, 1)                   // YCbCrPositioning = centered
	long(34665, exifAt)             // Exif IFD pointer
	binary.Write(&b, le, uint32(0)) // next IFD

	binary.Write(&b, le, num)
	binary.Write(&b, le, den)
	binary.Write(&b, le, num)
	binary.Write(&b, le, den)

	binary.Write(&b, le, uint16(6)) // Exif IFD entry count
	undef4(36864, []byte("0210"))   // ExifVersion
	undef4(37121, []byte{1, 2, 3, 0})
	undef4(40960, []byte("0100")) // FlashpixVersion
	short(40961, 65535)           // ColorSpace = uncalibrated
	long(40962, uint32(width))    // PixelXDimension
	long(40963, uint32(height))   // PixelYDimension
	binary.Write(&b, le, uint32(0))

	return b.Bytes()
}

// FinalizePNG rewrites a libvips PNG into the CDN's container form.
//
// masterHead is the first HeadSize bytes of the SOURCE image: the resolution
// rules read the master's own EXIF and detect a metric JFIF block, neither of
// which survives into the rendition.
func FinalizePNG(png []byte, masterHead []byte) ([]byte, error) {
	chunks, err := SplitPNG(png)
	if err != nil {
		return nil, err
	}

	var ihdr, phys, srcEXIF []byte
	for _, c := range chunks {
		switch string(c.Type) {
		case "IHDR":
			ihdr = c.Data
		case "pHYs":
			phys = c.Data
		case "eXIf":
			srcEXIF = c.Data
		}
	}
	if len(ihdr) < 8 {
		return nil, errors.New("wix: PNG has no usable IHDR")
	}
	width := int(binary.BigEndian.Uint32(ihdr[0:4]))
	height := int(binary.BigEndian.Uint32(ihdr[4:8]))

	pxPerM := uint32(defaultPxPerM)
	if len(phys) >= 4 {
		pxPerM = binary.BigEndian.Uint32(phys[0:4])
	}

	// Resolution precedence (§8.3), measured rather than reasoned. Dropping
	// step 2 costs 142 renditions, so it is NOT redundant.
	num, den, have := MasterResolution(masterHead)
	if !have && !JFIFIsMetric(masterHead) {
		num, den, have = ReadEXIFResolution(srcEXIF)
	}

	blob := CanonicalEXIF(width, height, pxPerM, num, den, have)

	out := make([]Chunk, 0, len(chunks)+1)
	sawEXIF := false
	for _, c := range chunks {
		switch string(c.Type) {
		case "zTXt":
			continue // libvips writes it; the CDN does not

		case "eXIf":
			// libvips 8.15.5 emits eXIf TWICE for a source that carries one --
			// once after IHDR and again after the IDATs. The CDN emits one, so
			// the first is replaced in place and any others are dropped.
			if sawEXIF {
				continue
			}
			sawEXIF = true
			out = append(out, Chunk{Type: c.Type, Data: blob})

		default:
			out = append(out, c)
		}
	}

	if !sawEXIF {
		// The CDN always emits eXIf; libvips omits it when the master had none.
		// It sits after IHDR -- and after iCCP when a profile is present --
		// which puts it before pHYs.
		at := 1
		for i, c := range out {
			if t := string(c.Type); t == "IHDR" || t == "iCCP" {
				at = i + 1
			}
		}
		out = append(out, Chunk{})
		copy(out[at+1:], out[at:])
		out[at] = Chunk{Type: []byte("eXIf"), Data: blob}
	}

	return JoinPNG(out), nil
}

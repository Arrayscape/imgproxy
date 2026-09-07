package wix

import (
	"bytes"
	"encoding/binary"
	"math"
)

// EXIF resolution rules for the PNG container fix-ups. OP-SPEC §8.3.

type byteOrder interface {
	Uint16([]byte) uint16
	Uint32([]byte) uint32
}

// tiffOrder returns the byte order of a TIFF/EXIF blob and whether it is valid.
func tiffOrder(b []byte) (byteOrder, bool) {
	if len(b) < 8 {
		return nil, false
	}
	switch {
	case bytes.HasPrefix(b, []byte("II")):
		return binary.LittleEndian, true
	case bytes.HasPrefix(b, []byte("MM")):
		return binary.BigEndian, true
	}
	return nil, false
}

// ifd0Entries iterates IFD0, calling fn with each (tag, type, valueOffset).
func ifd0Entries(b []byte, fn func(tag, typ uint16, at int)) bool {
	o, ok := tiffOrder(b)
	if !ok {
		return false
	}
	off := int(o.Uint32(b[4:8]))
	if off < 0 || off+2 > len(b) {
		return false
	}
	n := int(o.Uint16(b[off : off+2]))
	for i := range n {
		e := off + 2 + i*12
		if e+12 > len(b) {
			return false
		}
		fn(o.Uint16(b[e:e+2]), o.Uint16(b[e+2:e+4]), e+8)
	}
	return true
}

// ReadEXIFResolution pulls XResolution out of an existing eXIf payload,
// carrying its rational through unchanged. This is step 2 of the precedence:
// right for most masters, and dropping it costs 142 renditions.
func ReadEXIFResolution(exif []byte) (num, den uint32, ok bool) {
	o, valid := tiffOrder(exif)
	if !valid {
		return 0, 0, false
	}
	ifd0Entries(exif, func(tag, typ uint16, at int) {
		if tag != 282 || typ != 5 || ok {
			return
		}
		p := int(o.Uint32(exif[at : at+4]))
		if p+8 <= len(exif) {
			num, den, ok = o.Uint32(exif[p:p+4]), o.Uint32(exif[p+4:p+8]), true
		}
	})
	return num, den, ok
}

// MasterResolution is the XResolution rational the CDN carries for this master.
// Step 1 of the precedence, and it wins outright.
//
// The master's own EXIF XResolution is converted to dots-per-INCH (ResUnit 3
// means dots-per-cm, so multiply by 2.54) and emitted over a denominator of
// 1000: a master holding 216/1 comes back as 216000/1000, and 182/1 as
// 182000/1000. The value is preserved and the representation normalised.
//
// 72 dpi is the exception. It is the CDN's default and it emits the default's
// own rational, 72/1, whatever form the master used -- both 720000/10000 and
// 72/1 come back as 72/1.
//
// KNOWN RESIDUAL, deliberately not fixed: a master whose EXIF rational is
// unreduced comes back REDUCED rather than normalised. Reducing unconditionally
// breaks the commoner shape, so this stays wrong for that one master.
func MasterResolution(head []byte) (num, den uint32, ok bool) {
	blob := exifBlob(head)
	if blob == nil {
		return 0, 0, false
	}
	o, valid := tiffOrder(blob)
	if !valid {
		return 0, 0, false
	}

	var rnum, rden uint32
	var unit uint16
	ifd0Entries(blob, func(tag, typ uint16, at int) {
		switch {
		case tag == 282 && typ == 5:
			p := int(o.Uint32(blob[at : at+4]))
			if p+8 <= len(blob) {
				rnum, rden = o.Uint32(blob[p:p+4]), o.Uint32(blob[p+4:p+8])
			}
		case tag == 296 && typ == 3:
			unit = o.Uint16(blob[at : at+2])
		}
	})
	if rnum == 0 || rden == 0 {
		return 0, 0, false
	}

	dpi := float64(rnum) / float64(rden)
	if unit == 3 {
		dpi *= 2.54
	}
	if math.Abs(dpi-72.0) < 1e-9 {
		return 72, 1, true
	}
	return uint32(roundHalfUp(dpi * 1000)), 1000, true
}

// exifBlob locates the TIFF header inside a JPEG APP1 or a PNG eXIf chunk.
func exifBlob(head []byte) []byte {
	switch {
	case len(head) >= 2 && head[0] == 0xFF && head[1] == 0xD8:
		if i := bytes.Index(head, []byte("Exif\x00\x00")); i > 0 {
			return head[i+6:]
		}
	case bytes.HasPrefix(head, pngMagic):
		off := 8
		for off+8 <= len(head) {
			ln := int(binary.BigEndian.Uint32(head[off : off+4]))
			typ := string(head[off+4 : off+8])
			if typ == "eXIf" && off+8+ln <= len(head) {
				return head[off+8 : off+8+ln]
			}
			if typ == "IEND" || ln < 0 {
				return nil
			}
			off += 12 + ln
		}
	}
	return nil
}

// JFIFIsMetric reports a JPEG whose ONLY resolution is a dots-per-cm JFIF block.
//
// libvips forwards the JFIF density verbatim (28) where the CDN converts via
// the rendition's pHYs: 2800 px/m -> int(2800 * 0.0254 * 1000) = 71119, not
// 28 * 2.54 * 1000 = 71120. Detecting it lets the caller skip libvips' value
// and fall through to the pHYs derivation. This was the one genuine VALUE
// error in this path; every other difference was representation.
func JFIFIsMetric(head []byte) bool {
	if len(head) < 2 || head[0] != 0xFF || head[1] != 0xD8 {
		return false
	}
	if bytes.Index(head, []byte("Exif\x00\x00")) > 0 {
		return false
	}
	i := bytes.Index(head, []byte("JFIF\x00"))
	return i > 0 && i+7 < len(head) && head[i+7] == 2
}

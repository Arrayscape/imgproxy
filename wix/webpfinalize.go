package wix

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// WebP container fix-ups. OP-SPEC §8.4.
//
// The coded payload libvips produces is already exact; these steps are the
// difference between a payload that matches and a FILE that matches. Checking
// only the VP8/VP8L chunk reported WebP as finished while 22 of 54 files still
// differed.
//
// The CDN always emits exactly this chunk sequence:
//
//	RIFF WEBP  VP8X  [ICCP]  [ALPH]  VP8|VP8L  EXIF

// ErrNotWebP is returned when the input is not a RIFF/WEBP file.
var ErrNotWebP = errors.New("wix: not a WebP")

// RIFFChunk is one chunk of a RIFF container. Payloads are stored unpadded;
// the pad byte an odd-length chunk needs is added on write.
type RIFFChunk struct {
	FourCC string
	Data   []byte
}

// SplitWebP walks the chunk sequence of a RIFF/WEBP file.
func SplitWebP(b []byte) ([]RIFFChunk, error) {
	if len(b) < 12 || !bytes.Equal(b[0:4], []byte("RIFF")) || !bytes.Equal(b[8:12], []byte("WEBP")) {
		return nil, ErrNotWebP
	}

	var out []RIFFChunk
	off := 12
	for off+8 <= len(b) {
		fourcc := string(b[off : off+4])
		n := int(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		if n < 0 || off+8+n > len(b) {
			return nil, fmt.Errorf("wix: truncated WebP chunk %q at %d", fourcc, off)
		}
		out = append(out, RIFFChunk{
			FourCC: fourcc,
			Data:   append([]byte(nil), b[off+8:off+8+n]...),
		})
		off += 8 + n + (n & 1) // chunks are padded to an even length
	}
	return out, nil
}

// JoinWebP reassembles a RIFF/WEBP file.
func JoinWebP(cs []RIFFChunk) []byte {
	var body bytes.Buffer
	body.WriteString("WEBP")
	for _, c := range cs {
		var hdr [4]byte
		binary.LittleEndian.PutUint32(hdr[:], uint32(len(c.Data)))
		body.WriteString(c.FourCC)
		body.Write(hdr[:])
		body.Write(c.Data)
		if len(c.Data)%2 == 1 {
			body.WriteByte(0)
		}
	}

	var out bytes.Buffer
	out.WriteString("RIFF")
	var size [4]byte
	binary.LittleEndian.PutUint32(size[:], uint32(body.Len()))
	out.Write(size[:])
	out.Write(body.Bytes())
	return out.Bytes()
}

// VP8X flag bits. OP-SPEC §8.4 step 3.
const (
	vp8xICCP  = 0x20
	vp8xAlpha = 0x10
	vp8xEXIF  = 0x08
)

// buildVP8X synthesises the 10-byte VP8X payload.
//
// Canvas dimensions are stored minus one, as 24-bit little-endian.
func buildVP8X(flags byte, width, height int) []byte {
	b := make([]byte, 10)
	b[0] = flags
	// b[1:4] stay zero: reserved.
	put24 := func(dst []byte, v int) {
		dst[0] = byte(v)
		dst[1] = byte(v >> 8)
		dst[2] = byte(v >> 16)
	}
	put24(b[4:7], width-1)
	put24(b[7:10], height-1)
	return b
}

// codedSize reads the image dimensions out of the VP8 or VP8L bitstream.
//
// The canvas size VP8X advertises is the CODED size, so it is read from the
// bitstream rather than assumed to equal the requested output size.
func codedSize(cs []RIFFChunk) (int, int, bool) {
	for _, c := range cs {
		switch c.FourCC {
		case "VP8 ":
			// Keyframe: 3-byte frame tag, 3-byte start code, then 14-bit
			// width and 14-bit height.
			d := c.Data
			if len(d) < 10 || d[3] != 0x9d || d[4] != 0x01 || d[5] != 0x2a {
				return 0, 0, false
			}
			w := int(binary.LittleEndian.Uint16(d[6:8]) & 0x3FFF)
			h := int(binary.LittleEndian.Uint16(d[8:10]) & 0x3FFF)
			return w, h, w > 0 && h > 0

		case "VP8L":
			// 1-byte signature, then 14 bits width-1 and 14 bits height-1.
			d := c.Data
			if len(d) < 5 || d[0] != 0x2f {
				return 0, 0, false
			}
			bits := binary.LittleEndian.Uint32(d[1:5])
			w := int(bits&0x3FFF) + 1
			h := int((bits>>14)&0x3FFF) + 1
			return w, h, true
		}
	}
	return 0, 0, false
}

// FinalizeWebP rewrites a libvips WebP into the CDN's container form.
//
// masterHead is the first HeadSize bytes of the SOURCE image and pxPerM is the
// rendition's resolution, both feeding the §8.3 precedence — WebP carries no
// pHYs chunk, so the resolution has to come from the image rather than be read
// back out of the file.
func FinalizeWebP(webp []byte, masterHead []byte, pxPerM uint32) ([]byte, error) {
	chunks, err := SplitWebP(webp)
	if err != nil {
		return nil, err
	}

	width, height, ok := codedSize(chunks)
	if !ok {
		return nil, errors.New("wix: WebP has no readable VP8/VP8L bitstream")
	}

	// Resolution precedence, §8.3. libvips forwards the master's own EXIF here,
	// which is big-endian and drops YCbCrPositioning when the master had none,
	// so the block is rebuilt rather than edited.
	var srcEXIF []byte
	for _, c := range chunks {
		if c.FourCC == "EXIF" {
			srcEXIF = bytes.TrimPrefix(c.Data, []byte("Exif\x00\x00"))
		}
	}
	num, den, have := MasterResolution(masterHead)
	if !have && !JFIFIsMetric(masterHead) {
		num, den, have = ReadEXIFResolution(srcEXIF)
	}
	exif := append([]byte("Exif\x00\x00"),
		CanonicalEXIF(width, height, pxPerM, num, den, have)...)

	var iccp, alph, image *RIFFChunk
	for i := range chunks {
		switch chunks[i].FourCC {
		case "ICCP":
			iccp = &chunks[i]
		case "ALPH":
			alph = &chunks[i]
		case "VP8 ", "VP8L":
			image = &chunks[i]
		}
		// XMP is dropped: libvips sometimes appends one, the CDN never does.
	}
	if image == nil {
		return nil, errors.New("wix: WebP has no VP8/VP8L chunk")
	}

	// VP8X is always written, synthesised when libvips omitted it -- which it
	// does whenever there is no metadata to carry.
	flags := byte(vp8xEXIF)
	if iccp != nil {
		flags |= vp8xICCP
	}
	if alph != nil || hasAlphaBit(image) {
		flags |= vp8xAlpha
	}

	out := []RIFFChunk{{FourCC: "VP8X", Data: buildVP8X(flags, width, height)}}
	if iccp != nil {
		out = append(out, *iccp)
	}
	if alph != nil {
		out = append(out, *alph)
	}
	out = append(out, *image)
	out = append(out, RIFFChunk{FourCC: "EXIF", Data: exif})

	return JoinWebP(out), nil
}

// hasAlphaBit reports alpha carried inside a VP8L stream, which needs no
// separate ALPH chunk. Bit 3 of the byte after the 14+14 size field.
func hasAlphaBit(image *RIFFChunk) bool {
	if image.FourCC != "VP8L" || len(image.Data) < 5 {
		return false
	}
	return binary.LittleEndian.Uint32(image.Data[1:5])&(1<<28) != 0
}

// Package wixavif encodes AVIF the way the Wix CDN does.
//
// It is deliberately NOT part of the vips package: the CDN's AVIFs carry `hdlr`
// name "libavif" and libavif's exact box layout, so libheif output would be the
// wrong encoder regardless of settings. OP-SPEC.md §7.5.
//
// libavif and libaom are linked statically from a private prefix. The base
// image already ships a shared libaom for libheif, at a different version and
// with the same soname, so a private static pair is the only way to have both.
package wixavif

/*
#cgo CFLAGS: -I/opt/wix-avif/include
// libsharpyuv comes from the base's libwebp and is referenced by libavif's
// reformat_libsharpyuv.c. It is only invoked for explicit sharp-YUV chroma
// downsampling, which this encoder does not request -- the default conversion
// is what OP-SPEC §7.5 measured -- but it still has to be on the link line.
#cgo LDFLAGS: -L/opt/wix-avif/lib -L/opt/imgproxy/lib -lavif -laom -lsharpyuv -lm -lpthread -lstdc++
#include <stdlib.h>
#include "encode.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

// Params are the encoder settings for one rendition.
type Params struct {
	MinQ, MaxQ           int
	MinAlphaQ, MaxAlphaQ int
	Speed, Threads       int
}

// Encode compresses width*height RGBA pixels to AVIF.
//
// The input must already be flattened to 8-bit RGBA; nothing here decodes
// pixels, so the rendering pipeline stays the single source of them.
func Encode(rgba []byte, width, height int, p Params) ([]byte, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("wixavif: bad size %dx%d", width, height)
	}
	if want := width * height * 4; len(rgba) != want {
		return nil, fmt.Errorf(
			"wixavif: got %d bytes, want %d for %dx%d RGBA", len(rgba), want, width, height)
	}

	var (
		out    *C.uint8_t
		outLen C.size_t
		cerr   *C.char
	)
	rc := C.wixavif_encode(
		(*C.uint8_t)(unsafe.Pointer(&rgba[0])),
		C.int(width), C.int(height),
		C.int(p.MinQ), C.int(p.MaxQ),
		C.int(p.MinAlphaQ), C.int(p.MaxAlphaQ),
		C.int(p.Speed), C.int(p.Threads),
		&out, &outLen, &cerr)
	if rc != 0 {
		msg := "unknown error"
		if cerr != nil {
			msg = C.GoString(cerr)
		}
		return nil, errors.New("wixavif: " + msg)
	}
	defer C.wixavif_free(out)

	return C.GoBytes(unsafe.Pointer(out), C.int(outLen)), nil
}

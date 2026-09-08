package vips

/*
#cgo pkg-config: vips
#cgo CFLAGS: -O3
#cgo LDFLAGS: -lm
#include "source.h"
#include "vips.h"
*/
import "C"
import (
	"errors"
	"io"
	"runtime/cgo"
	"unsafe"
)

// newVipsSource creates a new VipsAsyncSource from an io.ReadSeeker.
func newVipsImgproxySource(r io.ReadSeeker) *C.VipsImgproxySource {
	handler := cgo.NewHandle(r)
	return C.vips_new_imgproxy_source(C.uintptr_t(handler))
}

//export closeImgproxyReader
func closeImgproxyReader(handle C.uintptr_t) {
	h := cgo.Handle(handle)
	h.Delete()
}

// calls seek() on the async reader via it's handle from the C side
//
//export imgproxyReaderSeek
func imgproxyReaderSeek(handle C.uintptr_t, offset C.int64_t, whence int) C.int64_t {
	h := cgo.Handle(handle)
	r, ok := h.Value().(io.ReadSeeker)
	if !ok {
		vipsError("imgproxyReaderSeek", "failed to cast handle to io.ReadSeeker")
		return -1
	}

	pos, err := r.Seek(int64(offset), whence)
	if err != nil {
		vipsError("imgproxyReaderSeek", "failed to seek: %v", err)
		return -1
	}

	return C.int64_t(pos)
}

// calls read() on the async reader via it's handle from the C side
//
//export imgproxyReaderRead
func imgproxyReaderRead(handle C.uintptr_t, pointer unsafe.Pointer, size C.int64_t) C.int64_t {
	h := cgo.Handle(handle)
	r, ok := h.Value().(io.ReadSeeker)
	if !ok {
		vipsError("imgproxyReaderRead", "invalid reader handle")
		return -1
	}

	buf := unsafe.Slice((*byte)(pointer), size)
	n, err := r.Read(buf)

	// Bytes first, error second. io.Reader explicitly permits returning
	// n > 0 together with io.EOF (or any error) in a single call, and the
	// caller must consume those bytes before considering the error -- the
	// error surfaces again on the next Read, which returns 0.
	//
	// Returning 0 here whenever err was io.EOF discarded the bytes and told
	// libvips the stream had ended early. Downstream that looks like a
	// truncated image rather than a read failure: libtiff, for instance,
	// reports "Read error at scanline N; got X bytes, expected Y".
	if n > 0 {
		return C.int64_t(n)
	}

	if errors.Is(err, io.EOF) {
		return 0
	}
	if err != nil {
		vipsError("imgproxyReaderRead", "error reading from imgproxy source: %v", err)
		return -1
	}

	return 0
}

// Helpers so tests can drive the cgo callbacks: a _test.go file cannot import
// "C" itself.
func C_uintptr(h cgo.Handle) C.uintptr_t { return C.uintptr_t(h) }
func C_int64(n int) C.int64_t            { return C.int64_t(n) }

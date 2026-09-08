package vips

import (
	"io"
	"runtime/cgo"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"
)

// eofWithDataReader returns all of its data together with io.EOF in a single
// Read, which io.Reader explicitly permits:
//
//	"An instance of this general case is that a Reader returning a non-zero
//	 number of bytes at the end of the input stream may return either
//	 err == EOF or err == nil."
//
// Callers must consume n before considering the error.
type eofWithDataReader struct {
	data []byte
	done bool
}

func (r *eofWithDataReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	return copy(p, r.data), io.EOF
}

func (r *eofWithDataReader) Seek(int64, int) (int64, error) { return 0, nil }

// readViaCallback drives the cgo callback the way libvips does.
func readViaCallback(t *testing.T, r io.ReadSeeker, bufSize int) (int, []byte) {
	t.Helper()

	h := cgo.NewHandle(r)
	defer h.Delete()

	buf := make([]byte, bufSize)
	n := imgproxyReaderRead(
		C_uintptr(h), unsafe.Pointer(&buf[0]), C_int64(len(buf)))
	return int(n), buf
}

func TestReaderReadKeepsBytesReturnedWithEOF(t *testing.T) {
	// Regression: the callback used to return 0 whenever the error was io.EOF,
	// discarding bytes that had actually been read and telling libvips the
	// stream ended early. Downstream that looks like a truncated image, not a
	// read failure -- libtiff reports "Read error at scanline N".
	want := []byte("the bytes that must not be dropped")
	r := &eofWithDataReader{data: want}

	n, buf := readViaCallback(t, r, 64)

	require.Equal(t, len(want), n, "bytes returned alongside io.EOF must be kept")
	require.Equal(t, want, buf[:n])
}

func TestReaderReadReportsCleanEOF(t *testing.T) {
	// A reader with nothing left still signals end-of-stream as 0.
	r := &eofWithDataReader{data: nil, done: true}
	n, _ := readViaCallback(t, r, 16)
	require.Equal(t, 0, n)
}

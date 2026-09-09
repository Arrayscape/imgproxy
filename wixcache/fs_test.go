package wixcache

import (
	"image"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/imgproxy/imgproxy/v4/imagetype"
	"github.com/imgproxy/imgproxy/v4/wix"
)

func fsEntryOf(n int, size int) *Entry {
	return &Entry{
		Data:    make([]byte, size),
		Format:  imagetype.PNG,
		SrcRect: image.Rect(0, 0, 1000, 1000),
		Width:   n, Height: n,
	}
}

func TestFSSurvivesRestart(t *testing.T) {
	// The whole point of a disk cache: an in-process one is empty after every
	// restart, so derivation stays rarer than on the CDN.
	dir := t.TempDir()

	f, err := NewFS(dir, 1<<20)
	require.NoError(t, err)
	f.Put("mid", "aabbcc", fsEntryOf(400, 1024))
	f.PutDims("mid", 512, 392)

	reopened, err := NewFS(dir, 1<<20)
	require.NoError(t, err)

	got, ok := reopened.Get("aabbcc")
	require.True(t, ok, "the rendition must survive a restart")
	require.Len(t, got.Data, 1024)
	require.Equal(t, 400, got.Width)
	require.Equal(t, image.Rect(0, 0, 1000, 1000), got.SrcRect)

	w, h, ok := reopened.Dims("mid")
	require.True(t, ok)
	require.Equal(t, 512, w)
	require.Equal(t, 392, h)

	require.Equal(t, []*Entry{}, emptyIfNil(reopened.Ancestors("other")))
	require.Len(t, reopened.Ancestors("mid"), 1, "the master index is rebuilt too")
}

func emptyIfNil(e []*Entry) []*Entry {
	if e == nil {
		return []*Entry{}
	}
	return e
}

func TestFSPreservesEffectsAcrossRestart(t *testing.T) {
	// Effects must survive, or a sharpened rendition would be offered as a
	// resamplable ancestor after a restart -- silently wrong output.
	dir := t.TempDir()
	f, err := NewFS(dir, 1<<20)
	require.NoError(t, err)

	e := fsEntryOf(300, 512)
	e.Effects = wix.Effects{Blur: 3, USM: &wix.USM{Sigma: 0.66, Amount: 1, Threshold: 0.01}}
	f.Put("mid", "ddeeff", e)

	reopened, _ := NewFS(dir, 1<<20)
	got, ok := reopened.Get("ddeeff")
	require.True(t, ok)
	require.Equal(t, 3.0, got.Effects.Blur)
	require.NotNil(t, got.Effects.USM)
	require.Equal(t, 0.66, got.Effects.USM.Sigma)
	require.False(t, Usable(got, target(0, 0, 1000, 1000, 100, 100), DefaultMaxDepth, 1000, 1000),
		"an ancestor with effects baked in must stay unusable after a restart")
}

func TestFSEvictsLeastRecentlyUsed(t *testing.T) {
	dir := t.TempDir()
	f, err := NewFS(dir, 3000)
	require.NoError(t, err)

	f.Put("m", "aa1111", fsEntryOf(1, 1000))
	f.Put("m", "bb2222", fsEntryOf(2, 1000))
	f.Put("m", "cc3333", fsEntryOf(3, 1000))
	require.Equal(t, 3, f.Len())

	_, ok := f.Get("aa1111") // touch, so bb is now the oldest
	require.True(t, ok)

	f.Put("m", "dd4444", fsEntryOf(4, 1000))
	require.LessOrEqual(t, f.Bytes(), int64(3000))

	_, ok = f.Get("bb2222")
	require.False(t, ok, "least recently used is evicted")
	_, ok = f.Get("aa1111")
	require.True(t, ok, "the touched entry survives")
}

func TestFSShrinkingTheBudgetEvictsOnOpen(t *testing.T) {
	dir := t.TempDir()
	f, _ := NewFS(dir, 1<<20)
	for _, k := range []string{"aa0001", "bb0002", "cc0003"} {
		f.Put("m", k, fsEntryOf(1, 1000))
	}

	// Reopening with a smaller budget must take effect immediately, not after
	// the next write.
	smaller, err := NewFS(dir, 1500)
	require.NoError(t, err)
	require.LessOrEqual(t, smaller.Bytes(), int64(1500))
}

func TestFSDropsTornWrites(t *testing.T) {
	// A payload whose metadata is missing cannot be described, so it must not
	// be served -- a crash between the two writes must not leave a usable but
	// unidentifiable entry.
	dir := t.TempDir()
	f, _ := NewFS(dir, 1<<20)
	f.Put("m", "aa9999", fsEntryOf(1, 512))

	require.NoError(t, os.Remove(filepath.Join(dir, "r", "aa", "aa9999.meta")))

	reopened, err := NewFS(dir, 1<<20)
	require.NoError(t, err)
	_, ok := reopened.Get("aa9999")
	require.False(t, ok)
	require.Equal(t, 0, reopened.Len())
}

func TestFSRejectsOversizedAndBadConfig(t *testing.T) {
	_, err := NewFS(t.TempDir(), 0)
	require.Error(t, err, "a zero budget is a configuration mistake, not a no-op cache")

	f, _ := NewFS(t.TempDir(), 100)
	f.Put("m", "aa0000", fsEntryOf(1, 1000)) // larger than the whole budget
	require.Equal(t, 0, f.Len())
}

func TestFSMediaIDIsNeverUsedAsAFilename(t *testing.T) {
	// Media ids carry `~` and `.` and are attacker-visible.
	dir := t.TempDir()
	f, _ := NewFS(dir, 1<<20)
	f.PutDims("../../etc/passwd~mv2.png", 10, 10)

	w, h, ok := f.Dims("../../etc/passwd~mv2.png")
	require.True(t, ok)
	require.Equal(t, 10, w)
	require.Equal(t, 10, h)

	entries, err := os.ReadDir(filepath.Join(dir, "d"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.NotContains(t, entries[0].Name(), "passwd")
}

func TestFSSatisfiesStore(t *testing.T) {
	var _ Store = (*FS)(nil)
	var _ Store = (*Memory)(nil)
	var _ Store = Nop{}
}

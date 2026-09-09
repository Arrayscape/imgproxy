package wixcache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/imgproxy/imgproxy/v4/imagetype"
	"github.com/imgproxy/imgproxy/v4/wix"
)

// FS is a durable rendition cache on disk.
//
// It exists because an in-process cache is empty after every restart and is not
// shared between replicas, so derivation would stay far rarer here than on the
// CDN, where roughly half of a mature URL set is derived.
//
// Layout, content-addressed by the same key the memory store uses:
//
//	<dir>/r/<ab>/<key>        the rendition bytes, exactly as served
//	<dir>/r/<ab>/<key>.meta   JSON: media id, format, source rect, size, depth
//	<dir>/d/<hash>.dims       JSON: one master's dimensions
//
// The payload is a bare file rather than a header-plus-body, because an exact
// cache hit serves those bytes verbatim and nothing may be prepended to them.
type FS struct {
	dir      string
	maxBytes int64

	mu       sync.Mutex
	bytes    int64
	entries  map[string]*fsEntry
	byMaster map[string]map[string]struct{}
}

type fsEntry struct {
	mediaID string
	size    int64
	used    time.Time
}

// fsMeta is the sidecar. Effects are recorded so a sharpened or blurred
// rendition is never offered as a resamplable ancestor after a restart, when
// the in-memory Entry that knew is long gone.
type fsMeta struct {
	MediaID string    `json:"media_id"`
	Format  string    `json:"format"`
	SrcRect [4]int    `json:"src_rect"`
	Width   int       `json:"width"`
	Height  int       `json:"height"`
	Depth   int       `json:"depth"`
	Effects fsEffects `json:"effects"`
}

type fsEffects struct {
	Blur      float64 `json:"blur"`
	USMSigma  float64 `json:"usm_sigma"`
	USMAmount float64 `json:"usm_amount"`
	USMThresh float64 `json:"usm_threshold"`
	HasUSM    bool    `json:"has_usm"`
}

// NewFS opens or creates a cache at dir, bounded by maxBytes of payload.
//
// The directory is scanned on open rather than trusting an index file: an index
// can go stale or be truncated by a crash, while the directory is the truth.
// The cost is a startup walk proportional to the number of entries.
func NewFS(dir string, maxBytes int64) (*FS, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("wixcache: cache size must be positive, got %d", maxBytes)
	}
	for _, sub := range []string{"r", "d"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("wixcache: cannot create %s: %w", dir, err)
		}
	}

	f := &FS{
		dir:      dir,
		maxBytes: maxBytes,
		entries:  make(map[string]*fsEntry),
		byMaster: make(map[string]map[string]struct{}),
	}
	if err := f.scan(); err != nil {
		return nil, err
	}
	f.evictLocked() // a smaller maxBytes than last run must take effect now
	return f, nil
}

func (f *FS) scan() error {
	root := filepath.Join(f.dir, "r")
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Ext(path) == ".meta" ||
			filepath.Ext(path) == ".tmp" {
			return nil //nolint:nilerr // a bad entry is skipped, not fatal
		}
		meta, merr := readMeta(path + ".meta")
		if merr != nil {
			// Payload without readable metadata: a torn write. Drop it rather
			// than serve something we cannot describe.
			os.Remove(path)
			os.Remove(path + ".meta")
			return nil
		}
		f.index(filepath.Base(path), meta.MediaID, info.Size(), info.ModTime())
		return nil
	})
}

func (f *FS) index(key, mediaID string, size int64, used time.Time) {
	f.entries[key] = &fsEntry{mediaID: mediaID, size: size, used: used}
	f.bytes += size
	if f.byMaster[mediaID] == nil {
		f.byMaster[mediaID] = make(map[string]struct{})
	}
	f.byMaster[mediaID][key] = struct{}{}
}

func (f *FS) pathFor(key string) string {
	return filepath.Join(f.dir, "r", key[:2], key)
}

func (f *FS) Get(key string) (*Entry, bool) {
	f.mu.Lock()
	e, ok := f.entries[key]
	if ok {
		e.used = time.Now()
	}
	f.mu.Unlock()
	if !ok {
		return nil, false
	}

	p := f.pathFor(key)
	data, err := os.ReadFile(p)
	if err != nil {
		f.forget(key)
		return nil, false
	}
	meta, err := readMeta(p + ".meta")
	if err != nil {
		f.forget(key)
		return nil, false
	}

	// Touch so the LRU survives a restart: mtime is the only access record a
	// plain directory keeps.
	_ = os.Chtimes(p, time.Now(), time.Now())

	return meta.entry(data), true
}

func (f *FS) Put(mediaID, key string, e *Entry) {
	if e == nil || len(e.Data) == 0 || int64(len(e.Data)) > f.maxBytes {
		return
	}

	p := f.pathFor(key)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}

	// Write through a temp file and rename, so a crash never leaves a torn
	// payload that a later run would serve as if it were complete.
	if err := writeAtomic(p+".meta", metaFor(mediaID, e)); err != nil {
		return
	}
	if err := writeAtomic(p, e.Data); err != nil {
		os.Remove(p + ".meta")
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if old, ok := f.entries[key]; ok {
		f.bytes -= old.size
	}
	f.index(key, mediaID, int64(len(e.Data)), time.Now())
	f.evictLocked()
}

func (f *FS) Ancestors(mediaID string) []*Entry {
	f.mu.Lock()
	keys := make([]string, 0, len(f.byMaster[mediaID]))
	for k := range f.byMaster[mediaID] {
		keys = append(keys, k)
	}
	f.mu.Unlock()

	// Sorted so the candidate order is a function of the cache contents alone,
	// not of map iteration -- SelectAncestor must be deterministic.
	sort.Strings(keys)

	out := make([]*Entry, 0, len(keys))
	for _, k := range keys {
		if e, ok := f.Get(k); ok {
			out = append(out, e)
		}
	}
	return out
}

func (f *FS) Dims(mediaID string) (int, int, bool) {
	var d [2]int
	b, err := os.ReadFile(f.dimsPath(mediaID))
	if err != nil || json.Unmarshal(b, &d) != nil || d[0] <= 0 || d[1] <= 0 {
		return 0, 0, false
	}
	return d[0], d[1], true
}

func (f *FS) PutDims(mediaID string, w, h int) {
	if w <= 0 || h <= 0 {
		return
	}
	b, err := json.Marshal([2]int{w, h})
	if err != nil {
		return
	}
	_ = writeAtomic(f.dimsPath(mediaID), b)
}

// dimsPath hashes the media id: it contains `~` and `.` and is attacker-visible,
// so it is never used as a filename directly.
func (f *FS) dimsPath(mediaID string) string {
	sum := sha256.Sum256([]byte(mediaID))
	return filepath.Join(f.dir, "d", hex.EncodeToString(sum[:])+".dims")
}

// Len reports the number of cached renditions, for tests and diagnostics.
func (f *FS) Len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.entries)
}

// Bytes reports the cached payload total.
func (f *FS) Bytes() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bytes
}

func (f *FS) forget(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropLocked(key)
}

func (f *FS) dropLocked(key string) {
	e, ok := f.entries[key]
	if !ok {
		return
	}
	f.bytes -= e.size
	delete(f.entries, key)
	if s := f.byMaster[e.mediaID]; s != nil {
		delete(s, key)
		if len(s) == 0 {
			delete(f.byMaster, e.mediaID)
		}
	}
	p := f.pathFor(key)
	os.Remove(p)
	os.Remove(p + ".meta")
}

// evictLocked drops least-recently-used entries until the cache is inside its
// budget. Callers hold f.mu.
func (f *FS) evictLocked() {
	if f.bytes <= f.maxBytes {
		return
	}
	keys := make([]string, 0, len(f.entries))
	for k := range f.entries {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return f.entries[keys[i]].used.Before(f.entries[keys[j]].used)
	})
	for _, k := range keys {
		if f.bytes <= f.maxBytes {
			return
		}
		f.dropLocked(k)
	}
}

func writeAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

func readMeta(path string) (*fsMeta, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m fsMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func metaFor(mediaID string, e *Entry) []byte {
	m := fsMeta{
		MediaID: mediaID,
		Format:  e.Format.String(),
		SrcRect: [4]int{e.SrcRect.Min.X, e.SrcRect.Min.Y, e.SrcRect.Max.X, e.SrcRect.Max.Y},
		Width:   e.Width,
		Height:  e.Height,
		Depth:   e.Depth,
		Effects: fsEffects{Blur: e.Effects.Blur},
	}
	if u := e.Effects.USM; u != nil {
		m.Effects.HasUSM = true
		m.Effects.USMSigma, m.Effects.USMAmount, m.Effects.USMThresh =
			u.Sigma, u.Amount, u.Threshold
	}
	b, _ := json.Marshal(m)
	return b
}

func (m *fsMeta) entry(data []byte) *Entry {
	t, _ := imagetype.GetTypeByName(m.Format)
	e := &Entry{
		Data:    data,
		Format:  t,
		SrcRect: image.Rect(m.SrcRect[0], m.SrcRect[1], m.SrcRect[2], m.SrcRect[3]),
		Width:   m.Width,
		Height:  m.Height,
		Depth:   m.Depth,
		Effects: wix.Effects{Blur: m.Effects.Blur},
	}
	if m.Effects.HasUSM {
		e.Effects.USM = &wix.USM{
			Sigma: m.Effects.USMSigma, Amount: m.Effects.USMAmount,
			Threshold: m.Effects.USMThresh,
		}
	}
	return e
}

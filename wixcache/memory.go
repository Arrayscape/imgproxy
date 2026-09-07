package wixcache

import (
	"container/list"
	"sync"
)

// Memory is an in-process LRU store bounded by total payload bytes.
//
// Entries are shared across requests and goroutines, so callers must treat the
// Data they get back as read-only.
type Memory struct {
	mu       sync.Mutex
	maxBytes int
	bytes    int

	entries map[string]*list.Element
	lru     *list.List // front = most recently used

	// byMaster indexes renditions per media id, for ancestor selection.
	byMaster map[string]map[string]struct{}

	dims map[string][2]int
}

type memItem struct {
	key     string
	mediaID string
	entry   *Entry
}

// NewMemory creates a store holding at most maxBytes of rendition payload.
func NewMemory(maxBytes int) *Memory {
	return &Memory{
		maxBytes: maxBytes,
		entries:  make(map[string]*list.Element),
		lru:      list.New(),
		byMaster: make(map[string]map[string]struct{}),
		dims:     make(map[string][2]int),
	}
}

func (m *Memory) Get(key string) (*Entry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	el, ok := m.entries[key]
	if !ok {
		return nil, false
	}
	m.lru.MoveToFront(el)
	return el.Value.(*memItem).entry, true
}

// Put stores a rendition. mediaID is carried on the Entry's key so the entry
// can be indexed for ancestor lookup; see PutFor.
func (m *Memory) Put(key string, e *Entry) {
	m.PutFor("", key, e)
}

// PutFor stores a rendition and indexes it under a media id so it can later be
// offered as an ancestor for that master.
func (m *Memory) PutFor(mediaID, key string, e *Entry) {
	if e == nil || len(e.Data) == 0 || len(e.Data) > m.maxBytes {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if el, ok := m.entries[key]; ok {
		it := el.Value.(*memItem)
		m.bytes -= len(it.entry.Data)
		it.entry = e
		m.bytes += len(e.Data)
		m.lru.MoveToFront(el)
		return
	}

	it := &memItem{key: key, mediaID: mediaID, entry: e}
	m.entries[key] = m.lru.PushFront(it)
	m.bytes += len(e.Data)

	if mediaID != "" {
		if m.byMaster[mediaID] == nil {
			m.byMaster[mediaID] = make(map[string]struct{})
		}
		m.byMaster[mediaID][key] = struct{}{}
	}

	for m.bytes > m.maxBytes && m.lru.Len() > 0 {
		m.evictOldest()
	}
}

func (m *Memory) evictOldest() {
	el := m.lru.Back()
	if el == nil {
		return
	}
	it := el.Value.(*memItem)
	m.lru.Remove(el)
	delete(m.entries, it.key)
	m.bytes -= len(it.entry.Data)
	if s := m.byMaster[it.mediaID]; s != nil {
		delete(s, it.key)
		if len(s) == 0 {
			delete(m.byMaster, it.mediaID)
		}
	}
}

func (m *Memory) Ancestors(mediaID string) []*Entry {
	m.mu.Lock()
	defer m.mu.Unlock()

	keys := m.byMaster[mediaID]
	out := make([]*Entry, 0, len(keys))
	for k := range keys {
		if el, ok := m.entries[k]; ok {
			out = append(out, el.Value.(*memItem).entry)
		}
	}
	return out
}

func (m *Memory) Dims(mediaID string) (int, int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.dims[mediaID]
	return d[0], d[1], ok
}

func (m *Memory) PutDims(mediaID string, w, h int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dims[mediaID] = [2]int{w, h}
}

// Len reports the number of cached renditions, for tests and diagnostics.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lru.Len()
}

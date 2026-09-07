// Package wixcache implements the rendition cache the Wix CDN uses as an input
// to later transforms. OP-SPEC.md §10, WIX-URL-SPEC.md §6.
//
// The CDN caches transformed output and derives new renditions from it rather
// than from the master, which is how roughly half of a mature URL set is
// produced. The pipeline is identical; only the input differs.
//
// Two things this deliberately does NOT try to do:
//
//   - Reproduce the CDN's choice of ancestor. That choice depends on node-local
//     cache state at the moment an entry was first created -- adjacent widths
//     one pixel apart resolve differently, and once created an entry is frozen
//     and shared. It is not reproducible in principle, so we define our own
//     policy that is a pure function of the request and the cache contents.
//   - Run by default. Deriving changes output bytes, and the master path is the
//     one measured byte-exact against the CDN, so it stays the default.
package wixcache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"

	"github.com/imgproxy/imgproxy/v4/imagetype"
	"github.com/imgproxy/imgproxy/v4/wix"
)

// Key identifies a rendition by everything that can change its bytes.
type Key struct {
	MediaID string
	Plan    wix.Plan
	Effects wix.Effects
	Format  imagetype.Type
	Quality int

	// Codec distinguishes VP8 from VP8L output, which follows the STORED
	// master's nature rather than anything in the URL.
	Codec string
}

// String is a stable content-addressed identifier. Field order is fixed so the
// same request always produces the same key regardless of map iteration.
func (k Key) String() string {
	u := ""
	if e := k.Effects.USM; e != nil {
		u = fmt.Sprintf("%v_%v_%v", e.Sigma, e.Amount, e.Threshold)
	}
	s := fmt.Sprintf(
		"%s|%d,%d,%d,%d|%v|%d,%d|%d,%d|%d,%d|%t|%s|%v|%d|%s",
		k.MediaID,
		k.Plan.NX, k.Plan.NY, k.Plan.HW, k.Plan.HH,
		k.Plan.S,
		k.Plan.DW, k.Plan.DH,
		k.Plan.TX, k.Plan.TY,
		k.Plan.W, k.Plan.H,
		k.Plan.Identity,
		u, k.Effects.Blur,
		k.Quality,
		k.Format.String()+"/"+k.Codec,
	)
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Entry is one cached rendition.
type Entry struct {
	Data   []byte
	Format imagetype.Type

	// SrcRect is the MASTER rectangle these pixels were rendered from. It is
	// what makes ancestor selection sound: an ancestor is only usable when its
	// rectangle CONTAINS the one the new request needs, otherwise the two are
	// framed differently and deriving would silently crop the wrong region.
	SrcRect image.Rectangle

	// Width and Height are the cached image's own dimensions.
	Width, Height int

	// Effects baked into these pixels. A sharpened or blurred ancestor is not a
	// resamplable source.
	Effects wix.Effects

	// Depth is how many derivations produced this entry. Quality degrades with
	// depth, since each generation resamples an already-resampled image.
	Depth int
}

// Store holds renditions and the master geometry needed to plan against them.
type Store interface {
	// Get returns an exact rendition.
	Get(key string) (*Entry, bool)

	// Put stores a rendition.
	Put(key string, e *Entry)

	// Ancestors returns cached renditions of one master, for derivation.
	Ancestors(mediaID string) []*Entry

	// Dims returns a master's dimensions. Cached separately so a plan can be
	// resolved without fetching and decoding the master first -- otherwise the
	// cache could never avoid the work it exists to avoid.
	Dims(mediaID string) (w, h int, ok bool)

	// PutDims records a master's dimensions.
	PutDims(mediaID string, w, h int)
}

// Nop is a Store that never caches. It is what runs when derivation is
// disabled, so the enabled and disabled paths are the same code.
type Nop struct{}

func (Nop) Get(string) (*Entry, bool)    { return nil, false }
func (Nop) Put(string, *Entry)           {}
func (Nop) Ancestors(string) []*Entry    { return nil }
func (Nop) Dims(string) (int, int, bool) { return 0, 0, false }
func (Nop) PutDims(string, int, int)     {}

package wix

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Op is a transform segment's operation. WIX-URL-SPEC.md §2.
type Op string

const (
	OpFit  Op = "fit"
	OpFill Op = "fill"
	OpCrop Op = "crop"
)

var errNotWixPath = errors.New("not a wix media path")

// ErrBadParams is returned when a segment is missing a parameter its op needs.
var ErrBadParams = errors.New("wix: segment is missing required parameters")

// Params holds one segment's comma-separated key_value pairs.
//
// Presence is kept separate from value on purpose: `al`'s PRESENCE changes the
// centring rounding independently of its value (WIX-URL-SPEC §3.2), so `al_c` --
// otherwise a no-op -- still selects the separate-halves form. A map that only
// stored values could not express that.
type Params map[string]string

// Has reports whether the key appeared at all, whatever its value.
func (p Params) Has(k string) bool { _, ok := p[k]; return ok }

// Int returns an integer parameter.
func (p Params) Int(k string) (int, bool) {
	v, ok := p[k]
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	return n, err == nil
}

// Float returns a float parameter.
func (p Params) Float(k string) (float64, bool) {
	v, ok := p[k]
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	return f, err == nil
}

// Float2 returns a parameter shaped `<a>_<b>`, as fp_<x>_<y> is. The key/value
// split takes the FIRST underscore, so fp_0.50_0.50 arrives here as "0.50_0.50".
func (p Params) Float2(k string) (float64, float64, bool) {
	v, ok := p[k]
	if !ok {
		return 0, 0, false
	}
	a, b, found := strings.Cut(v, "_")
	if !found {
		return 0, 0, false
	}
	af, err1 := strconv.ParseFloat(a, 64)
	bf, err2 := strconv.ParseFloat(b, 64)
	return af, bf, err1 == nil && err2 == nil
}

// Segment is one `<op>/<params>` pair from the URL.
type Segment struct {
	Op     Op
	Params Params
}

// Request is a parsed Wix media URL.
type Request struct {
	// MediaID is the stored original's identifier, e.g.
	// "0a7ba9_<32 hex>~mv2.png". It is OPAQUE: the extension is the original
	// upload format and lies about what is stored, so nothing may branch on it.
	// See WIX-URL-SPEC.md §1.1 -- four of this site's masters advertise
	// ~mv2.png and hold lossy WebP.
	MediaID string

	// Segments is the transform chain, applied left to right. Empty means the
	// bare media path, which returns the stored original untransformed and must
	// NOT travel the transform path. See OP-SPEC.md §9.
	Segments []Segment

	// Filename is the trailing path element. Its EXTENSION selects the output
	// format when the URL carries no `enc_` (WIX-URL-SPEC §4.1) -- an earlier
	// version of the spec called it arbitrary, which was wrong. With `enc_` it
	// is back to affecting CDN caching only, because Accept then decides.
	Filename string
}

// IsBare reports whether this is the bare `/media/<id>` path with no transform.
func (r *Request) IsBare() bool { return len(r.Segments) == 0 }

// ParsePath parses the path after the route prefix, e.g.
//
//	<media-id>/v1/fill/w_620,h_564,al_c/some-name.jpg
//	<media-id>/v1/crop/x_37,y_53,w_300,h_280/fill/w_620,h_564/some-name.jpg
//	<media-id>
//
// rawPath must be the RAW (still percent-encoded) path with the route prefix
// already trimmed. Segments are split before unescaping, so a %2F inside a
// media id cannot manufacture a phantom path element.
func ParsePath(rawPath string) (*Request, error) {
	rawPath = strings.TrimPrefix(rawPath, "/")
	if rawPath == "" {
		return nil, fmt.Errorf("%w: empty path", errNotWixPath)
	}

	parts := strings.Split(rawPath, "/")
	for i, p := range parts {
		u, err := url.PathUnescape(p)
		if err != nil {
			return nil, fmt.Errorf("%w: undecodable segment %d", errNotWixPath, i)
		}
		parts[i] = u
	}

	req := &Request{MediaID: parts[0]}
	if req.MediaID == "" {
		return nil, fmt.Errorf("%w: empty media id", errNotWixPath)
	}

	rest := parts[1:]
	if len(rest) == 0 {
		return req, nil // bare media path
	}

	// Everything after the media id must be a transform chain. Drop the literal
	// "v1" markers: a URL may carry more than one `/v1/<op>/<params>/` group
	// (WIX-URL-SPEC §1.2), and they are separators rather than operands.
	chain := make([]string, 0, len(rest))
	sawV1 := false
	for _, p := range rest {
		if p == "v1" {
			sawV1 = true
			continue
		}
		chain = append(chain, p)
	}
	if !sawV1 {
		return nil, fmt.Errorf("%w: expected /v1/ after the media id", errNotWixPath)
	}

	// Pair up <op>/<params>. A single leftover element is the filename.
	//
	// The Python reference regexes out `/v1/(.+?)/[^/]*$`, which REQUIRES a
	// trailing filename and silently drops the last op when one is absent.
	// Every production URL carries a filename so the two agree in practice;
	// pairing greedily is the more useful behaviour for a server and cannot
	// change the result for any real URL.
	for len(chain) >= 2 {
		op, raw := chain[0], chain[1]
		chain = chain[2:]
		req.Segments = append(req.Segments, Segment{Op: Op(op), Params: parseParams(raw)})
	}
	if len(chain) == 1 {
		req.Filename = chain[0]
	}

	if len(req.Segments) == 0 {
		return nil, fmt.Errorf("%w: /v1/ with no transform segment", errNotWixPath)
	}
	for _, s := range req.Segments {
		switch s.Op {
		case OpFit, OpFill, OpCrop:
		default:
			return nil, fmt.Errorf("%w: unknown op %q", errNotWixPath, s.Op)
		}
	}
	return req, nil
}

// parseParams splits `w_620,h_564,al_c` into {w:620, h:564, al:c}.
//
// The key/value split takes the FIRST underscore, so fp_0.50_0.50 yields
// {fp: "0.50_0.50"} and a bare flag with no underscore yields an empty value --
// which still registers as present, which is what `al` needs.
func parseParams(raw string) Params {
	p := make(Params)
	if raw == "" {
		return p
	}
	for _, kv := range strings.Split(raw, ",") {
		k, v, _ := strings.Cut(kv, "_")
		p[k] = v
	}
	return p
}

// Canonical renders the params back into a stable `k_v,k_v` string with the
// keys sorted, so a signature does not depend on the order the client wrote
// them in. A key that appeared with no value round-trips as a bare key, which
// keeps `al`'s presence-without-value distinguishable from its absence.
func (p Params) Canonical() string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(k)
		if v := p[k]; v != "" {
			sb.WriteByte('_')
			sb.WriteString(v)
		}
	}
	return sb.String()
}

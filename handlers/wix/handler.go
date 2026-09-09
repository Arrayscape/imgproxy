// Package wix serves the Wix CDN URL grammar.
//
//	/media/<media-id>/v1/<op>/<params>/<filename>   a transform
//	/media/<media-id>                               the stored original
//
// It sits alongside imgproxy's own grammar rather than replacing it. See
// WIXEMU.md for the emulated feature set and where it is unverified.
package wix

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/imgproxy/imgproxy/v4/errctx"
	"github.com/imgproxy/imgproxy/v4/handlers"
	"github.com/imgproxy/imgproxy/v4/imagedata"
	"github.com/imgproxy/imgproxy/v4/imagetype"
	"github.com/imgproxy/imgproxy/v4/monitoring"
	"github.com/imgproxy/imgproxy/v4/options"
	"github.com/imgproxy/imgproxy/v4/security"
	"github.com/imgproxy/imgproxy/v4/server"
	"github.com/imgproxy/imgproxy/v4/vips"
	"github.com/imgproxy/imgproxy/v4/wixcache"
	"github.com/imgproxy/imgproxy/v4/workers"

	procwix "github.com/imgproxy/imgproxy/v4/processing/wix"
	wixspec "github.com/imgproxy/imgproxy/v4/wix"
)

// HandlerContext provides the shared dependencies this handler borrows from
// imgproxy. Everything here is grammar-agnostic and reused as-is.
type HandlerContext interface {
	Workers() *workers.Workers
	ImageDataFactory() imagedata.Factory
	Security() *security.Checker
	Monitoring() *monitoring.Monitoring
}

type Handler struct {
	HandlerContext

	config      *Config
	profilePath string

	// cache reproduces the CDN's rendition-derived-from-rendition behaviour.
	// When derivation is disabled this is a wixcache.Nop, so the enabled and
	// disabled paths are the same code rather than two code paths that can
	// drift apart.
	cache wixcache.Store

	factsMu sync.RWMutex
	facts   map[string]masterFacts
}

func New(hCtx HandlerContext, config *Config) (*Handler, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	h := &Handler{HandlerContext: hCtx, config: config, cache: wixcache.Nop{}}
	if !config.Enabled {
		return h, nil
	}

	if config.DerivationCache {
		if config.DerivationCachePath != "" {
			fs, ferr := wixcache.NewFS(config.DerivationCachePath, int64(config.DerivationCacheSize))
			if ferr != nil {
				return nil, ferr
			}
			h.cache = fs
			slog.Info("wix: derivation cache on disk",
				slog.String("path", config.DerivationCachePath),
				slog.Int("max_bytes", config.DerivationCacheSize),
				slog.Int("entries", fs.Len()))
		} else {
			h.cache = wixcache.NewMemory(config.DerivationCacheSize)
			slog.Warn("wix: derivation cache is in-process only; it is empty after " +
				"every restart and not shared between replicas. Set " +
				"IMGPROXY_WIX_DERIVATION_CACHE_PATH to make it durable")
		}
		slog.Warn("wix: derivation cache is ON; renditions may be derived from " +
			"cached renditions rather than the master, which deliberately changes " +
			"output bytes (OP-SPEC §10)")
	}

	// OP-SPEC §2: refuse to start rather than silently render a different
	// transform.
	warnings, err := vips.CheckWixEnvironment(config.AllowUnverifiedLibvips)
	if err != nil {
		return nil, err
	}
	for _, w := range warnings {
		slog.Warn("wix: " + w)
	}

	// libvips 8.15 cannot take an output profile as a blob, only a filename.
	p, err := wixspec.MaterializeProfile(config.SRGBProfile)
	if err != nil {
		return nil, err
	}
	h.profilePath = p

	return h, nil
}

func (h *Handler) Execute(
	reqID string,
	rw server.ResponseWriter,
	req *http.Request,
) *server.Error {
	h.Monitoring().Stats().IncRequestsInProgress()
	defer h.Monitoring().Stats().DecRequestsInProgress()

	r, err := wixspec.ParsePath(h.rawPath(req))
	if err != nil {
		// §9.3. A path that does not parse -- an unrecognised op, a missing
		// /v1/, an undecodable segment -- is rejected upstream of the image
		// manipulator, so it is a routing failure and reads as 403 Forbidden,
		// not 404. A bad parameter VALUE is a different case; it reaches the
		// manipulator and surfaces below as a 400.
		return h.forbidden(rw, err)
	}

	if serr := h.verifySignature(req, r); serr != nil {
		return serr
	}

	imageURL := fmt.Sprintf(h.config.SourceURLTemplate, r.MediaID)
	if err := h.Security().VerifySourceURL(imageURL); err != nil {
		return server.NewError(errctx.Wrap(err), handlers.ErrCategorySecurity)
	}

	// Reject a parameter the ops cannot use before any fetch. The CDN answers
	// 400 here rather than 403 because the request routed correctly; only the
	// value is wrong. Checking it up front also means a malformed url never
	// costs a master download.
	if err := wixspec.ValidateParams(r.Segments); err != nil {
		return h.badRequest(rw, err)
	}

	// OP-SPEC §9: the bare media path must be routed BEFORE the transform
	// path, not through it. Passing it through would apply the §8 EXIF
	// whitelist and strip the original, which is the opposite of the required
	// behaviour. Nothing below this line runs for it.
	if r.IsBare() {
		return h.serveOriginal(reqID, rw, req, imageURL)
	}

	return h.serveTransform(reqID, rw, req, r, imageURL)
}

// rawPath returns the request path with the route prefix trimmed, still
// percent-encoded. RequestURI rather than URL.Path so an encoded character in
// a media id survives; ParsePath splits before unescaping.
func (h *Handler) rawPath(req *http.Request) string {
	uri := req.RequestURI
	if i := strings.IndexByte(uri, '?'); i >= 0 {
		uri = uri[:i]
	}
	if req.Pattern != "" {
		uri = strings.TrimPrefix(uri, req.Pattern)
	}
	return uri
}

// verifySignature enforces the optional signature mode. Wix URLs are unsigned,
// so this is off unless configured.
func (h *Handler) verifySignature(req *http.Request, r *wixspec.Request) *server.Error {
	if h.config.SignatureMode != SignatureQuery {
		return nil
	}
	sig := req.URL.Query().Get("sig")
	canon := h.canonicalPath(r)
	if err := h.Security().VerifySignature(req.Context(), sig, canon); err != nil {
		return server.NewError(errctx.Wrap(err), handlers.ErrCategorySecurity)
	}
	return nil
}

// canonicalPath rebuilds the path a signature covers, so that a signature does
// not depend on how the client happened to encode the URL.
func (h *Handler) canonicalPath(r *wixspec.Request) string {
	var sb strings.Builder
	sb.WriteString(url.PathEscape(r.MediaID))
	if !r.IsBare() {
		sb.WriteString("/v1")
		for _, s := range r.Segments {
			sb.WriteString("/")
			sb.WriteString(string(s.Op))
			sb.WriteString("/")
			sb.WriteString(s.Params.Canonical())
		}
	}
	return sb.String()
}

// serveOriginal reproduces the CDN's bare-media behaviour: the stored original,
// byte for byte, metadata and all.
//
// WIX-URL-SPEC §7.2 records that this LEAKS EXIF -- including GPS -- because
// media ids are public in every rendition URL on a page, so removing the
// transform suffix retrieves the untouched original. It is reproduced
// deliberately for compatibility; see WIXEMU.md.
func (h *Handler) serveOriginal(
	reqID string,
	rw server.ResponseWriter,
	req *http.Request,
	imageURL string,
) *server.Error {
	data, originHeaders, err := h.ImageDataFactory().DownloadSync(
		req.Context(), imageURL, "wix original", imagedata.DownloadOptions{})
	if err != nil {
		if isNotFound(err) {
			return h.forbidden(rw, err)
		}
		return server.NewError(errctx.Wrap(err), handlers.ErrCategoryDownload)
	}
	defer data.Close()

	size, serr := data.Size()
	if serr != nil {
		return server.NewError(errctx.Wrap(serr), handlers.ErrCategoryImageDataSize)
	}

	// §9.2. A different service answers this route than answers transforms,
	// and it shows: six times the TTL, a validator, and range support.
	//
	// These go on Header() directly rather than through SetContentType /
	// SetContentLength / SetExpires. ServeContent deletes content-type,
	// content-length and last-modified when it turns the request into a 304,
	// and flushHeaders would copy them straight back in from the staged set,
	// producing a 304 carrying a body's headers.
	rw.Header().Set("Content-Type", data.Format().Mime())
	rw.Header().Set("Cache-Control", wixspec.OriginalCacheControl)

	// Frozen alongside max-age, for HTTP/1.0 caches. Wix computes it once at
	// origin rather than per response, so two fetches of the same object share
	// an Expires; we cannot observe their freeze point, so we compute it per
	// response from the same clock that Date uses.
	rw.Header().Set("Expires",
		time.Now().UTC().Add(wixspec.OriginalMaxAge*time.Second).Format(http.TimeFormat))

	// etag is md5 of the body -- verified against the CDN, not assumed. Strong,
	// quoted, lowercase hex. The bare path serves stored bytes unmodified, so
	// hashing what we are about to write is the same value the CDN publishes.
	sum, herr := md5Reader(data.Reader())
	if herr != nil {
		return server.NewError(errctx.Wrap(herr), handlers.ErrCategoryImageDataSize)
	}
	rw.Header().Set("ETag", `"`+sum+`"`)

	// No vary: this route ignores Accept entirely -- it serves the stored
	// object or nothing.

	modTime := originLastModified(originHeaders)

	// ServeContent handles If-None-Match, If-Modified-Since, If-Range and
	// Range, including the 206 the CDN serves despite never advertising
	// accept-ranges. We advertise it because the CDN does, on the full
	// response only -- ServeContent sets its own on a 206.
	rw.Header().Set("Accept-Ranges", "bytes")

	rec := &statusRecorder{ResponseWriter: rw, code: http.StatusOK}
	http.ServeContent(rec, req, "", modTime, data.Reader())

	server.LogResponse(reqID, req, rec.code, nil,
		slog.String("image_url", imageURL), slog.String("wix", "original"),
		slog.Int("size", size))
	return nil
}

// statusRecorder remembers the status ServeContent chose -- 200, 206 or 304 --
// which the response writer does not expose, so the access log records what was
// actually served rather than assuming a full body.
type statusRecorder struct {
	server.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

// noAcceptRanges suppresses the accept-ranges header on a full response.
// ServeContent sets it unconditionally; the transform route serves ranges but
// never advertises that it does, so the header is removed at the moment the
// status is known -- on a 206 ServeContent has already written its own
// Content-Range and the advertisement is expected.
type noAcceptRanges struct {
	*statusRecorder
}

func (n *noAcceptRanges) WriteHeader(code int) {
	if code != http.StatusPartialContent {
		n.Header().Del("Accept-Ranges")
	}
	n.statusRecorder.WriteHeader(code)
}

// md5Reader hashes the whole stream and rewinds it, so the caller can still
// serve from the same reader.
func md5Reader(r io.ReadSeeker) (string, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	h := md5.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// originLastModified recovers the stored object's modification time from the
// fetch, so ours tracks the object rather than the moment we happened to serve
// it. A zero time makes ServeContent omit the header and ignore
// If-Modified-Since, which is the honest answer when the origin gave us none.
func originLastModified(h http.Header) time.Time {
	if h == nil {
		return time.Time{}
	}
	t, err := http.ParseTime(h.Get("Last-Modified"))
	if err != nil {
		return time.Time{}
	}
	return t
}

func (h *Handler) serveTransform(
	reqID string,
	rw server.ResponseWriter,
	req *http.Request,
	r *wixspec.Request,
	imageURL string,
) *server.Error {
	ctx := req.Context()

	release, err := h.Workers().Acquire(ctx)
	if err != nil {
		return server.NewError(errctx.Wrap(err), handlers.ErrCategoryQueue)
	}
	defer release()

	h.Monitoring().Stats().IncImagesInProgress()
	defer h.Monitoring().Stats().DecImagesInProgress()

	result, origin, rerr := h.render(ctx, r, req.Header.Get("Accept"))
	if rerr != nil {
		// A master the CDN's upload path would never have accepted is a
		// property of the request, not a server fault: 422, not 500.
		if errors.Is(rerr, procwix.ErrUnsupportedMasterFormat) {
			return server.NewError(
				handlers.NewCantLoadError(ctx, imagetype.Unknown),
				handlers.ErrCategoryPathParsing)
		}
		// A media id that does not exist is a routing failure to the CDN,
		// answered 403 by the same tier that rejects an unknown op -- it never
		// reaches the manipulator, so it cannot produce a manipulator error.
		if isNotFound(rerr) {
			return h.forbidden(rw, rerr)
		}
		// A parameter value the geometry could not use. Reached the
		// manipulator, so 400.
		if errors.Is(rerr, wixspec.ErrBadParams) {
			return h.badRequest(rw, rerr)
		}
		return server.NewError(errctx.Wrap(rerr), handlers.ErrCategoryProcessing)
	}

	// §7.5 / §9.5. A pass-through format is answered by the media router, not
	// the image manipulator -- the same service that answers a bare original.
	// So it takes the ORIGINAL header set, not the transform one: 180 days, an
	// etag, a validator, conditional GET. Serving the CDN's bytes under the
	// wrong service's headers would be a half-emulation.
	//
	// This re-fetches rather than reusing what render just read. The waste is
	// bounded to pass-through masters, which are rare, and it buys the whole
	// §9.2 contract -- including last-modified, which only the fetch knows --
	// without threading origin headers back through the render path.
	//
	// The §9.2 shape here is the SPEC'S PREDICTION, not a measurement: no GIF
	// master outside the probe uploads exists to check it against.
	if origin == "passthrough" {
		result.Close()
		return h.serveOriginal(reqID, rw, req, imageURL)
	}

	defer result.Close()

	size, serr := result.Size()
	if serr != nil {
		return server.NewError(errctx.Wrap(serr), handlers.ErrCategoryImageDataSize)
	}

	// Content-Type goes on Header() rather than through SetContentType, which
	// only stages it: ServeContent sniffs the body when Header() has no
	// content-type, and Go's sniffer does not know AVIF -- it would answer
	// application/octet-stream and Set that, beating the staged value.
	rw.Header().Set("Content-Type", result.Format().Mime())
	rw.SetContentLength(size)
	rw.SetCanonical(imageURL)

	// §9.1. Set directly rather than through SetExpires: the shape is a
	// property of the route, not of any TTL config, and flushHeaders leaves an
	// already-set Cache-Control alone.
	rw.Header().Set("Cache-Control", wixspec.TransformCacheControl)

	// Params() is cheap and pure -- the render path derives the same values
	// independently; taking them again here keeps the header decision next to
	// the header rather than threading it back out of render.
	_, enc := r.Params()

	// Vary only when the url opts into negotiation. Without enc_ the filename
	// decides the format and Accept is ignored entirely, so the CDN sends no
	// Vary at all -- advertising one would invite caches to split needlessly.
	if wixspec.TransformVariesOnAccept(enc) {
		rw.Header().Set("Vary", "Accept")
	}

	// Transforms carry no etag, last-modified, expires or accept-ranges,
	// whether served from the master or cache-derived: the two are
	// indistinguishable at the header level.
	// Which input produced this response: master, a cached rendition, or an
	// exact cache hit. The corpus scorer asserts this never says "derived"
	// while the derivation cache is off.
	rw.Header().Set("X-Wix-Source", origin)

	// Range requests are honoured -- 206 with a Content-Range -- even though
	// this route never advertises accept-ranges. noAcceptRanges strips the
	// header ServeContent would otherwise add to the 200; on a 206 the
	// advertisement is expected and stays.
	//
	// modtime is deliberately zero and no etag is set, so ServeContent adds
	// neither last-modified nor a validator, and conditional requests are
	// ignored rather than answered -- which is what the CDN does here.
	rec := &statusRecorder{ResponseWriter: rw, code: http.StatusOK}
	http.ServeContent(&noAcceptRanges{rec}, req, "", time.Time{}, result.Reader())

	server.LogResponse(reqID, req, rec.code, nil,
		slog.String("image_url", imageURL),
		slog.String("wix_format", result.Format().String()),
		slog.String("wix_source", origin))
	return nil
}

// render runs the whole transform: resolve, decode, apply §5, encode.
//
// The input is either the master or a previously cached rendition (OP-SPEC
// §10). The pipeline is identical either way; only the input differs.
func (h *Handler) render(
	ctx context.Context,
	r *wixspec.Request,
	accept string,
) (imagedata.ImageData, string, error) {
	imageURL := fmt.Sprintf(h.config.SourceURLTemplate, r.MediaID)
	fx, enc := r.Params()

	// Resolving needs the master's dimensions. They are cached separately so a
	// cache hit does not have to fetch and decode the master just to learn how
	// big it is -- otherwise the cache could never avoid the work it exists for.
	if mw, mh, ok := h.cache.Dims(r.MediaID); ok {
		plan, err := wixspec.Resolve(r.Segments, mw, mh)
		if err != nil {
			return nil, "", err
		}

		// The stored master's own nature decides the WebP codec and the
		// fallback format, so it is part of the key even on a cache hit.
		lossy, format, ok := h.cachedMasterFacts(r.MediaID)
		if ok {
			out := wixspec.Negotiate(enc.Enc, accept, r.Filename, format, h.config.AVIF)
			key := h.key(r.MediaID, plan, fx, enc, out, lossy)

			if e, hit := h.cache.Get(key); hit {
				return imagedata.NewFromBytesWithFormat(e.Format, e.Data), "cache", nil
			}

			if anc := wixcache.SelectAncestor(
				h.cache.Ancestors(r.MediaID), plan, h.config.DerivationMaxDepth, mw, mh,
			); anc != nil {
				if data, src, derr := h.deriveFrom(
					anc, r.Segments, plan, fx, enc, out, lossy, key, r.MediaID,
				); derr == nil {
					return data, src, nil
				}
				// Any failure deriving falls through to the master, which is
				// always correct if slower.
			}
		}
	}

	return h.renderFromMaster(ctx, r, imageURL, accept, fx, enc)
}

// renderFromMaster is the path that is measured byte-exact against the CDN.
func (h *Handler) renderFromMaster(
	ctx context.Context,
	r *wixspec.Request,
	imageURL, accept string,
	fx wixspec.Effects,
	enc wixspec.Encoding,
) (imagedata.ImageData, string, error) {
	data, _, derr := h.ImageDataFactory().DownloadSync(
		ctx, imageURL, "wix master", imagedata.DownloadOptions{})
	if derr != nil {
		return nil, "", derr
	}
	defer data.Close()

	// §9.1: GIF is passed through untransformed. Ingest stores it verbatim and
	// the media router answers rather than the image manipulator, so the
	// transform is NOT applied -- w_180,h_135 returns the full-size original,
	// and animation survives. Decided before decoding, because there is
	// nothing to decode.
	if passthrough, perr := h.passThrough(data); perr != nil {
		return nil, "", perr
	} else if passthrough != nil {
		return passthrough, "passthrough", nil
	}

	img := new(vips.Image)
	defer img.Clear()

	src, err := procwix.NewSource(img, data)
	if err != nil {
		return nil, "", err
	}
	h.cache.PutDims(r.MediaID, src.Width, src.Height)
	h.rememberMasterFacts(r.MediaID, src)

	plan, err := wixspec.Resolve(r.Segments, src.Width, src.Height)
	if err != nil {
		return nil, "", err
	}

	if err := procwix.Render(img, src, plan, fx, h.profilePath); err != nil {
		return nil, "", err
	}

	format := wixspec.Negotiate(enc.Enc, accept, r.Filename, src.Format, h.config.AVIF)
	if format == imagetype.Unknown {
		return nil, "", fmt.Errorf("wix: could not determine the stored master's format")
	}

	out, err := procwix.Encode(img, src, format, enc)
	if err != nil {
		return nil, "", err
	}

	h.store(r.MediaID, plan, fx, enc, format, src.Lossy, out, image.Rect(
		plan.NX, plan.NY, plan.NX+plan.HW, plan.NY+plan.HH), 0)

	return out, "master", nil
}

// deriveFrom renders a plan from a cached rendition instead of the master.
//
// The ancestor is decoded from its stored PNG, which is what makes pHYs read
// 1000 rather than the master's own resolution -- the marker the CDN's own
// derived renditions carry (WIX-URL-SPEC §6.1).
func (h *Handler) deriveFrom(
	anc *wixcache.Entry,
	segs []wixspec.Segment,
	plan wixspec.Plan,
	fx wixspec.Effects,
	enc wixspec.Encoding,
	format imagetype.Type,
	lossy bool,
	key, mediaID string,
) (imagedata.ImageData, string, error) {
	// The ancestor IS the source: the segments are resolved against its
	// dimensions exactly as if it were the master. "The pipeline is identical;
	// only the input differs" (OP-SPEC §10).
	replanned, ok := wixcache.Replan(segs, anc)
	if !ok {
		return nil, "", errors.New("wix: cannot replan against the cached ancestor")
	}

	ancData := imagedata.NewFromBytesWithFormat(anc.Format, anc.Data)
	defer ancData.Close()

	img := new(vips.Image)
	defer img.Clear()

	src, err := procwix.NewSource(img, ancData)
	if err != nil {
		return nil, "", err
	}
	// The codec choice follows the ORIGINAL master, not the intermediate.
	src.Lossy = lossy

	// The CDN's cached intermediates carry no metadata and no resolution: a
	// derived rendition reads pHYs 1000 precisely because the resolution was
	// lost in the write/re-read cycle (WIX-URL-SPEC §6.1).
	//
	// Ours are complete PNGs, so without this the ancestor's metadata would
	// travel into the derived rendition and diverge from the CDN in two ways at
	// once -- pHYs would keep the master's 2834, and the canonical EXIF's
	// XResolution would be derived from the ANCESTOR's eXIf rather than from
	// this rendition's own pHYs. Both are bugs, not cosmetic differences.
	//
	// Order matters: Strip rewrites xres/yres to 72dpi, so the resolution reset
	// has to come after it.
	if err := img.Strip(false); err != nil {
		return nil, "", err
	}
	if err := img.WixResetResolution(); err != nil {
		return nil, "", err
	}
	// The PNG container fix-ups take the master's resolution from this head.
	// An intermediate has none, so the derived rendition must fall through to
	// deriving it from its own pHYs.
	src.Head = nil

	if err := procwix.Render(img, src, replanned, fx, h.profilePath); err != nil {
		return nil, "", err
	}

	out, err := procwix.Encode(img, src, format, enc)
	if err != nil {
		return nil, "", err
	}

	h.storeKeyed(key, mediaID, plan, fx, out, image.Rect(
		plan.NX, plan.NY, plan.NX+plan.HW, plan.NY+plan.HH), anc.Depth+1)

	return out, "derived", nil
}

// passThrough returns the stored bytes when the master's format is served
// untransformed, or nil when it is not. See wix.DispositionPassThrough.
func (h *Handler) passThrough(data imagedata.ImageData) (imagedata.ImageData, error) {
	r := data.Reader()
	head := make([]byte, wixspec.HeadSize)
	n, err := io.ReadFull(r, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, fmt.Errorf("wix: cannot read master head: %w", err)
	}
	head = head[:n]

	format := wixspec.StoredFormat(head)
	if wixspec.FormatDisposition(format) != wixspec.DispositionPassThrough {
		return nil, nil
	}

	// Re-read from the start: the whole file is the response.
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("wix: cannot rewind master: %w", err)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("wix: cannot read master: %w", err)
	}
	return imagedata.NewFromBytesWithFormat(format, b), nil
}

// masterFacts records what the stored master IS, so a cache hit can pick the
// same output format and WebP codec without re-fetching it. The media-id
// extension cannot be used for this: it lies (WIX-URL-SPEC §1.1).
type masterFacts struct {
	lossy  bool
	format imagetype.Type
}

func (h *Handler) rememberMasterFacts(mediaID string, src *procwix.Source) {
	if !h.config.DerivationCache {
		return
	}
	h.factsMu.Lock()
	defer h.factsMu.Unlock()
	if h.facts == nil {
		h.facts = make(map[string]masterFacts)
	}
	h.facts[mediaID] = masterFacts{lossy: src.Lossy, format: src.Format}
}

func (h *Handler) cachedMasterFacts(mediaID string) (lossy bool, format imagetype.Type, ok bool) {
	h.factsMu.RLock()
	defer h.factsMu.RUnlock()
	f, ok := h.facts[mediaID]
	return f.lossy, f.format, ok
}

func (h *Handler) key(
	mediaID string, plan wixspec.Plan, fx wixspec.Effects,
	enc wixspec.Encoding, format imagetype.Type, lossy bool,
) string {
	codec := ""
	if format == imagetype.WEBP {
		codec = "vp8"
		if !lossy {
			codec = "vp8l"
		}
	}
	return wixcache.Key{
		MediaID: mediaID, Plan: plan, Effects: fx,
		Format: format, Quality: enc.Quality, Codec: codec,
	}.String()
}

func (h *Handler) store(
	mediaID string, plan wixspec.Plan, fx wixspec.Effects, enc wixspec.Encoding,
	format imagetype.Type, lossy bool, out imagedata.ImageData,
	rect image.Rectangle, depth int,
) {
	h.storeKeyed(h.key(mediaID, plan, fx, enc, format, lossy), mediaID, plan, fx, out, rect, depth)
}

func (h *Handler) storeKeyed(
	key, mediaID string, plan wixspec.Plan, fx wixspec.Effects,
	out imagedata.ImageData, rect image.Rectangle, depth int,
) {
	if !h.config.DerivationCache {
		return
	}
	b, err := io.ReadAll(out.Reader())
	if err != nil {
		return
	}
	e := &wixcache.Entry{
		Data: b, Format: out.Format(), SrcRect: rect,
		Width: plan.W, Height: plan.H, Effects: fx, Depth: depth,
	}
	h.cache.Put(mediaID, key, e)
}

// unused keeps the options import honest until the derivation cache lands.
var _ = options.New

// forbidden answers 403 the way the CDN's routing tier does: a nonexistent
// media id and an unrecognised op are indistinguishable to a client, both
// "Forbidden", both uncacheable. §9.3.
//
// Cache-Control goes on before returning because the error middleware writes
// the body itself, and flushHeaders deliberately sets no Cache-Control on a
// 4xx -- so an already-present one survives, and an absent one stays absent.
func (h *Handler) forbidden(rw server.ResponseWriter, err error) *server.Error {
	rw.Header().Set("Cache-Control", wixspec.ForbiddenCacheControl)
	return server.NewError(
		errctx.NewTextError(err.Error(), 1,
			errctx.WithStatusCode(http.StatusForbidden),
			errctx.WithPublicMessage("Forbidden"),
			errctx.WithShouldReport(false),
		),
		handlers.ErrCategoryPathParsing,
	)
}

// badRequest answers 400 for a malformed parameter value on an op that is
// itself valid. Note the cache-control differs from forbidden's in both order
// and content -- a different tier composes it, and reproducing that difference
// is the point. §9.3.
func (h *Handler) badRequest(rw server.ResponseWriter, err error) *server.Error {
	rw.Header().Set("Cache-Control", wixspec.BadRequestCacheControl)

	msg := err.Error()
	var pe wixspec.ParamError
	if errors.As(err, &pe) {
		msg = pe.CDNMessage()
	}

	return server.NewError(
		errctx.NewTextError(err.Error(), 1,
			errctx.WithStatusCode(http.StatusBadRequest),
			errctx.WithPublicMessage(msg),
			errctx.WithShouldReport(false),
		),
		handlers.ErrCategoryPathParsing,
	)
}

// isNotFound reports whether a fetch failed because the object is not there,
// as opposed to a transport or permission failure. Only the former is the
// CDN's 403; a broken backend is still our fault and stays a 5xx.
func isNotFound(err error) bool {
	var nf interface{ StatusCode() int }
	if errors.As(err, &nf) && nf.StatusCode() == http.StatusNotFound {
		return true
	}
	return errors.Is(err, fs.ErrNotExist)
}

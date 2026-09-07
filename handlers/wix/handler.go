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
	"errors"
	"fmt"
	"image"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"

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
		h.cache = wixcache.NewMemory(config.DerivationCacheSize)
		slog.Warn("wix: derivation cache is ON; renditions may be derived from " +
			"cached renditions rather than the master, which deliberately changes " +
			"output bytes (OP-SPEC §10)")
	}

	// OP-SPEC §2: refuse to start rather than silently render a different
	// transform.
	warnings, err := vips.CheckWixEnvironment(config.AllowWrongTileHeight, config.AllowUnverifiedLibvips)
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
		return server.NewError(
			handlers.NewInvalidPathError(req.Context(), err.Error()), handlers.ErrCategoryPathParsing)
	}

	if serr := h.verifySignature(req, r); serr != nil {
		return serr
	}

	imageURL := fmt.Sprintf(h.config.SourceURLTemplate, r.MediaID)
	if err := h.Security().VerifySourceURL(imageURL); err != nil {
		return server.NewError(errctx.Wrap(err), handlers.ErrCategorySecurity)
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
	data, _, err := h.ImageDataFactory().DownloadSync(
		req.Context(), imageURL, "wix original", imagedata.DownloadOptions{})
	if err != nil {
		return server.NewError(errctx.Wrap(err), handlers.ErrCategoryDownload)
	}
	defer data.Close()

	size, serr := data.Size()
	if serr != nil {
		return server.NewError(errctx.Wrap(serr), handlers.ErrCategoryImageDataSize)
	}

	rw.SetContentType(data.Format().Mime())
	rw.SetContentLength(size)
	rw.WriteHeader(http.StatusOK)

	if _, cerr := io.Copy(rw, data.Reader()); cerr != nil {
		server.LogResponse(reqID, req, http.StatusOK,
			handlers.NewResponseWriteError(cerr), slog.String("image_url", imageURL))
		return nil
	}
	server.LogResponse(reqID, req, http.StatusOK, nil,
		slog.String("image_url", imageURL), slog.String("wix", "original"))
	return nil
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
		return server.NewError(errctx.Wrap(rerr), handlers.ErrCategoryProcessing)
	}
	defer result.Close()

	size, serr := result.Size()
	if serr != nil {
		return server.NewError(errctx.Wrap(serr), handlers.ErrCategoryImageDataSize)
	}

	rw.SetContentType(result.Format().Mime())
	rw.SetContentLength(size)
	rw.SetCanonical(imageURL)
	// The output format depends on Accept, so caches must vary on it (§4).
	rw.Header().Add("Vary", "Accept")
	// Which input produced this response: master, a cached rendition, or an
	// exact cache hit. The corpus scorer asserts this never says "derived"
	// while the derivation cache is off.
	rw.Header().Set("X-Wix-Source", origin)
	rw.WriteHeader(http.StatusOK)

	if _, cerr := io.Copy(rw, result.Reader()); cerr != nil {
		server.LogResponse(reqID, req, http.StatusOK,
			handlers.NewResponseWriteError(cerr), slog.String("image_url", imageURL))
		return nil
	}

	server.LogResponse(reqID, req, http.StatusOK, nil,
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
			out := wixspec.Negotiate(enc.Enc, accept, format, h.config.AVIF)
			key := h.key(r.MediaID, plan, fx, enc, out, lossy)

			if e, hit := h.cache.Get(key); hit {
				return imagedata.NewFromBytesWithFormat(e.Format, e.Data), "cache", nil
			}

			if anc := wixcache.SelectAncestor(
				h.cache.Ancestors(r.MediaID), plan, h.config.DerivationMaxDepth,
			); anc != nil {
				if data, src, derr := h.deriveFrom(anc, plan, fx, enc, out, lossy, key, r.MediaID); derr == nil {
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

	format := wixspec.Negotiate(enc.Enc, accept, src.Format, h.config.AVIF)
	if format == imagetype.Unknown {
		return nil, "", fmt.Errorf("wix: could not determine the stored master's format")
	}

	out, err := procwix.Encode(img, src, format, enc, 9-h.config.AVIFSpeed)
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
	plan wixspec.Plan,
	fx wixspec.Effects,
	enc wixspec.Encoding,
	format imagetype.Type,
	lossy bool,
	key, mediaID string,
) (imagedata.ImageData, string, error) {
	rebased, ok := wixcache.Rebase(plan, anc)
	if !ok {
		return nil, "", errors.New("wix: cannot rebase onto the cached ancestor")
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

	// The CDN's derived renditions read pHYs 1000 because the intermediate lost
	// its resolution in the write/re-read cycle (WIX-URL-SPEC §6.1). Our cached
	// intermediates are complete PNGs and DO carry resolution through, so the
	// marker is reproduced deliberately here rather than arriving for free.
	// It matters: it is how a derived rendition is told apart from a master
	// render, and the corpus scorer excludes derived rows on exactly this.
	if err := img.WixResetResolution(); err != nil {
		return nil, "", err
	}

	if err := procwix.Render(img, src, rebased, fx, h.profilePath); err != nil {
		return nil, "", err
	}

	out, err := procwix.Encode(img, src, format, enc, 9-h.config.AVIFSpeed)
	if err != nil {
		return nil, "", err
	}

	h.storeKeyed(key, mediaID, plan, fx, out, image.Rect(
		plan.NX, plan.NY, plan.NX+plan.HW, plan.NY+plan.HH), anc.Depth+1)

	return out, "derived", nil
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
	if m, ok := h.cache.(*wixcache.Memory); ok {
		m.PutFor(mediaID, key, e)
		return
	}
	h.cache.Put(key, e)
}

// unused keeps the options import honest until the derivation cache lands.
var _ = options.New

// Package wix serves the Wix CDN URL grammar.
//
//	/media/<media-id>/v1/<op>/<params>/<filename>   a transform
//	/media/<media-id>                               the stored original
//
// It sits alongside imgproxy's own grammar rather than replacing it. See
// WIXEMU.md for the emulated feature set and where it is unverified.
package wix

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/imgproxy/imgproxy/v4/errctx"
	"github.com/imgproxy/imgproxy/v4/handlers"
	"github.com/imgproxy/imgproxy/v4/imagedata"
	"github.com/imgproxy/imgproxy/v4/imagetype"
	"github.com/imgproxy/imgproxy/v4/monitoring"
	"github.com/imgproxy/imgproxy/v4/options"
	"github.com/imgproxy/imgproxy/v4/security"
	"github.com/imgproxy/imgproxy/v4/server"
	"github.com/imgproxy/imgproxy/v4/vips"
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
}

func New(hCtx HandlerContext, config *Config) (*Handler, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	h := &Handler{HandlerContext: hCtx, config: config}
	if !config.Enabled {
		return h, nil
	}

	// OP-SPEC §2: refuse to start rather than silently render a different
	// transform.
	warnings, err := vips.CheckWixEnvironment(config.AllowWrongTileHeight)
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

	data, _, derr := h.ImageDataFactory().DownloadSync(
		ctx, imageURL, "wix master", imagedata.DownloadOptions{})
	if derr != nil {
		return server.NewError(errctx.Wrap(derr), handlers.ErrCategoryDownload)
	}
	defer data.Close()

	result, rerr := h.render(r, data, req.Header.Get("Accept"))
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
	rw.WriteHeader(http.StatusOK)

	if _, cerr := io.Copy(rw, result.Reader()); cerr != nil {
		server.LogResponse(reqID, req, http.StatusOK,
			handlers.NewResponseWriteError(cerr), slog.String("image_url", imageURL))
		return nil
	}

	server.LogResponse(reqID, req, http.StatusOK, nil,
		slog.String("image_url", imageURL),
		slog.String("wix_format", result.Format().String()))
	return nil
}

// render runs the whole transform: decode, resolve geometry, apply §5, encode.
func (h *Handler) render(
	r *wixspec.Request,
	data imagedata.ImageData,
	accept string,
) (imagedata.ImageData, error) {
	img := new(vips.Image)
	defer img.Clear()

	src, err := procwix.NewSource(img, data)
	if err != nil {
		return nil, err
	}

	plan, err := wixspec.Resolve(r.Segments, src.Width, src.Height)
	if err != nil {
		return nil, err
	}

	fx, enc := r.Params()

	if err := procwix.Render(img, src, plan, fx, h.profilePath); err != nil {
		return nil, err
	}

	format := wixspec.Negotiate(enc.Enc, accept, src.Format, h.config.AVIF)
	if format == imagetype.Unknown {
		return nil, fmt.Errorf("wix: could not determine the stored master's format")
	}

	return procwix.Encode(img, src, format, enc, 9-h.config.AVIFSpeed)
}

// unused keeps the options import honest until the derivation cache lands.
var _ = options.New

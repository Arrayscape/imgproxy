package wix

import (
	"strings"

	"github.com/imgproxy/imgproxy/v4/imagetype"
)

// Negotiate chooses the output format. WIX-URL-SPEC §4.
//
// `enc` does not name the format -- it is an opt-in FLAG, and the format is
// then chosen from the request's Accept header, preferring avif > webp > the
// STORED master's own format:
//
//	enc_avif + Accept: */*                        -> master's format
//	enc_avif + Accept: image/avif,image/webp,*/*  -> image/avif
//	enc_avif + Accept: image/webp,*/*             -> image/webp
//	enc_avif + Accept: image/png                  -> master's format
//	no enc,   any Accept                          -> the FILENAME's extension
//
// So a client sending Accept: */* never sees AVIF, whatever the URL says.
//
// Without enc_, Accept is ignored ENTIRELY and the filename extension decides
// (§4.1): a PNG master really does serve JPEG for a `.jpg` name. The two rules
// disagree about which encoder runs, so the opt-out is not a no-op. An
// unrecognised or absent extension falls back to the master's own format.
//
// This deliberately does NOT go through clientfeatures.Detector: that gates its
// Accept matching on IMGPROXY_AUTO_WEBP/AVIF, so a deployment with those unset
// would silently never negotiate. The rule here is unconditional.
//
// allowAVIF lets an operator disable the AVIF branch; the encoder settings for
// AVIF were never compared against the CDN (OP-SPEC §11).
func Negotiate(
	enc bool, accept, filename string, master imagetype.Type, allowAVIF bool,
) imagetype.Type {
	if enc {
		if allowAVIF && strings.Contains(accept, "image/avif") {
			return imagetype.AVIF
		}
		if strings.Contains(accept, "image/webp") {
			return imagetype.WEBP
		}
		return master
	}
	if t, ok := FilenameFormat(filename); ok && (t != imagetype.AVIF || allowAVIF) {
		return t
	}
	return master
}

// outputFormats is every format the CDN will encode a rendition into, and so
// every extension the filename rule recognises. It is an ALLOWLIST, not a
// filter over what libvips happens to support: an extension outside this set
// -- `.jxl`, `.tiff`, `.bmp` -- is not an error and not a format request, it
// simply does not decide anything, and the master's own format is used
// instead (§9.3). Answering `.jxl` with JPEG XL would be a capability the CDN
// does not have.
//
// GIF is absent deliberately. The CDN never encodes GIF; it passes a GIF
// master through untransformed (OP-SPEC §9.1), which this table produces
// naturally -- `.gif` falls through to the master's format, and a GIF master's
// format is GIF.
var outputFormats = map[string]imagetype.Type{
	"png":  imagetype.PNG,
	"jpg":  imagetype.JPEG,
	"jpeg": imagetype.JPEG,
	"webp": imagetype.WEBP,
	"avif": imagetype.AVIF,
}

// FilenameFormat maps a rendition filename's extension to an output format.
// The second result is false for an absent, empty or unrecognised extension,
// which the caller must read as "the master decides", never as an error.
func FilenameFormat(filename string) (imagetype.Type, bool) {
	i := strings.LastIndexByte(filename, '.')
	if i < 0 || i == len(filename)-1 {
		return imagetype.Unknown, false
	}
	t, ok := outputFormats[strings.ToLower(filename[i+1:])]
	return t, ok
}

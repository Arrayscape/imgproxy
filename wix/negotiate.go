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
	if t, ok := FilenameFormat(filename); ok {
		return t
	}
	return master
}

// FilenameFormat maps a rendition filename's extension to an output format.
func FilenameFormat(filename string) (imagetype.Type, bool) {
	i := strings.LastIndexByte(filename, '.')
	if i < 0 || i == len(filename)-1 {
		return imagetype.Unknown, false
	}
	return imagetype.GetTypeByName(strings.ToLower(filename[i+1:]))
}

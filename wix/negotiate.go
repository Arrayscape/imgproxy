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
//	no enc,   any Accept                          -> master's format
//
// So a client sending Accept: */* never sees AVIF, whatever the URL says.
//
// This deliberately does NOT go through clientfeatures.Detector: that gates its
// Accept matching on IMGPROXY_AUTO_WEBP/AVIF, so a deployment with those unset
// would silently never negotiate. The rule here is unconditional.
//
// allowAVIF lets an operator disable the AVIF branch; the encoder settings for
// AVIF were never compared against the CDN (OP-SPEC §11).
func Negotiate(enc bool, accept string, master imagetype.Type, allowAVIF bool) imagetype.Type {
	if enc {
		if allowAVIF && strings.Contains(accept, "image/avif") {
			return imagetype.AVIF
		}
		if strings.Contains(accept, "image/webp") {
			return imagetype.WEBP
		}
	}
	return master
}

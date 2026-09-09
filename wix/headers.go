package wix

// Response headers. WIX-URL-SPEC §9.
//
// The header shape is decided by which service answers -- not by format,
// operation, or master type. Two do:
//
//	image-manipulator  every /v1/<op>/... transform
//	media-router       the bare /media/<media-id> original, and /shapes/<id>.svg
const (
	// TransformCacheControl is constant across every op, format, quality and
	// geometry observed. 30 days.
	TransformCacheControl = "public, max-age=2592000, immutable"

	// OriginalCacheControl applies to the bare original. 180 days -- six times
	// a transform's TTL.
	OriginalCacheControl = "public, max-age=15552000, immutable"

	// OriginalMaxAge is OriginalCacheControl's max-age, for computing Expires.
	OriginalMaxAge = 15552000

	// ForbiddenCacheControl accompanies a 403: a nonexistent media id or an
	// unrecognised op, rejected upstream of the image manipulator.
	ForbiddenCacheControl = "no-cache, private, must-revalidate, proxy-revalidate, no-store"

	// BadRequestCacheControl accompanies a 400: a malformed parameter value on
	// an otherwise-valid op, which does reach the image manipulator. Note the
	// different ordering and the absent proxy-revalidate -- a different
	// service composes it.
	BadRequestCacheControl = "private, no-cache, no-store, must-revalidate"
)

// TransformVariesOnAccept reports whether a transform response should carry
// `Vary: Accept`.
//
// Only when the URL opts into negotiation. Its absence is not merely unset:
// without `enc_`, `Accept` is genuinely ignored (§4.1) and no Vary is sent at
// all, because the filename extension alone decides the format.
func TransformVariesOnAccept(enc Encoding) bool { return enc.Enc }

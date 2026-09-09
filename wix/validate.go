package wix

import "fmt"

// ParamError is a malformed parameter VALUE on an op that is itself valid.
//
// It is a distinct condition from an unparseable path: this request routed
// correctly and reached the image manipulator, which is why the CDN answers it
// 400 with a diagnostic body rather than 403 "Forbidden". WIX-URL-SPEC §9.3.
type ParamError struct {
	Op    Op
	Group string // the manipulator's parameter group, e.g. "dimensions"
	Name  string // the human name the CDN prints, e.g. "width"
	Value string // what the client actually sent
}

func (e ParamError) Error() string {
	return fmt.Sprintf("wix: %s: invalid %s %q", e.Op, e.Name, e.Value)
}

// Unwrap makes errors.Is(err, ErrBadParams) true, so callers that only care
// that the parameters were unusable do not have to know this type.
func (e ParamError) Unwrap() error { return ErrBadParams }

// CDNMessage renders the body the CDN sends. One real response is on record:
//
//	(fil) (dimensions) invalid width abc
//
// for a `fill` segment with `w_abc`. Note "fil", not "fill" -- the manipulator
// prints its own op token, not the URL's. Only that one token is measured, so
// opToken carries it and leaves the others as the URL spells them rather than
// generalising a truncation rule from a single sample.
func (e ParamError) CDNMessage() string {
	return fmt.Sprintf("(%s) (%s) invalid %s %s", opToken(e.Op), e.Group, e.Name, e.Value)
}

// opToken maps an op to the token the manipulator prints in a 400 body.
// Measured for fill only; see CDNMessage.
func opToken(op Op) string {
	if op == OpFill {
		return "fil"
	}
	return string(op)
}

// ValidateParams checks every segment's parameters before anything is fetched,
// so a malformed url costs no master download and reports 400 rather than
// failing later inside the geometry as an opaque processing error.
//
// It checks only what the ops actually consume. Unrecognised keys are NOT
// errors: the CDN accepts and ignores them (§3.7), and treating them as errors
// would reject urls the CDN serves.
func ValidateParams(segs []Segment) error {
	for _, s := range segs {
		switch s.Op {
		case OpFit, OpFill:
			if err := requirePositive(s, "w", "width", "dimensions"); err != nil {
				return err
			}
			if err := requirePositive(s, "h", "height", "dimensions"); err != nil {
				return err
			}

		case OpCrop:
			// x and y may be negative -- an out-of-bounds crop is clamped, not
			// rejected -- so they are checked for parseability only.
			if err := requireInt(s, "x", "x", "coordinates"); err != nil {
				return err
			}
			if err := requireInt(s, "y", "y", "coordinates"); err != nil {
				return err
			}
			if err := requirePositive(s, "w", "width", "dimensions"); err != nil {
				return err
			}
			if err := requirePositive(s, "h", "height", "dimensions"); err != nil {
				return err
			}
		}
	}
	return nil
}

func requireInt(s Segment, key, name, group string) error {
	if _, ok := s.Params.Int(key); !ok {
		return ParamError{Op: s.Op, Group: group, Name: name, Value: s.Params[key]}
	}
	return nil
}

func requirePositive(s Segment, key, name, group string) error {
	v, ok := s.Params.Int(key)
	if !ok || v <= 0 {
		return ParamError{Op: s.Op, Group: group, Name: name, Value: s.Params[key]}
	}
	return nil
}

package wix

import (
	"errors"
	"testing"
)

func TestValidateParams(t *testing.T) {
	tests := []struct {
		name    string
		segs    []Segment
		wantMsg string // "" means the url is acceptable
	}{
		// The one body on record from the CDN, reproduced exactly.
		{"fill non-numeric width", []Segment{{Op: OpFill, Params: Params{"w": "abc", "h": "100"}}},
			"(fil) (dimensions) invalid width abc"},
		{"fit zero width", []Segment{{Op: OpFit, Params: Params{"w": "0", "h": "100"}}},
			"(fit) (dimensions) invalid width 0"},
		{"fit negative height", []Segment{{Op: OpFit, Params: Params{"w": "10", "h": "-3"}}},
			"(fit) (dimensions) invalid height -3"},
		{"fit missing height", []Segment{{Op: OpFit, Params: Params{"w": "10"}}},
			"(fit) (dimensions) invalid height "},
		{"crop non-numeric x", []Segment{{Op: OpCrop, Params: Params{"x": "abc", "y": "0", "w": "5", "h": "5"}}},
			"(crop) (coordinates) invalid x abc"},

		{"valid fit", []Segment{{Op: OpFit, Params: Params{"w": "10", "h": "10"}}}, ""},
		// A negative crop origin is clamped, not rejected.
		{"crop negative origin", []Segment{{Op: OpCrop, Params: Params{"x": "-5", "y": "-5", "w": "5", "h": "5"}}}, ""},
		// Unrecognised keys are accepted and ignored (§3.7); rejecting them
		// would refuse urls the CDN serves.
		{"unknown params", []Segment{{Op: OpFit, Params: Params{"w": "10", "h": "10", "lg": "9", "zz": "1"}}}, ""},
		// A later bad segment must still be caught.
		{"second segment bad", []Segment{
			{Op: OpCrop, Params: Params{"x": "0", "y": "0", "w": "5", "h": "5"}},
			{Op: OpFit, Params: Params{"w": "x", "h": "10"}},
		}, "(fit) (dimensions) invalid width x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateParams(tt.segs)
			if tt.wantMsg == "" {
				if err != nil {
					t.Fatalf("ValidateParams = %v; want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("ValidateParams = nil; want an error")
			}
			// Callers that only care that the params were unusable must not
			// have to know the concrete type.
			if !errors.Is(err, ErrBadParams) {
				t.Errorf("errors.Is(err, ErrBadParams) = false; want true")
			}
			var pe ParamError
			if !errors.As(err, &pe) {
				t.Fatalf("errors.As(%v, *ParamError) = false", err)
			}
			if got := pe.CDNMessage(); got != tt.wantMsg {
				t.Errorf("CDNMessage() = %q; want %q", got, tt.wantMsg)
			}
		})
	}
}

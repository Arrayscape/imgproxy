package wix

import (
	"fmt"

	"github.com/imgproxy/imgproxy/v4/vips"
	wixspec "github.com/imgproxy/imgproxy/v4/wix"
)

// Render applies OP-SPEC §5 to an already-decoded image.
//
// The four branches are genuinely different operations, not one operation with
// flags, which is why this is a switch rather than a step list.
func Render(img *vips.Image, src *Source, plan wixspec.Plan, fx wixspec.Effects, profilePath string) error {
	// §3 -- colour first, and only when the master carries a profile. The
	// destination is Wix's own sRGB, not libvips' built-in: on wide-gamut
	// masters the built-in gives 99.472% and this gives 100.000%.
	if src.HasICC && profilePath != "" {
		if err := img.WixICCTransform(profilePath); err != nil {
			return fmt.Errorf("wix: icc_transform: %w", err)
		}
	}

	// The source rectangle, ALWAYS -- the reference emits this extract
	// unconditionally, even for `fit` where it is the whole image.
	if err := img.Crop(plan.NX, plan.NY, plan.HW, plan.HH); err != nil {
		return fmt.Errorf("wix: source extract_area(%d,%d,%d,%d): %w",
			plan.NX, plan.NY, plan.HW, plan.HH, err)
	}

	if plan.Identity {
		// §5.1 -- no resample at all. The premultiply/unpremultiply round trip
		// is lossy wherever alpha is partial, so it is skipped entirely rather
		// than run at scale 1.
		if err := img.WixCopy(); err != nil {
			return fmt.Errorf("wix: copy: %w", err)
		}
		return applyEffects(img, fx)
	}

	alpha := src.HasAlpha

	if alpha {
		if err := img.WixPremultiply(); err != nil {
			return fmt.Errorf("wix: premultiply: %w", err)
		}
	}

	// §4.5 -- the drop. Downscale only, and applied to the ORIGINAL crop:
	// libvips sizes a reduce as ROUND_UINT(in/shrink) where the CDN floors, and
	// the shortfall is made up by handing the resampler fewer pixels.
	if plan.DW != 0 || plan.DH != 0 {
		if err := img.Crop(0, 0, plan.HW-plan.DW, plan.HH-plan.DH); err != nil {
			return fmt.Errorf("wix: drop extract_area: %w", err)
		}
	}

	// The sequential hint is what makes the patched reducev install a line
	// cache sized from VIPS_TILE_HEIGHT. Anything that materialises the
	// pipeline before this point drops it and silently changes the transform,
	// so the pipeline above must never call CopyMemory.
	if !img.IsSequential() {
		return fmt.Errorf(
			"wix: pipeline was materialised before the resize, so reducev will " +
				"ignore VIPS_TILE_HEIGHT and render a different transform")
	}

	if err := img.WixResize(plan.S); err != nil {
		return fmt.Errorf("wix: resize(%v): %w", plan.S, err)
	}

	if alpha {
		if err := img.WixUnpremultiply(); err != nil {
			return fmt.Errorf("wix: unpremultiply: %w", err)
		}
		if err := img.CastUchar(); err != nil {
			return fmt.Errorf("wix: cast uchar: %w", err)
		}
	}

	// The final trim. Zero on the ordinary downscale path; the centred excess
	// on enlargement, fused crop and out-of-bounds crop.
	if err := img.Crop(plan.TX, plan.TY, plan.W, plan.H); err != nil {
		return fmt.Errorf("wix: final extract_area(%d,%d,%d,%d): %w",
			plan.TX, plan.TY, plan.W, plan.H, err)
	}

	return applyEffects(img, fx)
}

// applyEffects runs blur and sharpen.
//
// §6 -- AFTER unpremultiply and after the final extract_area, on the uchar
// image. Doing either in the premultiplied domain is wrong and shows at once
// on masters with alpha.
func applyEffects(img *vips.Image, fx wixspec.Effects) error {
	if fx.Blur > 0 {
		if err := img.WixGaussBlur(fx.Blur); err != nil {
			return fmt.Errorf("wix: gaussblur(%v): %w", fx.Blur, err)
		}
	}
	if u := fx.USM; u != nil {
		// usm_S_A_T -> --sigma S --m2 A --x1 (T*100) --y2 10 --y3 20 --m1 0
		if err := img.WixSharpen(u.EffectiveSigma(), u.Threshold*100, 10, 20, 0, u.Amount); err != nil {
			return fmt.Errorf("wix: sharpen: %w", err)
		}
	}
	return nil
}

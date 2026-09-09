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

	// Everything above is chained lazily -- nothing materialises an
	// intermediate. Beyond the wasted copies, materialising would break the
	// demand coupling that decides reducev's strip boundaries (OP-SPEC §5), so
	// this path must never call CopyMemory.
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

// Derive produces a rendition from a CACHED one rather than from the master.
//
// This is not the §5 pipeline with a different input, which is the natural
// assumption and is wrong (OP-SPEC §10.1). It is a plain resize to the
// target's own dimensions:
//
//	vips resize LEVEL.png OUT.png <W_target/W_level> --vscale <H_target/H_level>
//
// Measured on the CDN's own renditions: resizing a cached level down to a
// target below it reproduces the CDN's rendition at that target pixel-exactly
// (mean|d| 0.0000, max 0) across five level->target pairs.
//
// Everything the master path does around the resample is deliberately absent:
//
//   - No premultiply/unpremultiply, even when the image has alpha.
//   - No §4.5 drop rule.
//   - No sharpen or blur -- callers must not offer an effects-carrying request
//     for derivation at all.
//   - No icc_transform: a cached rendition is already in Wix's sRGB, and
//     transforming it again would move the colours a second time.
//   - No crop. The two axes get INDEPENDENT scales, because a cached level is
//     aspect-preserved and rounded each axis separately when it was made -- on
//     a 1032x24 master the 925 level is 925x21, not 925x24. Forcing a single
//     scale, or forcing a height, produces a stretched image that matches
//     nothing.
func Derive(img *vips.Image, w, h int) error {
	aw, ah := img.Width(), img.Height()
	if aw <= 0 || ah <= 0 {
		return fmt.Errorf("wix: cached ancestor has no area (%dx%d)", aw, ah)
	}
	if w > aw || h > ah {
		// Enlarging from a cached rendition is never right: the master still
		// holds the detail, and falling back to it is always allowed.
		return fmt.Errorf(
			"wix: refusing to enlarge %dx%d ancestor to %dx%d", aw, ah, w, h)
	}

	if w == aw && h == ah {
		// The target IS the ancestor. Resizing by exactly 1.0 still runs the
		// resampler and would not be a no-op in the low bits.
		return nil
	}

	if err := img.WixResizeXY(float64(w)/float64(aw), float64(h)/float64(ah)); err != nil {
		return err
	}

	// libvips derives the output size as round(in * scale). The scales are
	// exact ratios of integers, so this should already hold; check it rather
	// than trust it, because a silent off-by-one here would be served as a
	// correct rendition of the wrong size.
	if img.Width() != w || img.Height() != h {
		return fmt.Errorf("wix: derived %dx%d, wanted %dx%d",
			img.Width(), img.Height(), w, h)
	}
	return nil
}

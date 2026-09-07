package wix

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

//go:embed profiles/wix-srgb.icc.b64
var srgbB64 string

var (
	srgbOnce  sync.Once
	srgbBytes []byte
	srgbErr   error
)

// SRGBProfileSize is the decoded length of Wix's output sRGB profile.
const SRGBProfileSize = 456

// SRGBProfile returns Wix's own output sRGB ICC profile.
//
// Masters carrying an ICC profile are converted to THIS before anything else.
// It is not libvips' built-in sRGB and the difference is measurable: on
// wide-gamut masters vips' built-in gives 99.472% and this gives 100.000%.
// It is a D65 white point with a 42-point sampled TRC, unlike the D50
// parametric profiles lcms and vips ship. OP-SPEC §3.
//
// The .b64 carries `#` comment lines, so a plain base64 decode of the file
// fails; they are stripped first.
func SRGBProfile() ([]byte, error) {
	srgbOnce.Do(func() {
		var sb strings.Builder
		for _, line := range strings.Split(srgbB64, "\n") {
			if strings.HasPrefix(line, "#") {
				continue
			}
			sb.WriteString(strings.TrimSpace(line))
		}
		srgbBytes, srgbErr = base64.StdEncoding.DecodeString(sb.String())
		if srgbErr == nil && len(srgbBytes) != SRGBProfileSize {
			srgbErr = fmt.Errorf(
				"wix: embedded sRGB profile is %d bytes, expected %d",
				len(srgbBytes), SRGBProfileSize)
		}
	})
	return srgbBytes, srgbErr
}

// MaterializeProfile returns a filesystem path holding the sRGB profile.
//
// libvips 8.15 has no way to pass an output profile as a blob -- vips_icc_transform
// takes a filename or a built-in name -- so a file is unavoidable. If preferred
// already holds the right bytes it is used as-is; otherwise the profile is
// written to a temp file, which is what lets tests run outside the container.
func MaterializeProfile(preferred string) (string, error) {
	want, err := SRGBProfile()
	if err != nil {
		return "", err
	}

	if preferred != "" {
		if got, err := os.ReadFile(preferred); err == nil && len(got) == len(want) {
			same := true
			for i := range got {
				if got[i] != want[i] {
					same = false
					break
				}
			}
			if same {
				return preferred, nil
			}
		}
	}

	path := filepath.Join(os.TempDir(), "imgproxy-wix-srgb.icc")
	if got, err := os.ReadFile(path); err == nil && len(got) == len(want) {
		return path, nil
	}
	if err := os.WriteFile(path, want, 0o644); err != nil {
		return "", fmt.Errorf("wix: cannot materialize sRGB profile: %w", err)
	}
	return path, nil
}

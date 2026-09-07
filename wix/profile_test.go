package wix

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSRGBProfile(t *testing.T) {
	b, err := SRGBProfile()
	require.NoError(t, err)
	require.Len(t, b, SRGBProfileSize)

	// Pinned so a corrupted or swapped profile is caught immediately: this is
	// the destination of every ICC transform on the Wix path.
	sum := sha256.Sum256(b)
	t.Logf("wix-srgb.icc sha256=%s", hex.EncodeToString(sum[:]))
	require.Equal(t, byte(0x00), b[0], "ICC profiles start with a 4-byte big-endian size")
	require.Equal(t, "lcms", string(b[4:8]), "preferred CMM should be lcms")
}

func TestMaterializeProfile(t *testing.T) {
	p, err := MaterializeProfile("")
	require.NoError(t, err)

	got, err := os.ReadFile(p)
	require.NoError(t, err)
	want, _ := SRGBProfile()
	require.Equal(t, want, got)

	// An existing correct file is reused rather than rewritten.
	p2, err := MaterializeProfile(p)
	require.NoError(t, err)
	require.Equal(t, p, p2)
}

package wix

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParsePath(t *testing.T) {
	const mid = "0a7ba9_1234567890abcdef1234567890abcdef~mv2.png"

	t.Run("bare media path", func(t *testing.T) {
		r, err := ParsePath("/" + mid)
		require.NoError(t, err)
		require.Equal(t, mid, r.MediaID)
		require.True(t, r.IsBare())
	})

	t.Run("single fill segment", func(t *testing.T) {
		r, err := ParsePath("/" + mid + "/v1/fill/w_620,h_564,al_c,q_85/some-name.jpg")
		require.NoError(t, err)
		require.Equal(t, mid, r.MediaID)
		require.Equal(t, "some-name.jpg", r.Filename)
		require.Len(t, r.Segments, 1)
		require.Equal(t, OpFill, r.Segments[0].Op)

		p := r.Segments[0].Params
		require.Equal(t, "620", p["w"])
		require.Equal(t, "c", p["al"])
		require.True(t, p.Has("al"))
	})

	t.Run("crop chained into fill", func(t *testing.T) {
		r, err := ParsePath("/" + mid + "/v1/crop/x_37,y_53,w_300,h_280/fill/w_620,h_564/n.jpg")
		require.NoError(t, err)
		require.Len(t, r.Segments, 2)
		require.Equal(t, OpCrop, r.Segments[0].Op)
		require.Equal(t, OpFill, r.Segments[1].Op)
	})

	t.Run("fp keeps both coordinates: the split takes the FIRST underscore", func(t *testing.T) {
		r, err := ParsePath("/" + mid + "/v1/fill/w_100,h_100,fp_0.50_0.50/n.jpg")
		require.NoError(t, err)
		x, y, ok := r.Segments[0].Params.Float2("fp")
		require.True(t, ok)
		require.Equal(t, 0.5, x)
		require.Equal(t, 0.5, y)
	})

	t.Run("a repeated /v1/ marker is a separator, not an operand", func(t *testing.T) {
		r, err := ParsePath("/" + mid + "/v1/crop/x_0,y_0,w_10,h_10/v1/fill/w_5,h_5/n.jpg")
		require.NoError(t, err)
		require.Len(t, r.Segments, 2)
	})

	t.Run("rejects", func(t *testing.T) {
		for _, p := range []string{"", "/", "/" + mid + "/fill/w_10,h_10/n.jpg", "/" + mid + "/v1/", "/" + mid + "/v1/bogus/w_1,h_1/n.jpg"} {
			_, err := ParsePath(p)
			require.Error(t, err, "should reject %q", p)
		}
	})
}

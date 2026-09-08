package testutil

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imgproxy/imgproxy/v4/imagetype"
	"github.com/stretchr/testify/require"
)

const (
	// hashPath is a path to hash data in testdata folder
	hashPath = "test-hashes"

	// hashOverlayPathFmt is an OPTIONAL per-libvips-version overlay, consulted
	// before hashPath.
	//
	// Most suites compare processed output BYTE-exactly, so pinning a different
	// libvips changes them: this fork pins 8.15.5 to reproduce the Wix CDN
	// (OP-SPEC.md §1), where upstream builds 8.18.x. Rather than rewrite
	// upstream's fixtures -- which would conflict on every rebase and lose the
	// reference for anyone building against 8.18 -- the entries that genuinely
	// differ live in testdata/test-hashes-vips<major>.<minor>/ and win when
	// present. Everything else still comes from upstream's set.
	hashOverlayPathFmt = "test-hashes-vips%d.%d"

	// If TEST_CREATE_MISSING_HASHES is set, matcher would create missing hash files
	createMissingHashesEnv = "TEST_CREATE_MISSING_HASHES"

	// If this is set, the images are saved to this folder before hash is calculated
	saveTmpImagesPathEnv = "TEST_SAVE_TMP_IMAGES_PATH"
)

// ImageHashCacheMatcher is a helper struct for image hash comparison in tests
type ImageHashCacheMatcher struct {
	hashesPath string

	// overlayPath holds hashes recorded for the linked libvips version, and is
	// empty when there are none. See hashOverlayPathFmt.
	overlayPath         string
	createMissingHashes bool
	saveTmpImagesPath   string
	hashType            ImageHashType
}

// NewImageHashCacheMatcher creates a new ImageHashRegressionMatcher instance
func NewImageHashCacheMatcher(testDataProvider *TestDataProvider, hashType ImageHashType) *ImageHashCacheMatcher {
	hashesPath := testDataProvider.Path(hashPath)
	createMissingHashes := len(os.Getenv(createMissingHashesEnv)) > 0
	saveTmpImagesPath := os.Getenv(saveTmpImagesPathEnv)

	overlayPath := testDataProvider.Path(
		fmt.Sprintf(hashOverlayPathFmt, vipsMajorVersion(), vipsMinorVersion()))
	if st, err := os.Stat(overlayPath); err != nil || !st.IsDir() {
		overlayPath = ""
	}

	return &ImageHashCacheMatcher{
		hashesPath:          hashesPath,
		overlayPath:         overlayPath,
		createMissingHashes: createMissingHashes,
		saveTmpImagesPath:   saveTmpImagesPath,
		hashType:            hashType,
	}
}

// resolveHashPath returns the hash file to compare against: the
// libvips-version overlay when it has an entry, otherwise upstream's.
//
// When creating missing hashes, a new entry is written to the overlay if one
// exists, so a rebaseline never rewrites upstream's fixtures in place.
func (m *ImageHashCacheMatcher) resolveHashPath(t *testing.T, key string) string {
	t.Helper()

	base := m.hashesPath
	if m.overlayPath != "" {
		if p := m.makeTargetPath(t, m.overlayPath, t.Name(), key, "hash"); fileExists(p) {
			return p
		}
		if m.createMissingHashes {
			base = m.overlayPath
		}
	}
	return m.makeTargetPath(t, base, t.Name(), key, "hash")
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// ImageMatches is a testing helper, which accepts image as reader, calculates
// hash and compares it with a hash saved to testdata/test-hashes folder.
func (m *ImageHashCacheMatcher) ImageMatches(t *testing.T, img io.Reader, key string, maxDistance float32) {
	t.Helper()

	// Read image in memory
	buf, err := io.ReadAll(img)
	require.NoError(t, err)

	// Save tmp image if requested
	m.saveTmpImage(t, key, buf)

	// Calculate hash using shared helper
	sourceHash := m.calculateHash(t, buf)

	// Calculate image hash path (create folder if missing)
	hashPath := m.resolveHashPath(t, key)

	// Try to read or create the hash file
	f, err := os.Open(hashPath)
	if os.IsNotExist(err) {
		// If the hash file does not exist, and we are not allowed to create it, fail
		if !m.createMissingHashes {
			require.NoError(
				t, err,
				"failed to read target hash from %s, use %s=true to create it, %s=/some/path to check resulting images",
				hashPath,
				createMissingHashesEnv,
				saveTmpImagesPathEnv,
			)
		}

		// Create missing hash file
		h, hashErr := os.Create(hashPath)
		require.NoError(t, hashErr, "failed to create target hash file %s", hashPath)
		defer h.Close()

		// Dump calculated source hash to this hash file
		hashErr = sourceHash.Dump(h)
		require.NoError(t, hashErr, "failed to write target hash to %s", hashPath)

		t.Logf("Created missing hash in %s", hashPath)
		return
	}

	// Otherwise, if there is no error or error is something else
	require.NoError(t, err)

	// Load image hash from hash file
	targetHash, err := LoadImageHash(f)
	require.NoError(t, err, "failed to load target hash from %s", hashPath)

	// Ensure distance is OK
	distance, err := sourceHash.Distance(targetHash)
	require.NoError(t, err, "failed to calculate hash distance for %s", key)

	require.LessOrEqual(t, distance, maxDistance, "image hashes are too different for %s: distance %f", key, distance)
}

// calculateHash converts image data to RGBA using VIPS and calculates hash
func (m *ImageHashCacheMatcher) calculateHash(t *testing.T, buf []byte) *ImageHash {
	t.Helper()

	// Calculate hash
	hash, err := NewImageHashFromBytes(buf, m.hashType)
	require.NoError(t, err)

	return hash
}

// makeTargetPath creates the target directory and returns file path for saving
// the image or hash.
func (m *ImageHashCacheMatcher) makeTargetPath(
	t *testing.T,
	base, folder, filename, ext string,
) string {
	t.Helper()

	// Create the target directory if it doesn't exist
	targetDir := path.Join(base, folder)
	err := os.MkdirAll(targetDir, 0750)
	require.NoError(t, err, "failed to create %s target directory", targetDir)

	// Replace the extension with the detected one
	filename = filename + "." + ext

	// Create the target file
	targetPath := path.Join(targetDir, filename)

	return targetPath
}

// saveTmpImage saves the provided image data to a temporary file
func (m *ImageHashCacheMatcher) saveTmpImage(t *testing.T, key string, buf []byte) {
	t.Helper()

	if m.saveTmpImagesPath == "" {
		return
	}

	// Detect the image type to get the correct extension
	ext, err := imagetype.Detect(bytes.NewReader(buf), "", "")
	require.NoError(t, err)

	// Put all the test images into the same folder regardless of
	// the test structure (hierarchy).
	tmpImageFileName := strings.ReplaceAll(filepath.Join(t.Name(), key), "/", "_")

	targetPath := m.makeTargetPath(t, m.saveTmpImagesPath, "", tmpImageFileName, ext.String())

	targetFile, err := os.Create(targetPath)
	require.NoError(t, err, "failed to create %s target file %s", saveTmpImagesPathEnv, targetPath)
	defer targetFile.Close()

	// Write the image data to the file
	_, err = io.Copy(targetFile, bytes.NewReader(buf))
	require.NoError(t, err, "failed to write to %s target file %s", saveTmpImagesPathEnv, targetPath)

	t.Logf("Saved temporary image to %s", targetPath)
}

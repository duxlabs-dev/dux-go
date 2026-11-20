package qtbase

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Extractions are read-only; make them writable so TempDir cleanup can remove them.
func writableCache(t *testing.T) string {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Cleanup(func() {
		_ = filepath.WalkDir(cache, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(p, 0o755)
			}
			return nil
		})
	})
	return cache
}

func TestEmbeddedExtractsIntoTheAppCache(t *testing.T) {
	cache := writableCache(t)

	lib, err := Embedded(fstest.MapFS{"lib/" + Soname: {Data: []byte("v1")}}, "lib", "app")()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(cache, "app", "qtbase"), filepath.Dir(filepath.Dir(lib)))
	data, err := os.ReadFile(lib)
	require.NoError(t, err)
	assert.Equal(t, "v1", string(data))

	newer, err := Embedded(fstest.MapFS{"lib/" + Soname: {Data: []byte("v2")}}, "lib", "app")()
	require.NoError(t, err)
	assert.NotEqual(t, lib, newer)
	assert.NoFileExists(t, lib, "an older extraction is pruned")
}

func TestEmbeddedWithoutALibraryIsEmpty(t *testing.T) {
	writableCache(t)
	lib, err := Embedded(fstest.MapFS{"lib/README": {Data: []byte("stub")}}, "lib", "app")()
	require.NoError(t, err)
	assert.Empty(t, lib)
}

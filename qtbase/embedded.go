package qtbase

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/duxlabs-dev/dux-go/shellapp/shellfs"
)

// Embedded is the Locator for a runtime embedded under root in fsys, extracted to <user cache>/<app>/qtbase.
func Embedded(fsys fs.FS, root, app string) Locator {
	return func() (string, error) {
		info, err := fs.Stat(fsys, path.Join(root, Soname))
		if err != nil || info.IsDir() {
			return "", nil
		}
		sub, err := fs.Sub(fsys, root)
		if err != nil {
			return "", err
		}
		userCache, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		baseDir := filepath.Join(userCache, app, "qtbase")
		dir, err := shellfs.Extract(sub, baseDir)
		if err != nil {
			return "", err
		}
		// A running older app keeps its mapped copy after the unlink.
		shellfs.Prune(baseDir, dir)
		return filepath.Join(dir, Soname), nil
	}
}

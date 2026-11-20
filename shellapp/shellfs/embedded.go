package shellfs

import (
	"io/fs"
	"path"
)

// Embedded is a shellapp.Embedded for the UI tree under Root in FS; Entry marks a build that carries one.
type Embedded struct {
	FS    fs.FS
	Root  string
	Entry string
}

func (e Embedded) Available() bool {
	info, err := fs.Stat(e.FS, path.Join(e.Root, e.Entry))
	return err == nil && !info.IsDir()
}

func (e Embedded) Extract(baseDir string) (string, error) {
	sub, err := fs.Sub(e.FS, e.Root)
	if err != nil {
		return "", err
	}
	return Extract(sub, baseDir)
}

func (e Embedded) Prune(baseDir, keep string) { Prune(baseDir, keep) }

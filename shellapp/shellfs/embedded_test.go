package shellfs

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestEmbeddedServesTheTreeUnderRoot(t *testing.T) {
	tree := fstest.MapFS{}
	for name, file := range testFS() {
		tree["dist/"+name] = file
	}
	e := Embedded{FS: tree, Root: "dist", Entry: "shell.qml"}
	if !e.Available() {
		t.Fatal("tree with shell.qml should be available")
	}
	dir, err := e.Extract(tempBase(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Common", "Theme.qml")); err != nil {
		t.Fatalf("extracted tree should be rooted at dist: %v", err)
	}

	stub := Embedded{FS: fstest.MapFS{"dist/README": {Data: []byte("stub")}}, Root: "dist", Entry: "shell.qml"}
	if stub.Available() {
		t.Fatal("stub build without shell.qml should not be available")
	}
}

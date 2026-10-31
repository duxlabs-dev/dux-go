package files

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sortedNames(t *testing.T, raw []string, spec SortSpec) []string {
	t.Helper()
	b := newBuilder(nil, nil)
	entries := make([]Entry, 0, len(raw))
	for _, name := range raw {
		entries = append(entries, Entry{Name: name, nameKey: b.keys.key(name)})
	}
	sortEntries(entries, spec)
	return namesOf(entries)
}

func TestNameOrderIsNaturalAndCaseInsensitive(t *testing.T) {
	raw := []string{
		"file2.txt", "File10.txt", "file1.txt", "FILE20.txt", "file0.txt",
		"a1b", "a10b", "a2b",
		"Ärger.txt", "apple.txt", "Banana.txt",
		"IMG_0001.jpg", "img_10.jpg", "img_2.jpg",
		"10-report.pdf", "3-report.pdf",
	}
	assert.Equal(t, []string{
		"3-report.pdf", "10-report.pdf",
		"a1b", "a2b", "a10b",
		"apple.txt", "Ärger.txt", "Banana.txt",
		"file0.txt", "file1.txt", "file2.txt", "File10.txt", "FILE20.txt",
		"IMG_0001.jpg", "img_2.jpg", "img_10.jpg",
	}, sortedNames(t, raw, SortSpec{Key: SortName}))
}

func TestNameOrderReversed(t *testing.T) {
	assert.Equal(t, []string{"b10", "b9", "a"}, sortedNames(t, []string{"a", "b9", "b10"}, SortSpec{Key: SortName, Desc: true}))
}

func TestSortBySizeMtimeAndType(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "big.txt"), []byte("0123456789"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "small.png"), []byte("0"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(root, "dir"), 0o755))

	old := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(root, "big.txt"), old, old))

	bySize, err := List(root, ListOptions{Sort: SortSpec{Key: SortSize}})
	require.NoError(t, err)
	assert.Equal(t, []string{"dir", "small.png", "big.txt"}, namesOf(bySize))

	byMtime, err := List(root, ListOptions{Sort: SortSpec{Key: SortMtime}})
	require.NoError(t, err)
	assert.Equal(t, "big.txt", namesOf(byMtime)[0])

	byType, err := List(root, ListOptions{Sort: SortSpec{Key: SortType}})
	require.NoError(t, err)
	assert.Equal(t, []string{"small.png", "dir", "big.txt"}, namesOf(byType))

	dirsFirst, err := List(root, ListOptions{Sort: SortSpec{Key: SortType, DirsFirst: true}})
	require.NoError(t, err)
	assert.Equal(t, "dir", namesOf(dirsFirst)[0])
}

func TestParseSortKeyFallsBackToName(t *testing.T) {
	assert.Equal(t, SortSize, ParseSortKey("SIZE"))
	assert.Equal(t, SortMtime, ParseSortKey(" mtime "))
	assert.Equal(t, SortName, ParseSortKey("nonsense"))
}

func TestHiddenLastGroupsAfterVisible(t *testing.T) {
	b := newBuilder(nil, nil)
	entry := func(name string, isDir, hidden bool) Entry {
		return Entry{Name: name, IsDir: isDir, Hidden: hidden, nameKey: b.keys.key(name)}
	}
	entries := []Entry{
		entry(".config", true, true), entry("b.txt", false, false), entry(".bashrc", false, true),
		entry("Music", true, false), entry("a.txt", false, false), entry(".cache", true, true),
		entry("listed", false, true),
	}

	sortEntries(entries, SortSpec{Key: SortName, HiddenLast: true})
	assert.Equal(t, []string{"a.txt", "b.txt", "Music", ".bashrc", ".cache", ".config", "listed"}, namesOf(entries))

	grouped := SortSpec{Key: SortName, DirsFirst: true, HiddenLast: true}
	sortEntries(entries, grouped)
	assert.Equal(t, []string{"Music", ".cache", ".config", "a.txt", "b.txt", ".bashrc", "listed"}, namesOf(entries))

	entries = insertSorted(entries, entry(".zshrc", false, true), grouped)
	assert.Equal(t, []string{"Music", ".cache", ".config", "a.txt", "b.txt", ".bashrc", ".zshrc", "listed"}, namesOf(entries))

	sortEntries(entries, SortSpec{Key: SortName, DirsFirst: true, HiddenLast: true, Desc: true})
	assert.Equal(t, []string{"Music", ".config", ".cache", "b.txt", "a.txt", "listed", ".zshrc", ".bashrc"}, namesOf(entries))
}

func TestInsertSortedKeepsOrder(t *testing.T) {
	b := newBuilder(nil, nil)
	entry := func(name string, isDir bool) Entry {
		return Entry{Name: name, IsDir: isDir, nameKey: b.keys.key(name)}
	}
	spec := SortSpec{Key: SortName, DirsFirst: true}
	entries := []Entry{entry("dir", true), entry("a.txt", false), entry("c.txt", false)}

	entries = insertSorted(entries, entry("b.txt", false), spec)
	entries = insertSorted(entries, entry("aaa", true), spec)
	assert.Equal(t, []string{"aaa", "dir", "a.txt", "b.txt", "c.txt"}, namesOf(entries))
}

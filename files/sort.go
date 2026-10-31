package files

import (
	"bytes"
	"cmp"
	"os"
	"slices"
	"strings"
	"sync"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

const digitPad = 20

type SortKey string

const (
	SortName  SortKey = "name"
	SortSize  SortKey = "size"
	SortMtime SortKey = "mtime"
	SortType  SortKey = "type"
)

type SortSpec struct {
	Key        SortKey
	Desc       bool
	DirsFirst  bool
	HiddenLast bool
}

func ParseSortKey(raw string) SortKey {
	switch SortKey(strings.ToLower(strings.TrimSpace(raw))) {
	case SortSize:
		return SortSize
	case SortMtime:
		return SortMtime
	case SortType:
		return SortType
	default:
		return SortName
	}
}

// nameKeys caches collation keys so re-sorting a listing is a byte compare.
type nameKeys struct {
	mu        sync.Mutex
	collator  *collate.Collator
	buf       collate.Buffer
	populated bool
}

func newNameKeys() *nameKeys {
	return &nameKeys{collator: collate.New(collationLanguage(), collate.Loose)}
}

func (k *nameKeys) key(name string) []byte {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.populated {
		k.buf.Reset()
	}
	k.populated = true
	return slices.Clone(k.collator.KeyFromString(&k.buf, padDigits(name)))
}

// padDigits stands in for collate.Numeric, whose keys sort 0 after every other digit.
func padDigits(name string) string {
	if !strings.ContainsAny(name, "0123456789") {
		return name
	}
	var out strings.Builder
	out.Grow(len(name) + digitPad)
	for i := 0; i < len(name); {
		if name[i] < '0' || name[i] > '9' {
			out.WriteByte(name[i])
			i++
			continue
		}
		end := i
		for end < len(name) && name[end] >= '0' && name[end] <= '9' {
			end++
		}
		if pad := digitPad - (end - i); pad > 0 {
			out.WriteString(strings.Repeat("0", pad))
		}
		out.WriteString(name[i:end])
		i = end
	}
	return out.String()
}

func collationLanguage() language.Tag {
	for _, env := range []string{"LC_ALL", "LC_COLLATE", "LANG"} {
		value := os.Getenv(env)
		if value == "" || value == "C" || value == "POSIX" {
			continue
		}
		if cut := strings.IndexAny(value, ".@"); cut >= 0 {
			value = value[:cut]
		}
		tag, err := language.Parse(strings.ReplaceAll(value, "_", "-"))
		if err != nil {
			continue
		}
		return tag
	}
	return language.Und
}

func sortEntries(entries []Entry, spec SortSpec) {
	slices.SortStableFunc(entries, func(a, b Entry) int { return compareEntries(a, b, spec) })
}

func compareEntries(a, b Entry, spec SortSpec) int {
	if spec.DirsFirst && a.IsDir != b.IsDir {
		if a.IsDir {
			return -1
		}
		return 1
	}
	if spec.HiddenLast && a.Hidden != b.Hidden {
		if a.Hidden {
			return 1
		}
		return -1
	}
	if c := compareBy(a, b, spec.Key); c != 0 {
		if spec.Desc {
			return -c
		}
		return c
	}
	return compareName(a, b)
}

func compareBy(a, b Entry, key SortKey) int {
	switch key {
	case SortSize:
		return cmp.Compare(a.Size, b.Size)
	case SortMtime:
		return cmp.Compare(a.MtimeMs, b.MtimeMs)
	case SortType:
		return cmp.Compare(a.Mime, b.Mime)
	default:
		return compareName(a, b)
	}
}

func compareName(a, b Entry) int {
	if c := bytes.Compare(a.nameKey, b.nameKey); c != 0 {
		return c
	}
	return strings.Compare(a.Name, b.Name)
}

func insertSorted(entries []Entry, e Entry, spec SortSpec) []Entry {
	index, _ := slices.BinarySearchFunc(entries, e, func(x, y Entry) int { return compareEntries(x, y, spec) })
	return slices.Insert(entries, index, e)
}

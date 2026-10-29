package lyrics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	cacheVersion  = 6
	negativeTTL   = 14 * 24 * time.Hour
	upgradeTTL    = 14 * 24 * time.Hour
	entryMaxAge   = 90 * 24 * time.Hour
	pruneInterval = 24 * time.Hour
	pruneSentinel = ".lastprune"
)

type cacheEntry struct {
	Version int      `json:"v"`
	Fetched int64    `json:"fetched"`
	Source  Provider `json:"source,omitempty"`
	Miss    bool     `json:"miss,omitempty"`
	Lyrics
}

func (e *cacheEntry) awaitsWordSync() bool {
	return len(e.Synced) > 0 && !e.wordSynced() && time.Since(time.Unix(e.Fetched, 0)) > upgradeTTL
}

func cacheKey(req Request) string {
	sum := sha256.Sum256([]byte("lyrics/v" + strconv.Itoa(cacheVersion) + "\n" + fold(req.Artist) + "\n" + fold(req.Title) + "\n" + fold(req.Album) + "\n" + strconv.Itoa(req.seconds())))
	return hex.EncodeToString(sum[:])
}

func providerCacheKey(provider Provider, req Request) string {
	key := cacheKey(req)
	if provider == LRCLIB {
		return key
	}
	sum := sha256.Sum256([]byte(string(provider) + "\n" + key))
	return hex.EncodeToString(sum[:])
}

func fold(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

func (c *Client) cachePath(key string) string {
	return filepath.Join(c.cacheDir, key[:2], key+".json")
}

func (c *Client) loadCache(key string) (*cacheEntry, bool) {
	if c.cacheDir == "" {
		return nil, false
	}
	data, err := os.ReadFile(c.cachePath(key))
	if err != nil {
		return nil, false
	}

	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil || entry.Version != cacheVersion {
		return nil, false
	}
	if entry.Miss && time.Since(time.Unix(entry.Fetched, 0)) > negativeTTL {
		return nil, false
	}
	return &entry, true
}

func (c *Client) storeCache(key string, entry *cacheEntry) {
	if c.cacheDir == "" {
		return
	}
	entry.Version = cacheVersion
	entry.Fetched = time.Now().Unix()

	data, err := json.Marshal(entry)
	if err != nil {
		return
	}

	path := c.cachePath(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	os.Rename(tmp.Name(), path)
}

// Prune removes entries older than 90 days. Runs in the background, at most once a day.
func (c *Client) Prune() {
	if c.cacheDir == "" {
		return
	}
	root := c.cacheDir
	sentinel := filepath.Join(root, pruneSentinel)
	if info, err := os.Stat(sentinel); err == nil && time.Since(info.ModTime()) < pruneInterval {
		return
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return
	}
	if err := os.WriteFile(sentinel, nil, 0o644); err != nil {
		return
	}

	go pruneOlderThan(root, time.Now().Add(-entryMaxAge))
}

func pruneOlderThan(root string, cutoff time.Time) {
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			return nil
		}
		os.Remove(path)
		return nil
	})
}

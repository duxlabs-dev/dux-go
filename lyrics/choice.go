package lyrics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Sync is how finely lyrics are timed.
type Sync string

const (
	SyncNone Sync = "none"
	SyncLine Sync = "line"
	SyncWord Sync = "word"
)

func (l *Lyrics) sync() Sync {
	switch {
	case l.wordSynced():
		return SyncWord
	case len(l.Synced) > 0:
		return SyncLine
	}
	return SyncNone
}

// Choice is one source that has lyrics for a track.
type Choice struct {
	Source       Provider    `json:"source"`
	Attribution  Attribution `json:"attribution,omitzero"`
	Sync         Sync        `json:"sync"`
	Instrumental bool        `json:"instrumental,omitempty"`
}

func choiceOf(result *Result) Choice {
	attribution := result.Attribution
	attribution.Name = stripMarkup(attribution.Name)
	attribution.Text = stripMarkup(attribution.Text)
	return Choice{Source: result.Source, Attribution: attribution, Sync: result.sync(), Instrumental: result.Instrumental}
}

// Choices lists every source that has lyrics for the track: the sidecar file, then the
// providers in request order. It waits for each provider, where Lookup stops early.
func (c *Client) Choices(ctx context.Context, req Request) ([]Choice, error) {
	providers, err := c.requested(req)
	if err != nil {
		return nil, err
	}
	choices := make([]Choice, 0, len(providers)+1)
	if sidecar, ok := fromSidecar(req.FileURL); ok {
		choices = append(choices, choiceOf(sidecar))
	}

	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	replies := c.ask(ctx, providers, req)
	results := make([]*Result, len(providers))
collect:
	for range providers {
		select {
		case <-ctx.Done():
			break collect
		case response := <-replies:
			results[response.index] = response.result
		}
	}
	for _, result := range results {
		if result != nil && result.Found {
			choices = append(choices, choiceOf(result))
		}
	}
	return choices, nil
}

// Choose makes Lookup return source's lyrics for the track whenever source has them,
// ahead of a sidecar file and the ranking. Sidecar is a valid source, "" forgets the choice.
func (c *Client) Choose(req Request, source Provider) error {
	path, ok := c.choicePath(req)
	if !ok {
		return errors.New("lyrics: a choice needs a cache dir and a track with artist and title")
	}
	if source == "" {
		err := os.Remove(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(source), 0o644)
}

// Chosen is the source stored by Choose, "" if none.
func (c *Client) Chosen(req Request) Provider {
	path, ok := c.choicePath(req)
	if !ok {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return Provider(strings.TrimSpace(string(data)))
}

// Not keyed by cacheVersion and not a .json file, so a cache format bump and Prune both leave choices alone.
func (c *Client) choicePath(req Request) (string, bool) {
	if c.cacheDir == "" || req.Artist == "" || req.Title == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte("lyrics/choice\n" + trackIdentity(req)))
	key := hex.EncodeToString(sum[:])
	return filepath.Join(c.cacheDir, "choices", key[:2], key), true
}

func (c *Client) lookupChosen(ctx context.Context, providers []namedSource, chosen Provider, req Request) (*Result, bool) {
	index := slices.IndexFunc(providers, func(provider namedSource) bool { return provider.id == chosen })
	if index < 0 {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	result, err := c.lookupProvider(ctx, providers[index], req)
	if err != nil || !result.Found {
		return nil, false
	}
	return result, true
}

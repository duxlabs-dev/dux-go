package lyrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultUserAgent = "dankgo-lyrics (+https://github.com/duxlabs-dev/dux-go)"

var (
	errNotFound = errors.New("lyrics not found")
	errUncached = fmt.Errorf("%w: not cached by provider", errNotFound)
	// Sync claimed but every stamp is zero, LyricsPlus does this for some Apple tracks.
	errUntimedSync = fmt.Errorf("%w: synced lyrics without timing", errNotFound)
)

// StatusError is a non-2xx provider response that is not a plain "no such track".
type StatusError struct {
	Code int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("HTTP %d", e.Code)
}

// Fetcher looks up one track. Empty Lyrics is a miss and gets cached, an error does not.
type Fetcher func(ctx context.Context, req Request) (*Lyrics, error)

// Source is one provider. The built-in providers are Sources too.
type Source struct {
	Attribution Attribution
	// Deprecated: ranking compares the lyrics themselves.
	WordSync bool
	Fetch    Fetcher
}

// A new built-in is a fetch method plus a row here. lrclib has word sync too rarely.
func (c *Client) builtins() map[Provider]Source {
	return map[Provider]Source{
		BetterLyrics: {
			Attribution: Attribution{Name: "Better Lyrics", URL: "https://betterlyrics.org"},
			WordSync:    true,
			Fetch:       c.fetchBetterLyrics,
		},
		Unison: {
			Attribution: Attribution{Name: "Unison", URL: "https://unison.boidu.dev", Text: "Lyrics from Unison (https://unison.boidu.dev)"},
			WordSync:    true,
			Fetch:       c.fetchUnison,
		},
		LyricsPlus: {
			Attribution: Attribution{Name: "LyricsPlus", URL: "https://github.com/ibratabian17/lyricsplus"},
			WordSync:    true,
			Fetch:       c.fetchLyricsPlus,
		},
		KuGou: {
			Attribution: Attribution{Name: "KuGou", URL: "https://www.kugou.com"},
			WordSync:    true,
			Fetch:       c.fetchKuGou,
		},
		LRCLIB: {
			Attribution: Attribution{Name: "LRCLIB", URL: "https://lrclib.net"},
			Fetch:       c.fetchLrclib,
		},
		YouTubeMusic: {
			Attribution: Attribution{Name: "YouTube Music", URL: "https://music.youtube.com"},
			Fetch:       c.fetchYouTubeMusic,
		},
	}
}

func (c *Client) source(provider Provider) (Source, bool) {
	if source, ok := c.sources[provider]; ok {
		return source, true
	}
	if c.resolve == nil {
		return Source{}, false
	}
	return c.resolve(provider)
}

func newHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			DialContext:         dialer.DialContext,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		},
		CheckRedirect: sameOrigin,
	}
}

func sameOrigin(req *http.Request, via []*http.Request) error {
	origin := via[0].URL
	if len(via) >= 5 || req.URL.Scheme != origin.Scheme || req.URL.Host != origin.Host {
		return errors.New("refusing lyrics redirect")
	}
	return nil
}

func (c *Client) get(ctx context.Context, endpoint string, query url.Values, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// kugou search finds nothing for "a - b" when the spaces are sent as '+'
	rawQuery := strings.ReplaceAll(query.Encode(), "+", "%20")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+rawQuery, nil)
	if err != nil {
		return nil, err
	}
	return c.do(req)
}

func (c *Client) do(req *http.Request) ([]byte, error) {
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, classifyStatus(resp.StatusCode)
	}
	return readBounded(resp.Body)
}

// betterlyrics answers 401 for a track its server has not cached yet, which another
// client can change at any time. Only a transient status is worth reporting as an error.
func classifyStatus(code int) error {
	switch code {
	case http.StatusUnauthorized:
		return errUncached
	case http.StatusNotFound, http.StatusForbidden, http.StatusGone:
		return errNotFound
	}
	return &StatusError{Code: code}
}

func readBounded(stream io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(stream, MaxSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxSourceBytes {
		return nil, ErrTooLarge
	}
	return body, nil
}

func (c *Client) getJSON(ctx context.Context, endpoint string, query url.Values, timeout time.Duration, payload any) error {
	body, err := c.get(ctx, endpoint, query, timeout)
	if err != nil {
		return err
	}
	return decodeJSON(body, payload)
}

func decodeJSON(body []byte, payload any) error {
	if err := json.Unmarshal(body, payload); err != nil {
		return fmt.Errorf("invalid lyrics response: %w", err)
	}
	return nil
}

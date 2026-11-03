package weather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

const (
	defaultUserAgent = "dux-go-weather (+https://github.com/duxlabs-dev/dux-go)"
	// MaxResponseBytes caps a provider response, over HTTP or from a Command.
	MaxResponseBytes = 2 << 20
)

// StatusError is a non-2xx provider response.
type StatusError struct {
	Code int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("HTTP %d", e.Code)
}

// Fetcher gets the forecast for one location from one provider.
type Fetcher func(ctx context.Context, req Request) (*Forecast, error)

// Source is one provider. The built-in providers are Sources too.
type Source struct {
	Name  string
	URL   string
	Fetch Fetcher
}

// A new built-in is a fetch method plus a row here.
func (c *Client) builtins() map[Provider]Source {
	return map[Provider]Source{
		OpenMeteo: {Name: "Open-Meteo", URL: "https://open-meteo.com", Fetch: c.fetchOpenMeteo},
		METNorway: {Name: "MET Norway", URL: "https://api.met.no", Fetch: c.fetchMETNorway},
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
	dialer := &net.Dialer{Timeout: 5 * time.Second}
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
		return errors.New("refusing weather redirect")
	}
	return nil
}

func (c *Client) get(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	// A url.Error carries the URL, and with it the location, into logs and the UI.
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return nil, urlErr.Err
	}
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &StatusError{Code: resp.StatusCode}
	}
	return readBounded(resp.Body)
}

func readBounded(stream io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(stream, MaxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxResponseBytes {
		return nil, ErrTooLarge
	}
	return body, nil
}

func decodeJSON(body []byte, payload any) error {
	if err := json.Unmarshal(body, payload); err != nil {
		return fmt.Errorf("invalid weather response: %w", err)
	}
	return nil
}

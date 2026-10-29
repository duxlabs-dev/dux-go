package lyrics

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	rateBurst  = 10
	rateRefill = 0.5
	syncGrace  = 2 * time.Second
)

var ErrRateLimited = errors.New("rate limited")

type rateBucket struct {
	tokens float64
	last   time.Time
}

type Options struct {
	// CacheDir defaults to dankgo/lyrics under os.UserCacheDir.
	CacheDir  string
	UserAgent string
	// DisableUpgrade stops cached line-synced lyrics from being re-requested for word sync.
	DisableUpgrade bool
	// Resolve supplies Sources for providers that are not built in. Built-ins win a clash.
	Resolve func(Provider) (Source, bool)
}

// Client is safe for concurrent use. Rate limits and request dedup are per Client, so share one.
type Client struct {
	cacheDir  string
	userAgent string
	upgrade   bool
	syncGrace time.Duration
	http      *http.Client
	sources   map[Provider]Source
	resolve   func(Provider) (Source, bool)
	lookups   singleflight.Group

	rateMu      sync.Mutex
	rateBuckets map[Provider]*rateBucket
}

func New(opts Options) *Client {
	client := &Client{
		cacheDir:    opts.CacheDir,
		userAgent:   opts.UserAgent,
		upgrade:     !opts.DisableUpgrade,
		syncGrace:   syncGrace,
		http:        newHTTPClient(),
		resolve:     opts.Resolve,
		rateBuckets: map[Provider]*rateBucket{},
	}
	client.sources = client.builtins()
	if client.userAgent == "" {
		client.userAgent = defaultUserAgent
	}
	if client.cacheDir != "" {
		return client
	}
	if dir, err := os.UserCacheDir(); err == nil {
		client.cacheDir = filepath.Join(dir, "dankgo", "lyrics")
	}
	return client
}

// Lookup checks sidecar files, then the cache, then the providers.
// No lyrics is Found false, not an error. No returned text can open an html tag.
func (c *Client) Lookup(ctx context.Context, req Request) (*Result, error) {
	if result, ok := fromSidecar(req.FileURL); ok {
		return result.withoutMarkup(), nil
	}
	if req.Artist == "" || req.Title == "" {
		return &Result{}, nil
	}

	providers := req.Providers
	if providers == nil {
		providers = DefaultProviders()
	}
	seen := make(map[Provider]bool, len(providers))
	ordered := make([]namedSource, 0, len(providers))
	names := make([]string, 0, len(providers))
	for _, provider := range providers {
		if seen[provider] {
			continue
		}
		source, known := c.source(provider)
		if !known {
			return nil, fmt.Errorf("unknown lyrics provider: %s", provider)
		}
		seen[provider] = true
		ordered = append(ordered, namedSource{id: provider, Source: source})
		names = append(names, string(provider))
	}
	if len(ordered) == 0 {
		return &Result{}, nil
	}
	key := fmt.Sprintf("%s/%t/%s", cacheKey(req), req.CacheOnly, strings.Join(names, ","))
	flight := c.lookups.DoChan(key, func() (any, error) {
		return c.lookupProviders(context.WithoutCancel(ctx), ordered, req)
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case response := <-flight:
		if response.Err != nil {
			return nil, response.Err
		}
		return response.Val.(*Result).withoutMarkup(), nil
	}
}

type namedSource struct {
	id Provider
	Source
}

type providerResponse struct {
	index  int
	result *Result
	err    error
}

// lookupProviders races all providers and picks among every result that arrives
// before the deadline. Once one provider has found lyrics, the wait for the rest
// is bounded by syncGrace.
func (c *Client) lookupProviders(ctx context.Context, providers []namedSource, req Request) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	replies := make(chan providerResponse, len(providers))
	for index, provider := range providers {
		go func() {
			result, err := c.lookupProvider(ctx, provider, req)
			replies <- providerResponse{index: index, result: result, err: err}
		}()
	}

	results := make([]providerResponse, len(providers))
	var grace <-chan time.Time
	for remaining := len(providers); remaining > 0; {
		select {
		case <-ctx.Done():
			if best := pick(req, results); best != nil {
				return best, nil
			}
			return nil, ctx.Err()
		case <-grace:
			return pick(req, results), nil
		case response := <-replies:
			results[response.index] = response
			remaining--
		}
		if grace == nil && anyFound(results) {
			grace = time.After(c.syncGrace)
		}
	}
	if best := pick(req, results); best != nil {
		return best, nil
	}
	// A provider that says "no lyrics" outranks one that merely failed to answer.
	var lookupErr error
	for _, response := range results {
		if response.err == nil {
			return &Result{}, nil
		}
		lookupErr = response.err
	}
	return nil, lookupErr
}

func anyFound(results []providerResponse) bool {
	return slices.ContainsFunc(results, func(r providerResponse) bool { return r.result != nil && r.result.Found })
}

func (c *Client) lookupProvider(ctx context.Context, provider namedSource, req Request) (*Result, error) {
	key := providerCacheKey(provider.id, req)
	if entry, ok := c.loadCache(key); ok {
		if c.upgrade && !req.CacheOnly && entry.awaitsWordSync() {
			c.upgradeCache(ctx, key, provider, req, entry)
		}
		return fromCache(entry, provider.Attribution), nil
	}
	if req.CacheOnly {
		return &Result{}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !c.allowRequest(provider.id) {
		return nil, ErrRateLimited
	}
	found, err := provider.Fetch(ctx, req)
	if errors.Is(err, errUncached) {
		return &Result{}, nil
	}
	if err != nil && !errors.Is(err, errNotFound) {
		return nil, err
	}
	if err != nil || found.Empty() {
		c.storeCache(key, &cacheEntry{Miss: true})
		return &Result{}, nil
	}
	c.storeCache(key, &cacheEntry{Source: provider.id, Lyrics: *found})
	return &Result{Found: true, Source: provider.id, Attribution: provider.Attribution, Lyrics: *found}, nil
}

// upgradeCache swaps a line-synced entry for word sync once the provider has it.
// The entry is restamped either way, so the provider is asked once per upgradeTTL.
func (c *Client) upgradeCache(ctx context.Context, key string, provider namedSource, req Request, entry *cacheEntry) {
	if !c.allowRequest(provider.id) {
		return
	}
	found, err := provider.Fetch(ctx, req)
	if err != nil && !errors.Is(err, errNotFound) {
		return
	}
	if err == nil && found.wordSynced() {
		entry.Lyrics = *found
	}
	c.storeCache(key, entry)
}

func fromCache(entry *cacheEntry, attribution Attribution) *Result {
	if entry.Miss {
		return &Result{Cached: true}
	}
	return &Result{Found: true, Source: entry.Source, Cached: true, Attribution: attribution, Lyrics: entry.Lyrics}
}

func (c *Client) allowRequest(provider Provider) bool {
	c.rateMu.Lock()
	defer c.rateMu.Unlock()

	now := time.Now()
	bucket := c.rateBuckets[provider]
	if bucket == nil {
		bucket = &rateBucket{tokens: rateBurst, last: now}
		c.rateBuckets[provider] = bucket
	}
	bucket.tokens = min(rateBurst, bucket.tokens+now.Sub(bucket.last).Seconds()*rateRefill)
	bucket.last = now

	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}

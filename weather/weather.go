package weather

import (
	"context"
	"errors"
	"net/http"
	"time"
)

const (
	providerTimeout = 15 * time.Second
	fillGrace       = 3 * time.Second
)

type Options struct {
	// UserAgent identifies the client, MET Norway's terms require one with contact details.
	UserAgent  string
	HTTPClient *http.Client
	// Resolve supplies Sources for providers that are not built in. Built-ins win a clash.
	Resolve func(Provider) (Source, bool)
	// Now defaults to time.Now.
	Now func() time.Time
	// Location is the zone for providers without zone information, like MET Norway.
	// Defaults to time.Local.
	Location *time.Location
}

// Client is safe for concurrent use.
type Client struct {
	userAgent     string
	http          *http.Client
	sources       map[Provider]Source
	resolve       func(Provider) (Source, bool)
	now           func() time.Time
	zone          *time.Location
	fillGrace     time.Duration
	openMeteoURL  string
	metnoURL      string
	airQualityURL string
}

func New(opts Options) *Client {
	client := &Client{
		userAgent:     opts.UserAgent,
		http:          opts.HTTPClient,
		resolve:       opts.Resolve,
		now:           opts.Now,
		zone:          opts.Location,
		fillGrace:     fillGrace,
		openMeteoURL:  openMeteoURL,
		metnoURL:      metnoURL,
		airQualityURL: airQualityURL,
	}
	client.sources = client.builtins()
	if client.userAgent == "" {
		client.userAgent = defaultUserAgent
	}
	if client.http == nil {
		client.http = newHTTPClient()
	}
	if client.now == nil {
		client.now = time.Now
	}
	if client.zone == nil {
		client.zone = time.Local
	}
	return client
}

type namedSource struct {
	id Provider
	Source
	// fillOnly never becomes the Result's Source, it only fills in.
	fillOnly bool
}

type providerResponse struct {
	index    int
	forecast *Forecast
	err      error
}

// Lookup asks every requested provider at once. The first one in priority order with a
// forecast is the Result's Source, the later ones fill in readings it lacks.
func (c *Client) Lookup(ctx context.Context, req Request) (*Result, error) {
	if !req.validLocation() {
		return nil, ErrInvalidLocation
	}
	providers := req.Providers
	if providers == nil {
		providers = DefaultProviders()
	}
	if len(providers) == 0 {
		return nil, ErrNoProviders
	}

	ordered := make([]namedSource, 0, len(providers))
	seen := make(map[Provider]bool, len(providers))
	for _, provider := range providers {
		// Air quality is asked with Request.AirQuality, it cannot be a provider.
		if seen[provider] || provider == OpenMeteoAirQuality {
			continue
		}
		seen[provider] = true
		source, _ := c.source(provider)
		ordered = append(ordered, namedSource{id: provider, Source: source})
	}
	if len(ordered) == 0 {
		return nil, ErrNoProviders
	}
	// Last, so a provider's own air quality wins.
	if req.AirQuality {
		ordered = append(ordered, namedSource{id: OpenMeteoAirQuality, Source: Source{Fetch: c.fetchAirQuality}, fillOnly: true})
	}

	ctx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	responses := c.race(ctx, ordered, req)

	var failures []*ProviderError
	var result *Result
	for i, response := range responses {
		switch {
		case ordered[i].fillOnly && (response.err != nil || result == nil):
			// Nothing to fill, and its failure is not the providers'.
		case response.err != nil:
			failures = append(failures, &ProviderError{Provider: ordered[i].id, Err: response.err})
		case result == nil:
			result = &Result{Source: ordered[i].id, Forecast: response.forecast}
			result.Forecast.noteReported(result.Forecast)
		case result.Forecast.fill(response.forecast):
			result.Filled = append(result.Filled, ordered[i].id)
		}
	}
	if result == nil {
		return nil, &LookupError{Failures: failures}
	}
	result.Forecast.completeSky(req, c.zone)
	return result, nil
}

// race collects every provider's answer, giving the ones after the primary at most
// fillGrace once it is known. A provider still out when race returns failed.
func (c *Client) race(ctx context.Context, providers []namedSource, req Request) []providerResponse {
	responses := make([]providerResponse, len(providers))
	finished := make([]bool, len(providers))
	replies := make(chan providerResponse, len(providers))
	remaining := 0
	for index, provider := range providers {
		if provider.Fetch == nil {
			responses[index], finished[index] = providerResponse{err: errors.New("unknown weather provider")}, true
			continue
		}
		remaining++
		go func() {
			forecast, err := provider.Fetch(ctx, req)
			switch {
			case err != nil:
			case !provider.fillOnly:
				err = forecast.check()
			case forecast == nil || forecast.Current == nil || forecast.Hourly == nil || forecast.Daily == nil:
				err = errors.New("nothing to fill in")
			}
			replies <- providerResponse{index: index, forecast: forecast, err: err}
		}()
	}

	var grace <-chan time.Time
	for remaining > 0 {
		decided, found := primaryState(providers, responses, finished)
		if decided && !found {
			// Every provider failed, a fill-only answer has nothing to fill.
			return unfinished(responses, finished, errors.New("no forecast to fill in"))
		}
		if grace == nil && found {
			timer := time.NewTimer(c.fillGrace)
			defer timer.Stop()
			grace = timer.C
		}
		select {
		case <-ctx.Done():
			return unfinished(responses, finished, ctx.Err())
		case <-grace:
			return unfinished(responses, finished, errors.New("too slow to fill in"))
		case response := <-replies:
			responses[response.index], finished[response.index] = response, true
			remaining--
		}
	}
	return responses
}

// primaryState is whether the primary is decided, and whether there is one: the first
// provider in order that is not fill-only and has answered with a forecast, every one
// before it having failed.
func primaryState(providers []namedSource, responses []providerResponse, finished []bool) (decided, found bool) {
	for i, response := range responses {
		if providers[i].fillOnly {
			continue
		}
		if !finished[i] {
			return false, false
		}
		if response.err == nil {
			return true, true
		}
	}
	return true, false
}

func unfinished(responses []providerResponse, finished []bool, err error) []providerResponse {
	for i := range responses {
		if !finished[i] {
			responses[i] = providerResponse{err: err}
		}
	}
	return responses
}

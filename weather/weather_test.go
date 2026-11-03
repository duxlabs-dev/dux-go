package weather

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func stub(client *Client, fetch func(context.Context, Provider) (*Forecast, error)) {
	for provider, source := range client.sources {
		source.Fetch = func(ctx context.Context, _ Request) (*Forecast, error) {
			return fetch(ctx, provider)
		}
		client.sources[provider] = source
	}
}

func lookup(client *Client, providers ...Provider) (*Result, error) {
	return client.Lookup(context.Background(), Request{Latitude: 46.95, Longitude: 7.45, Providers: providers})
}

func TestDefaultProviders(t *testing.T) {
	if got := DefaultProviders(); !slices.Equal(got, []Provider{OpenMeteo, METNorway}) {
		t.Errorf("DefaultProviders() = %v", got)
	}
}

func TestLookupPriorityAndFill(t *testing.T) {
	client := New(Options{})
	releaseFirst := make(chan struct{})
	stub(client, func(_ context.Context, provider Provider) (*Forecast, error) {
		forecast := openMeteoFixture()
		switch provider {
		case OpenMeteo:
			<-releaseFirst
			forecast.Current.WindDirection = nil
		case METNorway:
			close(releaseFirst)
			forecast.Current.WindDirection = reading(270)
		}
		return forecast, nil
	})

	result, err := client.Lookup(context.Background(), Request{Latitude: 46.95, Longitude: 7.45})
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != OpenMeteo || !slices.Equal(result.Filled, []Provider{METNorway}) || value(result.Forecast.Current.WindDirection) != 270.0 {
		t.Errorf("result = %s filled by %v", result.Source, result.Filled)
	}
}

// The fill grace starts once the primary is known, not when any provider answers: a
// primary 50 ms behind the second provider, with a 10 ms grace, still wins.
func TestLookupWaitsForSlowerPrimary(t *testing.T) {
	client := New(Options{})
	client.fillGrace = 10 * time.Millisecond
	secondDone := make(chan struct{})
	stub(client, func(_ context.Context, provider Provider) (*Forecast, error) {
		if provider == OpenMeteo {
			<-secondDone
			time.Sleep(50 * time.Millisecond)
		} else {
			defer close(secondDone)
		}
		return openMeteoFixture(), nil
	})

	result, err := lookup(client, OpenMeteo, METNorway)
	if err != nil || result.Source != OpenMeteo {
		t.Fatalf("result = %+v, %v", result, err)
	}
}

// A second provider with nothing the first lacks did not fill anything.
func TestLookupFilledOnlyByContributors(t *testing.T) {
	client := New(Options{})
	stub(client, func(context.Context, Provider) (*Forecast, error) {
		return openMeteoFixture(), nil
	})

	result, err := lookup(client, OpenMeteo, METNorway)
	if err != nil || result.Source != OpenMeteo || result.Filled != nil {
		t.Fatalf("source %s filled by %v, %v", result.Source, result.Filled, err)
	}
}

func TestLookupAsksEachProviderOnce(t *testing.T) {
	client := New(Options{})
	var calls atomic.Int32
	stub(client, func(context.Context, Provider) (*Forecast, error) {
		calls.Add(1)
		return openMeteoFixture(), nil
	})

	if _, err := lookup(client, OpenMeteo, OpenMeteo, OpenMeteo); err != nil || calls.Load() != 1 {
		t.Errorf("%d fetches, %v", calls.Load(), err)
	}
}

// A forecast the shell cannot show counts as the provider failing.
func TestLookupRejectsIncompleteForecasts(t *testing.T) {
	for name, broken := range map[string]func(*Forecast){
		"no hours":  func(f *Forecast) { f.Hourly.Time = nil },
		"no days":   func(f *Forecast) { f.Daily.Time = nil },
		"no hourly": func(f *Forecast) { f.Hourly = nil },
		"no daily":  func(f *Forecast) { f.Daily = nil },
	} {
		client := New(Options{})
		stub(client, func(context.Context, Provider) (*Forecast, error) {
			forecast := openMeteoFixture()
			broken(forecast)
			return forecast, nil
		})
		if _, err := lookup(client, OpenMeteo); err == nil {
			t.Errorf("%s: forecast accepted", name)
		}
	}
}

func TestLookupFallsBack(t *testing.T) {
	client := New(Options{})
	stub(client, func(_ context.Context, provider Provider) (*Forecast, error) {
		if provider == OpenMeteo {
			return nil, &StatusError{Code: http.StatusServiceUnavailable}
		}
		return openMeteoFixture(), nil
	})

	result, err := lookup(client, OpenMeteo, METNorway)
	if err != nil || result.Source != METNorway || result.Filled != nil {
		t.Fatalf("result = %+v, %v", result, err)
	}
}

func TestLookupDoesNotWaitLongForFill(t *testing.T) {
	client := New(Options{})
	client.fillGrace = 10 * time.Millisecond
	canceled := make(chan struct{})
	stub(client, func(ctx context.Context, provider Provider) (*Forecast, error) {
		if provider == METNorway {
			<-ctx.Done()
			close(canceled)
			return nil, ctx.Err()
		}
		return openMeteoFixture(), nil
	})

	started := time.Now()
	result, err := lookup(client, OpenMeteo, METNorway)
	if err != nil || result.Source != OpenMeteo || result.Filled != nil {
		t.Fatalf("result = %+v, %v", result, err)
	}
	// The fill provider never answers, Lookup gives it 10 ms, not its 15 s timeout.
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("Lookup took %s waiting for a fill provider", elapsed)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("slow fill provider was not canceled")
	}
}

func TestLookupNamesEveryFailure(t *testing.T) {
	client := New(Options{})
	stub(client, func(_ context.Context, provider Provider) (*Forecast, error) {
		if provider == OpenMeteo {
			return nil, ErrTooLarge
		}
		return &Forecast{Current: &Current{}}, nil
	})

	_, err := lookup(client, OpenMeteo, METNorway, "nosuch")
	var lookupErr *LookupError
	if !errors.As(err, &lookupErr) || len(lookupErr.Failures) != 3 || !errors.Is(err, ErrTooLarge) {
		t.Fatalf("error = %v", err)
	}
	for _, want := range []string{"openmeteo: weather response too large", "metno: forecast lacks", "nosuch: unknown weather provider"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestLookupResolve(t *testing.T) {
	client := New(Options{Resolve: func(provider Provider) (Source, bool) {
		return Source{Name: "Plugin", Fetch: func(context.Context, Request) (*Forecast, error) { return openMeteoFixture(), nil }}, provider == "plugin"
	}})
	stub(client, func(context.Context, Provider) (*Forecast, error) {
		t.Error("built-in asked for a plugin-only lookup")
		return nil, errors.New("unexpected")
	})

	result, err := lookup(client, "plugin", "plugin")
	if err != nil || result.Source != "plugin" {
		t.Fatalf("result = %+v, %v", result, err)
	}
}

func TestLookupRejectsBadRequests(t *testing.T) {
	client := New(Options{})
	if _, err := lookup(client, []Provider{}...); !errors.Is(err, ErrNoProviders) {
		t.Errorf("empty providers error = %v", err)
	}
	for _, req := range []Request{{Latitude: 91}, {Latitude: -91}, {Longitude: 181}, {Longitude: -181}, {Latitude: math.NaN()}, {Longitude: math.NaN()}} {
		if _, err := client.Lookup(context.Background(), req); !errors.Is(err, ErrInvalidLocation) {
			t.Errorf("%v %v error = %v", req.Latitude, req.Longitude, err)
		}
	}
}

func TestResultJSON(t *testing.T) {
	forecast := openMeteoFixture()
	forecast.Current.SurfacePressure = nil
	body, err := json.Marshal(Result{Source: OpenMeteo, Filled: []Provider{METNorway}, Forecast: forecast})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"source":"openmeteo"`, `"filled":["metno"]`, `"utc_offset_seconds":3600`, `"surface_pressure":null`, `"precipitation_probability_max":[5,null]`, `"time":"2026-11-02T14:15"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("JSON lacks %s: %s", want, body)
		}
	}
	if strings.Contains(string(body), "uv_index") {
		t.Error("a reading the provider lacks entirely was not omitted")
	}
}

func TestBuiltinsOverHTTP(t *testing.T) {
	metno := metnoFixture(t, time.Now().Truncate(time.Hour))
	var openMeteoQuery, metnoAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openmeteo":
			openMeteoQuery = r.URL.RawQuery
			json.NewEncoder(w).Encode(openMeteoFixture())
		case "/metno":
			metnoAgent = r.UserAgent()
			w.Write(metno)
		case "/unavailable":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/large":
			w.Write([]byte(strings.Repeat(" ", MaxResponseBytes+1)))
		}
	}))
	defer server.Close()

	client := New(Options{UserAgent: "test-agent"})
	client.openMeteoURL, client.metnoURL = server.URL+"/openmeteo", server.URL+"/metno"
	result, err := lookup(client, OpenMeteo, METNorway)
	if err != nil || result.Source != OpenMeteo || value(result.Forecast.Current.Temperature) != 7.6 {
		t.Fatalf("result = %+v, %v", result, err)
	}
	if !strings.HasPrefix(openMeteoQuery, "latitude=46.95&longitude=7.45&current=") || !strings.Contains(openMeteoQuery, "timezone=auto") {
		t.Errorf("Open-Meteo query = %s", openMeteoQuery)
	}
	if metnoAgent != "test-agent" {
		t.Errorf("MET Norway user agent = %q", metnoAgent)
	}

	var status *StatusError
	if _, err := client.get(context.Background(), server.URL+"/unavailable"); !errors.As(err, &status) || status.Code != http.StatusServiceUnavailable {
		t.Errorf("503 error = %v", err)
	}
	if _, err := client.get(context.Background(), server.URL+"/large"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized body error = %v", err)
	}
}

func TestRefusesRedirectElsewhere(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("{}"))
	}))
	defer elsewhere.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusFound)
	}))
	defer server.Close()

	if _, err := New(Options{}).get(context.Background(), server.URL); err == nil {
		t.Error("followed a redirect to another host")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestOptionsHTTPClient(t *testing.T) {
	var host string
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		host = req.URL.Host
		body, _ := json.Marshal(openMeteoFixture())
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Request: req}, nil
	})
	client := New(Options{HTTPClient: &http.Client{Transport: transport}})

	result, err := lookup(client, OpenMeteo)
	if err != nil || result.Source != OpenMeteo || host != "api.open-meteo.com" {
		t.Fatalf("result = %+v, %v, host %q", result, err, host)
	}
}

func TestNetworkErrorsLeaveOutTheLocation(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()

	_, err := New(Options{}).get(context.Background(), server.URL+"/?lat=46.9512&lon=7.4505")
	if err == nil || strings.Contains(err.Error(), "46.9512") {
		t.Errorf("error = %v", err)
	}
}

// Four redirects within the provider's host are followed, a fifth is refused. A 3xx
// without a Location is an answer, and not a forecast.
func TestRedirectLimitAndUnfollowedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/hop/"))
		switch {
		case r.URL.Path == "/notmodified":
			w.WriteHeader(http.StatusNotModified)
		case err != nil:
			w.WriteHeader(http.StatusNotFound)
		case hops == 0:
			w.Write([]byte("{}"))
		default:
			http.Redirect(w, r, "/hop/"+strconv.Itoa(hops-1), http.StatusFound)
		}
	}))
	defer server.Close()

	client := New(Options{})
	if _, err := client.get(context.Background(), server.URL+"/hop/4"); err != nil {
		t.Errorf("4 redirects: %v", err)
	}
	if _, err := client.get(context.Background(), server.URL+"/hop/5"); err == nil {
		t.Error("followed a fifth redirect")
	}
	var status *StatusError
	if _, err := client.get(context.Background(), server.URL+"/notmodified"); !errors.As(err, &status) || status.Code != http.StatusNotModified {
		t.Errorf("304 error = %v", err)
	}
}

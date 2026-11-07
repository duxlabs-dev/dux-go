package weather

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// airQualityServer answers like Open-Meteo's air quality API, with a temperature too: that
// API has more current readings, which are not weather and must not be filled in.
func airQualityServer(t *testing.T, handle func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if handle != nil {
			handle(w, r)
			return
		}
		fmt.Fprint(w, airQualityBody)
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

// The hourly PM2.5 of the past 24 hours average 15.3: 18 hours at 14, 6 at 19.2.
const airQualityBody = `{"timezone":"Europe/Zurich","current":{"time":"2026-11-07T15:00","interval":3600,` +
	`"european_aqi":32,"us_aqi":61,"pm2_5":10.8,"pm10":13.2,"temperature_2m":99},` +
	`"hourly":{"pm2_5":[14,14,14,14,14,14,14,14,14,14,14,14,14,14,14,14,14,14,19.2,19.2,19.2,19.2,19.2,19.2]}}`

func airQualityLookup(client *Client, providers ...Provider) (*Result, error) {
	return client.Lookup(context.Background(), Request{Latitude: 46.95, Longitude: 7.45, Providers: providers, AirQuality: true})
}

func TestAirQualityFillsCurrent(t *testing.T) {
	var query string
	server, _ := airQualityServer(t, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		fmt.Fprint(w, airQualityBody)
	})
	client := New(Options{})
	client.airQualityURL = server.URL
	stub(client, func(context.Context, Provider) (*Forecast, error) {
		forecast := openMeteoFixture()
		forecast.Current.Temperature = nil
		return forecast, nil
	})

	result, err := airQualityLookup(client, OpenMeteo)
	if err != nil || result.Source != OpenMeteo {
		t.Fatalf("result = %+v, %v", result, err)
	}
	current := result.Forecast.Current
	got := fmt.Sprint(value(current.EuropeanAQI), value(current.USAQI), value(current.PM25), value(current.PM10), value(current.PM25Mean24h), value(current.Temperature))
	if got != "32 61 10.8 13.2 15.3 <nil>" {
		t.Errorf("air quality, temperature = %s", got)
	}
	if !slices.Equal(result.Filled, []Provider{OpenMeteoAirQuality}) {
		t.Errorf("filled = %v", result.Filled)
	}
	if query != "latitude=46.95&longitude=7.45&current=european_aqi,us_aqi,pm2_5,pm10&hourly=pm2_5&past_hours=24&forecast_hours=0" {
		t.Errorf("query = %s", query)
	}
}

// A provider's own air quality, a plugin's say, wins over Open-Meteo's, which fills the rest.
func TestAirQualityFromAProviderWins(t *testing.T) {
	server, _ := airQualityServer(t, nil)
	client := New(Options{})
	client.airQualityURL = server.URL
	stub(client, func(context.Context, Provider) (*Forecast, error) {
		forecast := openMeteoFixture()
		forecast.Current.EuropeanAQI, forecast.Current.PM25 = reading(12), reading(4)
		return forecast, nil
	})

	result, err := airQualityLookup(client, OpenMeteo)
	if err != nil {
		t.Fatal(err)
	}
	current := result.Forecast.Current
	if got := fmt.Sprint(value(current.EuropeanAQI), value(current.USAQI), value(current.PM25), value(current.PM10)); got != "12 61 4 13.2" {
		t.Errorf("air quality = %s", got)
	}
}

// When every provider has failed, Lookup does not wait for the air quality API.
func TestAirQualityDoesNotDelayAFailure(t *testing.T) {
	server, _ := airQualityServer(t, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	client := New(Options{})
	client.airQualityURL = server.URL
	stub(client, func(context.Context, Provider) (*Forecast, error) {
		return nil, &StatusError{Code: http.StatusBadGateway}
	})

	started := time.Now()
	if _, err := airQualityLookup(client, OpenMeteo, METNorway); err == nil {
		t.Fatal("lookup without a forecast succeeded")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("failure took %s, waiting for the air quality API", elapsed)
	}
}

// Air quality fills after every provider and is listed last.
func TestAirQualityFilledAfterTheProviders(t *testing.T) {
	server, _ := airQualityServer(t, nil)
	client := New(Options{})
	client.airQualityURL = server.URL
	stub(client, func(_ context.Context, provider Provider) (*Forecast, error) {
		forecast := openMeteoFixture()
		if provider == OpenMeteo {
			forecast.Current.WindDirection = nil
		} else {
			forecast.Current.USAQI = reading(5)
		}
		return forecast, nil
	})

	result, err := airQualityLookup(client, OpenMeteo, METNorway)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Filled, []Provider{METNorway, OpenMeteoAirQuality}) || value(result.Forecast.Current.USAQI) != 5.0 {
		t.Errorf("filled by %v, US AQI %v", result.Filled, value(result.Forecast.Current.USAQI))
	}
}

// An answer without values fills nothing. The air quality id in Providers is skipped,
// even when Resolve knows a source by that name.
func TestAirQualityEmptyAndListedAsProvider(t *testing.T) {
	server, calls := airQualityServer(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"current":{}}`)
	})
	var resolved atomic.Int32
	client := New(Options{Resolve: func(Provider) (Source, bool) {
		return Source{Fetch: func(context.Context, Request) (*Forecast, error) {
			resolved.Add(1)
			return openMeteoFixture(), nil
		}}, true
	}})
	client.airQualityURL = server.URL
	stub(client, func(context.Context, Provider) (*Forecast, error) {
		return openMeteoFixture(), nil
	})

	result, err := airQualityLookup(client, OpenMeteoAirQuality, OpenMeteo)
	if err != nil || result.Source != OpenMeteo || result.Filled != nil || calls.Load() != 1 || resolved.Load() != 0 {
		t.Fatalf("result = %+v, %v, %d air quality calls, %d resolved", result, err, calls.Load(), resolved.Load())
	}
}

// Listing only the air quality id is listing no provider.
func TestAirQualityAloneIsNoProvider(t *testing.T) {
	client := New(Options{})
	if _, err := airQualityLookup(client, OpenMeteoAirQuality); !errors.Is(err, ErrNoProviders) {
		t.Errorf("error = %v, want ErrNoProviders", err)
	}
}

// Air quality alone is no forecast. With every provider failed, Lookup fails and names
// the providers only.
func TestAirQualityIsNeverTheSource(t *testing.T) {
	server, _ := airQualityServer(t, nil)
	client := New(Options{})
	client.airQualityURL = server.URL
	stub(client, func(context.Context, Provider) (*Forecast, error) {
		return nil, &StatusError{Code: http.StatusBadGateway}
	})

	_, err := airQualityLookup(client, OpenMeteo, METNorway)
	var lookupErr *LookupError
	if !errors.As(err, &lookupErr) || len(lookupErr.Failures) != 2 {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), string(OpenMeteoAirQuality)) {
		t.Errorf("error names the air quality API: %v", err)
	}
}

func TestAirQualityOnlyWhenAsked(t *testing.T) {
	server, calls := airQualityServer(t, nil)
	client := New(Options{})
	client.airQualityURL = server.URL
	stub(client, func(context.Context, Provider) (*Forecast, error) {
		return openMeteoFixture(), nil
	})

	result, err := lookup(client, OpenMeteo)
	if err != nil || result.Forecast.Current.EuropeanAQI != nil || calls.Load() != 0 {
		t.Fatalf("air quality asked without AirQuality: %d calls, %v", calls.Load(), err)
	}
}

// A failing, broken or slow air quality API leaves the forecast as it is.
func TestAirQualityFailureKeepsTheForecast(t *testing.T) {
	cases := map[string]func(w http.ResponseWriter, r *http.Request){
		"unavailable": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) },
		"not json":    func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "<html>") },
		"no current":  func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"latitude":47}`) },
		"text index":  func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"current":{"european_aqi":"good"}}`) },
		"slow": func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		},
	}
	for name, handle := range cases {
		t.Run(name, func(t *testing.T) {
			server, _ := airQualityServer(t, handle)
			client := New(Options{})
			client.airQualityURL = server.URL
			client.fillGrace = 50 * time.Millisecond
			stub(client, func(context.Context, Provider) (*Forecast, error) {
				return openMeteoFixture(), nil
			})

			started := time.Now()
			result, err := airQualityLookup(client, OpenMeteo)
			if err != nil || result.Source != OpenMeteo || result.Filled != nil || result.Forecast.Current.EuropeanAQI != nil {
				t.Fatalf("result = %+v, %v", result, err)
			}
			if elapsed := time.Since(started); elapsed > 2*time.Second {
				t.Errorf("Lookup waited %s for the air quality API", elapsed)
			}
		})
	}
}

func TestMean24h(t *testing.T) {
	hours := func(count int, at func(int) *float64) []*float64 { return readings(count, at) }
	none := func(int) *float64 { return nil }
	cases := []struct {
		name   string
		hourly []*float64
		want   string
	}{
		{"all 24", hours(24, constant(12.25)), "12.3"},
		{"gaps are left out", append(hours(18, constant(10)), hours(6, none)...), "10"},
		{"fewer than 18 hours", append(hours(17, constant(10)), hours(7, none)...), "<nil>"},
		{"no hours", nil, "<nil>"},
		// Only the last 24: an API sending 30 hours must not stretch the mean.
		{"longer than a day", append(hours(6, constant(1000)), hours(24, constant(8))...), "8"},
		{"overflow", hours(24, constant(1.7e308)), "<nil>"},
	}
	for _, tc := range cases {
		if got := fmt.Sprint(value(mean24h(tc.hourly))); got != tc.want {
			t.Errorf("%s: mean = %s, want %s", tc.name, got, tc.want)
		}
	}
}

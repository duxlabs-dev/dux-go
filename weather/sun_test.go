package weather

import (
	"context"
	"testing"
	"time"
)

func moonDay(t *testing.T, date, zoneName string, lat, lon float64) (rise, set string) {
	t.Helper()
	day, err := time.ParseInLocation(localDate, date, zone(t, zoneName))
	if err != nil {
		t.Fatal(err)
	}
	riseAt, setAt := moonTimes(day, lat, lon)
	if riseAt != nil {
		rise = riseAt.In(day.Location()).Format(localTime)
	}
	if setAt != nil {
		set = setAt.In(day.Location()).Format(localTime)
	}
	return rise, set
}

// near is for moon times, the computed ones are within about 4 minutes.
func near(t *testing.T, got, want string) bool {
	t.Helper()
	return within(got, want, 5*time.Minute)
}

// nearSun is for sun times, the computed ones are within about a minute.
func nearSun(t *testing.T, got, want string) bool {
	t.Helper()
	return within(got, want, 2*time.Minute)
}

func within(got, want string, tolerance time.Duration) bool {
	if got == "" || want == "" {
		return got == want
	}
	a, errA := time.Parse(localTime, got)
	b, errB := time.Parse(localTime, want)
	return errA == nil && errB == nil && a.Sub(b).Abs() <= tolerance
}

// The expected times are Open-Meteo's for the same places.
func TestMoonTimesMatchOpenMeteo(t *testing.T) {
	for _, c := range []struct {
		name, date, zone string
		lat, lon         float64
		rise, set        string
	}{
		{"Bern", "2026-11-03", "Europe/Zurich", 46.9514, 7.4587, "2026-11-03T00:31", "2026-11-03T14:39"},
		{"Bern", "2026-11-10", "Europe/Zurich", 46.9514, 7.4587, "2026-11-10T08:57", "2026-11-10T17:08"},
		{"Bern without moonset", "2026-11-17", "Europe/Zurich", 46.9514, 7.4587, "2026-11-17T13:33", ""},
		{"Longyearbyen without moonrise", "2026-11-04", "Arctic/Longyearbyen", 78.2074, 15.6977, "", "2026-11-04T15:19"},
		{"Longyearbyen polar moon", "2026-11-10", "Arctic/Longyearbyen", 78.2074, 15.6977, "", ""},
	} {
		rise, set := moonDay(t, c.date, c.zone, c.lat, c.lon)
		if !near(t, rise, c.rise) || !near(t, set, c.set) {
			t.Errorf("%s %s: moon %q-%q, want %q-%q", c.name, c.date, rise, set, c.rise, c.set)
		}
	}
}

func TestLookupCompletesSunAndMoon(t *testing.T) {
	bare := func() *Forecast {
		forecast := openMeteoFixture()
		forecast.Daily.Time = []string{"2026-11-03", "2026-11-10"}
		forecast.Daily.Sunrise, forecast.Daily.Sunset = nil, []string{"2026-11-03T17:05", ""}
		return forecast
	}
	withMoon := openMeteoFixture()
	withMoon.Daily.Time = []string{"2026-11-03", "2026-11-10"}
	withMoon.Daily.Sunrise, withMoon.Daily.Sunset = nil, nil
	withMoon.Daily.Moonrise = []string{"2026-11-03T00:30", "2026-11-10T08:58"}
	sources := map[Provider]*Forecast{"plugin": bare(), "moon": withMoon, "alone": bare()}
	client := New(Options{Resolve: func(provider Provider) (Source, bool) {
		forecast, ok := sources[provider]
		return Source{Fetch: func(context.Context, Request) (*Forecast, error) { return forecast, nil }}, ok
	}})

	result, err := client.Lookup(context.Background(), Request{Latitude: 46.9514, Longitude: 7.4587, Providers: []Provider{"plugin", "moon"}})
	if err != nil {
		t.Fatal(err)
	}
	daily := result.Forecast.Daily
	if len(result.Filled) != 1 || daily.Moonrise[1] != "2026-11-10T08:58" || !near(t, daily.Moonset[1], "2026-11-10T17:08") {
		t.Errorf("filled by %v, moon %v-%v", result.Filled, daily.Moonrise, daily.Moonset)
	}
	if daily.Sunset[0] != "2026-11-03T17:05" || !nearSun(t, daily.Sunrise[0], "2026-11-03T07:15") || daily.Sunset[1] == "" {
		t.Errorf("sun %v-%v", daily.Sunrise, daily.Sunset)
	}

	result, err = client.Lookup(context.Background(), Request{Latitude: 46.9514, Longitude: 7.4587, Providers: []Provider{"alone"}})
	if err != nil {
		t.Fatal(err)
	}
	if daily := result.Forecast.Daily; result.Filled != nil || !near(t, daily.Moonrise[0], "2026-11-03T00:31") || !near(t, daily.Moonset[0], "2026-11-03T14:39") {
		t.Errorf("computed moon %v-%v", daily.Moonrise, daily.Moonset)
	}
}

// Open-Meteo has a moonset for Bern at 14:39 on 2026-11-03. A provider that gives that
// day's moonrise but no moonset says there is none, which stays, and is not filled from
// a provider after it either.
func TestReportedMissingMoonTimeStays(t *testing.T) {
	primary := openMeteoFixture()
	primary.Daily.Time = []string{"2026-11-03"}
	primary.Daily.Moonrise, primary.Daily.Moonset = []string{"2026-11-03T00:31"}, []string{""}
	filler := openMeteoFixture()
	filler.Daily.Time = []string{"2026-11-03"}
	filler.Daily.Moonset = []string{"2026-11-03T14:39"}
	sources := map[Provider]*Forecast{"primary": primary, "filler": filler}
	client := New(Options{Resolve: func(provider Provider) (Source, bool) {
		forecast, ok := sources[provider]
		return Source{Fetch: func(context.Context, Request) (*Forecast, error) { return forecast, nil }}, ok
	}})

	result, err := client.Lookup(context.Background(), Request{Latitude: 46.9514, Longitude: 7.4587, Providers: []Provider{"primary", "filler"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := at(result.Forecast.Daily.Moonset, 0); got != "" {
		t.Errorf("moonset %q replaced the provider's none", got)
	}
}

// A provider printing moon arrays with nothing in them has no moon times. Open-Meteo has
// Bern's moon rise at 00:31 and set at 14:39 on 2026-11-03.
func TestEmptyMoonArraysAreComputed(t *testing.T) {
	forecast := openMeteoFixture()
	forecast.Daily.Time = []string{"2026-11-03"}
	forecast.Daily.Moonrise, forecast.Daily.Moonset = []string{""}, []string{""}
	client := New(Options{Resolve: func(Provider) (Source, bool) {
		return Source{Fetch: func(context.Context, Request) (*Forecast, error) { return forecast, nil }}, true
	}})

	result, err := client.Lookup(context.Background(), Request{Latitude: 46.9514, Longitude: 7.4587, Providers: []Provider{"plugin"}})
	if err != nil {
		t.Fatal(err)
	}
	if daily := result.Forecast.Daily; !near(t, at(daily.Moonrise, 0), "2026-11-03T00:31") || !near(t, at(daily.Moonset, 0), "2026-11-03T14:39") {
		t.Errorf("moon %v-%v", daily.Moonrise, daily.Moonset)
	}
}

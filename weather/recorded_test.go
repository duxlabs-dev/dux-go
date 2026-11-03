package weather

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// Recorded on 2026-11-03 at 20:18 UTC for Bern, 46.948 7.4474:
//   - bern-openmeteo.json: Open-Meteo with the package's own query, location zone.
//   - bern-metno.json: MET Norway locationforecast/2.0/complete, meta dropped.
//   - bern-moon-auckland.json: Open-Meteo sun and moon times for Bern in Pacific/Auckland.
var recordedAt = time.Date(2026, 11, 3, 20, 18, 0, 0, time.UTC)

func recording(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func recorded(t *testing.T, name string, payload any) {
	t.Helper()
	if err := json.Unmarshal(recording(t, name), payload); err != nil {
		t.Fatal(err)
	}
}

// recordedClient answers like Open-Meteo and MET Norway did, for a machine in Auckland,
// half a day from Bern.
func recordedClient(t *testing.T, now time.Time) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openmeteo":
			w.Write(recording(t, "bern-openmeteo.json"))
		case "/metno":
			w.Write(recording(t, "bern-metno.json"))
		}
	}))
	t.Cleanup(server.Close)
	client := New(Options{Location: zone(t, "Pacific/Auckland"), Now: func() time.Time { return now }})
	client.openMeteoURL, client.metnoURL = server.URL+"/openmeteo", server.URL+"/metno"
	return client
}

func lookupBern(t *testing.T, client *Client, providers ...Provider) *Result {
	t.Helper()
	result, err := client.Lookup(context.Background(), Request{Latitude: 46.948, Longitude: 7.4474, Providers: providers})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func instants(t *testing.T, times []string, zoneName string) []int64 {
	t.Helper()
	location := zone(t, zoneName)
	unix := make([]int64, len(times))
	for i, local := range times {
		at, err := time.ParseInLocation(localTime, local, location)
		if err != nil {
			t.Fatal(err)
		}
		unix[i] = at.Unix()
	}
	return unix
}

func TestRecordedMETNorwayFilledFromOpenMeteo(t *testing.T) {
	var openMeteo Forecast
	recorded(t, "bern-openmeteo.json", &openMeteo)
	var moon Forecast
	recorded(t, "bern-moon-auckland.json", &moon)

	result := lookupBern(t, recordedClient(t, recordedAt), METNorway, OpenMeteo)
	forecast := result.Forecast
	if result.Source != METNorway || !slices.Equal(result.Filled, []Provider{OpenMeteo}) {
		t.Fatalf("source %s filled by %v", result.Source, result.Filled)
	}

	// MET Norway has no surface pressure. Each Auckland hour must get Open-Meteo's
	// pressure for the Bern hour at the same instant.
	bern := instants(t, openMeteo.Hourly.Time, "Europe/Zurich")
	matched := 0
	for i, at := range instants(t, forecast.Hourly.Time, "Pacific/Auckland") {
		j := slices.Index(bern, at)
		if j < 0 {
			continue
		}
		matched++
		if value(forecast.Hourly.SurfacePressure[i]) != value(openMeteo.Hourly.SurfacePressure[j]) {
			t.Errorf("%s Auckland pressure %v, Open-Meteo has %v for %s Bern", forecast.Hourly.Time[i],
				value(forecast.Hourly.SurfacePressure[i]), value(openMeteo.Hourly.SurfacePressure[j]), openMeteo.Hourly.Time[j])
		}
	}
	if matched < 48 {
		t.Errorf("only %d hours matched", matched)
	}

	// Moon times in Auckland's dates: the first days come from Open-Meteo's Bern
	// forecast, the days after its 7 are computed. Open-Meteo's own Auckland view is the
	// expectation for both.
	for i, date := range forecast.Daily.Time {
		j := slices.Index(moon.Daily.Time, date)
		if j < 0 {
			continue
		}
		for _, field := range []struct {
			name      string
			got, want []string
		}{{"moonrise", forecast.Daily.Moonrise, moon.Daily.Moonrise}, {"moonset", forecast.Daily.Moonset, moon.Daily.Moonset}} {
			got := ""
			if i < len(field.got) {
				got = field.got[i]
			}
			if !near(t, got, field.want[j]) {
				t.Errorf("%s %s = %q, Open-Meteo says %q", date, field.name, got, field.want[j])
			}
		}
	}
}

func TestRecordedOpenMeteoFilledFromMETNorway(t *testing.T) {
	var openMeteo Forecast
	recorded(t, "bern-openmeteo.json", &openMeteo)

	result := lookupBern(t, recordedClient(t, recordedAt), OpenMeteo, METNorway)
	hourly := result.Forecast.Hourly
	if result.Source != OpenMeteo || !slices.Equal(result.Filled, []Provider{METNorway}) {
		t.Fatalf("source %s filled by %v", result.Source, result.Filled)
	}
	for i := range hourly.Time {
		if value(hourly.Temperature[i]) != value(openMeteo.Hourly.Temperature[i]) {
			t.Fatalf("%s temperature %v replaced Open-Meteo's %v", hourly.Time[i], value(hourly.Temperature[i]), value(openMeteo.Hourly.Temperature[i]))
		}
	}

	// Open-Meteo has no hourly is_day, MET Norway's hours fill it. Its hours run from
	// midnight in Auckland, noon in Bern, for about two and a half days, so Bern's
	// morning and Open-Meteo's later days stay null. Where filled, it must agree with
	// Open-Meteo's own sunrise and sunset.
	if len(hourly.IsDay) != len(hourly.Time) || hourly.IsDay[0] != nil || hourly.IsDay[len(hourly.IsDay)-1] != nil {
		t.Fatalf("is_day has %d of %d hours, first %v, last %v", len(hourly.IsDay), len(hourly.Time), value(hourly.IsDay[0]), value(hourly.IsDay[len(hourly.IsDay)-1]))
	}
	filled := 0
	for i, local := range hourly.Time {
		if hourly.IsDay[i] == nil {
			continue
		}
		filled++
		day := slices.Index(openMeteo.Daily.Time, local[:len(localDate)])
		want := 0.0
		if openMeteo.Daily.Sunrise[day] <= local && local < openMeteo.Daily.Sunset[day] {
			want = 1
		}
		if *hourly.IsDay[i] != want {
			t.Errorf("%s is_day %v, Open-Meteo's sun %s-%s", local, *hourly.IsDay[i], openMeteo.Daily.Sunrise[day], openMeteo.Daily.Sunset[day])
		}
	}
	if filled < 48 {
		t.Errorf("only %d hours of is_day filled", filled)
	}
}

// A 25-hour day in Zurich whose only moonrise is in its last hour, and a 23-hour day in
// Auckland without a moonset, the next one at 00:05 the day after. The expected times
// are the US Naval Observatory's (aa.usno.navy.mil/api/rstt/oneday, UTC), in local time.
// Open-Meteo is no reference here: it formats every day with one offset across the change.
func TestMoonOnDSTDays(t *testing.T) {
	for _, c := range []struct {
		zone, date        string
		lat, lon          float64
		moonrise, moonset string
	}{
		// USNO 2026-10-25: rise 22:17, set 11:59. 2026-10-24's rise at 21:56 is the day before.
		{"Europe/Zurich", "2026-10-25", 46.9948, -89.9917, "2026-10-25T23:17", "2026-10-25T12:59"},
		// USNO 2026-09-26: rise 23:29. 2026-09-27: set 11:05, past Auckland's midnight.
		{"Pacific/Auckland", "2026-09-27", -37.0123, -78.0079, "2026-09-27T12:29", ""},
	} {
		t.Run(c.zone, func(t *testing.T) {
			forecast := &Forecast{Timezone: c.zone, Daily: &Daily{Time: []string{c.date}}}
			forecast.completeSky(Request{Latitude: c.lat, Longitude: c.lon}, time.UTC)
			if got := at(forecast.Daily.Moonrise, 0); !near(t, got, c.moonrise) {
				t.Errorf("moonrise %q, USNO says %q", got, c.moonrise)
			}
			if got := at(forecast.Daily.Moonset, 0); !near(t, got, c.moonset) {
				t.Errorf("moonset %q, USNO says %q", got, c.moonset)
			}
		})
	}
}

func at(values []string, i int) string {
	if i >= len(values) {
		return ""
	}
	return values[i]
}

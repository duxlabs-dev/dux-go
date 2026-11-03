package weather

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"
)

type period = map[string]any

func cloudy() period {
	return period{"summary": period{"symbol_code": "cloudy"}, "details": period{}}
}

func metnoStepJSON(at time.Time, temp float64, next1, next6, next12 any) period {
	return period{
		"time": at.UTC().Format(time.RFC3339),
		"data": period{
			"instant":       period{"details": period{"air_temperature": temp, "relative_humidity": 70, "wind_speed": 5, "wind_from_direction": 60}},
			"next_1_hours":  next1,
			"next_6_hours":  next6,
			"next_12_hours": next12,
		},
	}
}

// metnoFixture is hourly for 33 hours from start, then two 6-hourly steps.
func metnoFixture(t *testing.T, start time.Time) []byte {
	t.Helper()
	var series []period
	for h := range 33 {
		next12 := any(cloudy())
		if h == 15 {
			next12 = period{"summary": period{"symbol_code": "rain"}}
		}
		series = append(series, metnoStepJSON(start.Add(time.Duration(h)*time.Hour), float64(5+h%3), cloudy(), cloudy(), next12))
	}
	last := start.Add(32 * time.Hour)
	snow := period{"summary": period{"symbol_code": "lightsnowshowers_night"}, "details": period{"air_temperature_min": -1, "air_temperature_max": 3, "probability_of_precipitation": 40}}
	series = append(series,
		metnoStepJSON(last.Add(6*time.Hour), 2, nil, snow, cloudy()),
		metnoStepJSON(last.Add(12*time.Hour), 4, nil, cloudy(), cloudy()),
	)
	body, err := json.Marshal(period{"properties": period{"timeseries": series}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return location
}

func instant(t *testing.T, value string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

type metnoCase struct {
	lat, lon float64
	start    string
	now      string
	zone     string
}

// metnoClient serves body as MET Norway's answer to a client in zone at now.
func metnoClient(t *testing.T, body []byte, now time.Time, zoneName string) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(body)
	}))
	t.Cleanup(server.Close)
	client := New(Options{Location: zone(t, zoneName), Now: func() time.Time { return now }})
	client.metnoURL = server.URL
	return client
}

func parseMetno(t *testing.T, c metnoCase) *Forecast {
	t.Helper()
	start := instant(t, c.start)
	now := start.Add(20 * time.Minute)
	if c.now != "" {
		now = instant(t, c.now)
	}
	zoneName := c.zone
	if zoneName == "" {
		zoneName = "Europe/Zurich"
	}
	client := metnoClient(t, metnoFixture(t, start), now, zoneName)
	result, err := client.Lookup(context.Background(), Request{Latitude: c.lat, Longitude: c.lon, Providers: []Provider{METNorway}})
	if err != nil {
		t.Fatal(err)
	}
	return result.Forecast
}

func bern(t *testing.T) *Forecast {
	return parseMetno(t, metnoCase{lat: 47, lon: 7.45, start: "2026-11-02T14:00:00Z"})
}

func value(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func TestMetnoCode(t *testing.T) {
	for symbol, want := range map[string]any{
		"clearsky_night": 0.0, "partlycloudy_day": 2.0, "heavyrainshowers_polartwilight": 82.0,
		"lightsleetshowersandthunder_day": 95.0, "sleet": 66.0, "fog": 45.0, "unknown": nil, "": nil,
	} {
		if got := value(metnoCode(symbol)); got != want {
			t.Errorf("metnoCode(%q) = %v, want %v", symbol, got, want)
		}
	}
}

func TestMetnoQueryRoundsToFourDecimals(t *testing.T) {
	if got := metnoQuery(Request{Latitude: 47.0215901, Longitude: 7.4505443}); got != "lat=47.0216&lon=7.4505" {
		t.Errorf("query = %q", got)
	}
}

func TestMetnoHourlyFromLocalMidnight(t *testing.T) {
	hourly := bern(t).Hourly
	if hourly.Time[0] != "2026-11-02T00:00" || hourly.Time[len(hourly.Time)-1] != "2026-11-03T23:00" {
		t.Errorf("hours run %s to %s", hourly.Time[0], hourly.Time[len(hourly.Time)-1])
	}
	if *hourly.Temperature[0] != *hourly.Temperature[15] {
		t.Error("hours before the forecast start do not repeat its first step")
	}
	if *hourly.WindSpeed[15] != 18 {
		t.Errorf("wind = %v km/h, want 18", *hourly.WindSpeed[15])
	}
	if hourly.UVIndex != nil || hourly.SurfacePressure != nil || hourly.Visibility != nil {
		t.Error("MET Norway readings that differ from Open-Meteo's were kept")
	}
}

func TestMetnoCurrentHourLinesUp(t *testing.T) {
	forecast := bern(t)
	current := forecast.Current
	if current.Time != "2026-11-02T15:00" || forecast.Hourly.Time[15] != current.Time {
		t.Errorf("current time %s, hour 15 is %s", current.Time, forecast.Hourly.Time[15])
	}
	if value(current.IsDay) != 1.0 || value(current.RelativeHumidity) != 70.0 || current.SurfacePressure != nil {
		t.Errorf("current = %+v", current)
	}
	if *forecast.UTCOffsetSeconds != 3600 || forecast.Timezone != "Europe/Zurich" {
		t.Errorf("zone = %s %d", forecast.Timezone, *forecast.UTCOffsetSeconds)
	}
}

func TestMetnoDailySummaryAndExtremes(t *testing.T) {
	daily := bern(t).Daily
	if strings.Join(daily.Time, ",") != "2026-11-02,2026-11-03,2026-11-04" {
		t.Fatalf("days = %v", daily.Time)
	}
	if value(daily.WeatherCode[1]) != 63.0 {
		t.Errorf("day 2 code = %v, want the 06:00 step's 12-hour rain", value(daily.WeatherCode[1]))
	}
	if value(daily.TemperatureMin[2]) != -1.0 || value(daily.TemperatureMax[2]) != 4.0 {
		t.Errorf("day 3 range = %v..%v", value(daily.TemperatureMin[2]), value(daily.TemperatureMax[2]))
	}
	// USNO for 47, 7.45 on 2026-11-03: rise 06:16, set 16:11 UTC.
	if !nearSun(t, daily.Sunrise[1], "2026-11-03T07:16") || !nearSun(t, daily.Sunset[1], "2026-11-03T17:11") {
		t.Errorf("sun %s-%s", daily.Sunrise[1], daily.Sunset[1])
	}
}

func TestMetnoSixHourlyPrecipitation(t *testing.T) {
	if got := value(bern(t).Daily.PrecipitationProbabilityMax[2]); got != 40.0 {
		t.Errorf("6-hourly day precipitation = %v, want 40", got)
	}
}

func allHours(hourly *Hourly, isDay float64) bool {
	for _, flag := range hourly.IsDay {
		if *flag != isDay {
			return false
		}
	}
	return true
}

func TestMetnoPolarNight(t *testing.T) {
	forecast := parseMetno(t, metnoCase{lat: 78.22, lon: 15.65, start: "2026-11-02T14:00:00Z"})
	if forecast.Daily.Sunrise[0] != "2026-11-02T00:00" || forecast.Daily.Sunset[0] != "2026-11-02T00:00" {
		t.Errorf("Longyearbyen sun %s-%s", forecast.Daily.Sunrise[0], forecast.Daily.Sunset[0])
	}
	if value(forecast.Current.IsDay) != 0.0 || !allHours(forecast.Hourly, 0) {
		t.Error("polar night has daylight")
	}
}

func TestMetnoMidnightSun(t *testing.T) {
	for name, c := range map[string]metnoCase{
		"Tromsø":  {lat: 69.65, lon: 18.96, start: "2026-06-21T10:00:00Z"},
		"McMurdo": {lat: -77.85, lon: 166.67, start: "2026-12-21T10:00:00Z"},
		// Polar day must not depend on the machine's zone.
		"Longyearbyen from Auckland": {lat: 78.22, lon: 15.65, start: "2026-04-20T10:00:00Z", zone: "Pacific/Auckland"},
	} {
		t.Run(name, func(t *testing.T) {
			forecast := parseMetno(t, c)
			day, _ := time.Parse(localTime, forecast.Daily.Sunrise[0])
			if forecast.Daily.Sunrise[0] != forecast.Daily.Time[0]+"T00:00" || forecast.Daily.Sunset[0] != day.AddDate(0, 0, 1).Format(localTime) {
				t.Errorf("sun %s-%s", forecast.Daily.Sunrise[0], forecast.Daily.Sunset[0])
			}
			if value(forecast.Current.IsDay) != 1.0 || !allHours(forecast.Hourly, 1) {
				t.Error("midnight sun has night")
			}
		})
	}
}

func TestMetnoEquatorDayAndNight(t *testing.T) {
	forecast := parseMetno(t, metnoCase{lat: -3.12, lon: -60.02, start: "2026-11-02T14:00:00Z", zone: "America/Manaus"})
	// USNO for -3.12, -60.02 on 2026-11-02: rise 09:37, set 21:50 UTC.
	if !nearSun(t, forecast.Daily.Sunrise[0], "2026-11-02T05:37") || !nearSun(t, forecast.Daily.Sunset[0], "2026-11-02T17:50") {
		t.Errorf("Manaus sun %s-%s", forecast.Daily.Sunrise[0], forecast.Daily.Sunset[0])
	}
	if value(forecast.Current.IsDay) != 1.0 || allHours(forecast.Hourly, 0) || allHours(forecast.Hourly, 1) {
		t.Error("Manaus has no day and night")
	}
}

func TestMetnoMachineZoneFarFromLocation(t *testing.T) {
	forecast := parseMetno(t, metnoCase{lat: 46.95, lon: 7.45, start: "2026-11-02T00:00:00Z", now: "2026-11-02T11:20:00Z", zone: "Pacific/Auckland"})
	isDay := map[string]float64{}
	for i, local := range forecast.Hourly.Time {
		isDay[local] = *forecast.Hourly.IsDay[i]
	}
	// Machine time 12:00, 20:00 and 00:00 are 00:00, 08:00 and 12:00 in Bern.
	if isDay["2026-11-02T12:00"] != 0 || isDay["2026-11-02T20:00"] != 1 || isDay["2026-11-03T00:00"] != 1 {
		t.Errorf("is_day = %v %v %v", isDay["2026-11-02T12:00"], isDay["2026-11-02T20:00"], isDay["2026-11-03T00:00"])
	}
	if value(forecast.Current.IsDay) != 1.0 {
		t.Error("Bern at noon is night")
	}
}

func TestMetnoRejectsEmptyTimeseries(t *testing.T) {
	client := metnoClient(t, []byte(`{"properties":{"timeseries":[]}}`), time.Now(), "UTC")
	if _, err := lookup(client, METNorway); err == nil || !strings.Contains(err.Error(), "no timeseries") {
		t.Error("empty timeseries accepted")
	}
}

// The fixture's step h has air temperature 5 + h%3. At 18:10 UTC the current step is the
// one from 18:00, h = 4, 19:00 in Zurich.
func TestMetnoCurrentIsTheStepNow(t *testing.T) {
	forecast := parseMetno(t, metnoCase{lat: 47, lon: 7.45, start: "2026-11-02T14:00:00Z", now: "2026-11-02T18:10:00Z"})
	if forecast.Current.Time != "2026-11-02T19:00" || value(forecast.Current.Temperature) != 6.0 {
		t.Errorf("current %s at %v°C", forecast.Current.Time, value(forecast.Current.Temperature))
	}
}

// Step 64 of the recording, 2026-11-06T12:00Z, is 6-hourly: no next_1_hours, next_6_hours
// is partlycloudy_day, WMO code 2, at 8.8°C.
func TestRecordedMetnoSixHourlyCurrent(t *testing.T) {
	client := recordedClient(t, time.Date(2026, 11, 6, 12, 20, 0, 0, time.UTC))
	current := lookupBern(t, client, METNorway).Forecast.Current
	if current.Time != "2026-11-07T01:00" || value(current.WeatherCode) != 2.0 || value(current.Temperature) != 8.8 {
		t.Errorf("current %s code %v at %v°C", current.Time, value(current.WeatherCode), value(current.Temperature))
	}
}

package weather

import (
	"errors"
	"fmt"
	"math"
	"time"
)

type Provider string

const (
	OpenMeteo Provider = "openmeteo"
	METNorway Provider = "metno"
	// OpenMeteoAirQuality only fills air quality, asked for with Request.AirQuality. It is
	// never a Result's Source and not a provider to list in Request.Providers.
	OpenMeteoAirQuality Provider = "openmeteo_air_quality"
)

// DefaultProviders is used when Request.Providers is nil.
func DefaultProviders() []Provider {
	return []Provider{OpenMeteo, METNorway}
}

var (
	ErrNoProviders     = errors.New("no weather providers requested")
	ErrInvalidLocation = errors.New("invalid weather location")
	ErrTooLarge        = errors.New("weather response too large")
)

type Request struct {
	Latitude  float64
	Longitude float64
	// Providers in priority order. nil = DefaultProviders, empty non-nil = ErrNoProviders.
	Providers []Provider
	// AirQuality also asks Open-Meteo's air quality API, for the Current air quality
	// readings the providers lack.
	AirQuality bool
}

func (r Request) validLocation() bool {
	// NaN fails both comparisons.
	return math.Abs(r.Latitude) <= 90 && math.Abs(r.Longitude) <= 180
}

type Result struct {
	// Source is the provider whose forecast is used.
	Source Provider `json:"source"`
	// Filled are the providers that filled in readings Source lacked, in priority order.
	Filled   []Provider `json:"filled,omitempty"`
	Forecast *Forecast  `json:"forecast"`
}

// Forecast is the Open-Meteo forecast response, reduced to the fields Dux Shell reads.
// Times are local wall-clock times without a zone, "2006-01-02T15:04". A nil reading
// is missing and marshals as null.
type Forecast struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Timezone  string  `json:"timezone,omitempty"`
	// UTCOffsetSeconds is the offset of the zone the times are in. nil = unknown.
	UTCOffsetSeconds *int     `json:"utc_offset_seconds,omitempty"`
	Current          *Current `json:"current"`
	Hourly           *Hourly  `json:"hourly"`
	Daily            *Daily   `json:"daily"`
	// zone is the zone the times are in, when the provider knows it beyond Timezone.
	zone *time.Location
	// reported holds, per Daily.times field, the dates some provider gave moon times for.
	// An empty value on such a date means there is no such event, not that it is unknown.
	reported [4]map[string]bool
}

type Current struct {
	Time                string   `json:"time"`
	Temperature         *float64 `json:"temperature_2m"`
	ApparentTemperature *float64 `json:"apparent_temperature"`
	RelativeHumidity    *float64 `json:"relative_humidity_2m"`
	IsDay               *float64 `json:"is_day"`
	WeatherCode         *float64 `json:"weather_code"`
	SurfacePressure     *float64 `json:"surface_pressure"`
	WindSpeed           *float64 `json:"wind_speed_10m"`
	WindDirection       *float64 `json:"wind_direction_10m"`
	// Air quality: the European (EAQI) and US index, particulate matter in µg/m³.
	EuropeanAQI *float64 `json:"european_aqi"`
	USAQI       *float64 `json:"us_aqi"`
	PM25        *float64 `json:"pm2_5"`
	PM10        *float64 `json:"pm10"`
	// PM25Mean24h is the mean PM2.5 of the past 24 hours, what the WHO guideline is for.
	PM25Mean24h *float64 `json:"pm2_5_mean_24h"`
}

// Hourly readings are indexed like Time. A reading the provider lacks entirely is omitted.
type Hourly struct {
	Time                     []string   `json:"time"`
	Temperature              []*float64 `json:"temperature_2m,omitempty"`
	ApparentTemperature      []*float64 `json:"apparent_temperature,omitempty"`
	RelativeHumidity         []*float64 `json:"relative_humidity_2m,omitempty"`
	DewPoint                 []*float64 `json:"dew_point_2m,omitempty"`
	WeatherCode              []*float64 `json:"weather_code,omitempty"`
	PrecipitationProbability []*float64 `json:"precipitation_probability,omitempty"`
	WindSpeed                []*float64 `json:"wind_speed_10m,omitempty"`
	SurfacePressure          []*float64 `json:"surface_pressure,omitempty"`
	Visibility               []*float64 `json:"visibility,omitempty"`
	UVIndex                  []*float64 `json:"uv_index,omitempty"`
	IsDay                    []*float64 `json:"is_day,omitempty"`
}

// Daily readings are indexed like Time, which holds dates, "2006-01-02".
type Daily struct {
	Time                        []string   `json:"time"`
	Sunrise                     []string   `json:"sunrise,omitempty"`
	Sunset                      []string   `json:"sunset,omitempty"`
	Moonrise                    []string   `json:"moonrise,omitempty"`
	Moonset                     []string   `json:"moonset,omitempty"`
	TemperatureMax              []*float64 `json:"temperature_2m_max,omitempty"`
	TemperatureMin              []*float64 `json:"temperature_2m_min,omitempty"`
	WeatherCode                 []*float64 `json:"weather_code,omitempty"`
	PrecipitationProbabilityMax []*float64 `json:"precipitation_probability_max,omitempty"`
}

// check rejects a forecast the shell cannot show.
func (f *Forecast) check() error {
	switch {
	case f == nil:
		return errors.New("no forecast")
	case f.Current == nil, f.Hourly == nil, f.Daily == nil:
		return errors.New("forecast lacks current, hourly or daily data")
	case len(f.Hourly.Time) == 0, len(f.Daily.Time) == 0:
		return errors.New("forecast has no hours or days")
	}
	return nil
}

// ProviderError is one provider's failure.
type ProviderError struct {
	Provider Provider
	Err      error
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("%s: %v", e.Provider, e.Err)
}

func (e *ProviderError) Unwrap() error {
	return e.Err
}

// LookupError is returned when every provider failed.
type LookupError struct {
	Failures []*ProviderError
}

func (e *LookupError) Error() string {
	message := "no weather forecast"
	for i, failure := range e.Failures {
		separator := "; "
		if i == 0 {
			separator = ": "
		}
		message += separator + failure.Error()
	}
	return message
}

func (e *LookupError) Unwrap() []error {
	errs := make([]error, len(e.Failures))
	for i, failure := range e.Failures {
		errs[i] = failure
	}
	return errs
}

func reading(value float64) *float64 {
	return &value
}

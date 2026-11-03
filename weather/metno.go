package weather

import (
	"context"
	"errors"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	metnoURL   = "https://api.met.no/weatherapi/locationforecast/2.0/complete"
	localTime  = "2006-01-02T15:04"
	localDate  = "2006-01-02"
	kmhPerMs   = 3.6
	firstLight = 6
)

// MET Norway symbol codes without the _day, _night or _polartwilight suffix, as WMO codes.
var metnoCodes = map[string]float64{
	"clearsky":          0,
	"fair":              1,
	"partlycloudy":      2,
	"cloudy":            3,
	"fog":               45,
	"lightrainshowers":  80,
	"rainshowers":       81,
	"heavyrainshowers":  82,
	"lightrain":         61,
	"rain":              63,
	"heavyrain":         65,
	"lightsleetshowers": 80,
	"sleetshowers":      80,
	"heavysleetshowers": 82,
	"lightsleet":        66,
	"sleet":             66,
	"heavysleet":        67,
	"lightsnowshowers":  85,
	"snowshowers":       85,
	"heavysnowshowers":  86,
	"lightsnow":         71,
	"snow":              73,
	"heavysnow":         75,
}

type metnoResponse struct {
	Properties struct {
		Timeseries []metnoStep `json:"timeseries"`
	} `json:"properties"`
}

type metnoStep struct {
	Time string `json:"time"`
	Data struct {
		Instant struct {
			Details metnoDetails `json:"details"`
		} `json:"instant"`
		Next1Hours  *metnoPeriod `json:"next_1_hours"`
		Next6Hours  *metnoPeriod `json:"next_6_hours"`
		Next12Hours *metnoPeriod `json:"next_12_hours"`
	} `json:"data"`
	at time.Time
}

type metnoPeriod struct {
	Summary struct {
		SymbolCode string `json:"symbol_code"`
	} `json:"summary"`
	Details metnoDetails `json:"details"`
}

type metnoDetails struct {
	AirTemperature             *float64 `json:"air_temperature"`
	ApparentAirTemperature     *float64 `json:"apparent_air_temperature"`
	RelativeHumidity           *float64 `json:"relative_humidity"`
	DewPointTemperature        *float64 `json:"dew_point_temperature"`
	WindSpeed                  *float64 `json:"wind_speed"`
	WindFromDirection          *float64 `json:"wind_from_direction"`
	AirTemperatureMax          *float64 `json:"air_temperature_max"`
	AirTemperatureMin          *float64 `json:"air_temperature_min"`
	ProbabilityOfPrecipitation *float64 `json:"probability_of_precipitation"`
}

// MET Norway rejects more than four decimals and anonymous clients, the User-Agent is set in get.
func metnoQuery(req Request) string {
	return "lat=" + strconv.FormatFloat(req.Latitude, 'f', 4, 64) + "&lon=" + strconv.FormatFloat(req.Longitude, 'f', 4, 64)
}

func (c *Client) fetchMETNorway(ctx context.Context, req Request) (*Forecast, error) {
	body, err := c.get(ctx, c.metnoURL+"?"+metnoQuery(req))
	if err != nil {
		return nil, err
	}
	return fromMETNorway(body, req, c.now(), c.zone)
}

func metnoCode(symbol string) *float64 {
	name, _, _ := strings.Cut(symbol, "_")
	if strings.Contains(name, "thunder") {
		return reading(95)
	}
	if code, ok := metnoCodes[name]; ok {
		return reading(code)
	}
	return nil
}

func (s *metnoStep) symbol(periods ...*metnoPeriod) *float64 {
	for _, period := range periods {
		if period != nil && period.Summary.SymbolCode != "" {
			return metnoCode(period.Summary.SymbolCode)
		}
	}
	return nil
}

func (s *metnoStep) code() *float64 {
	return s.symbol(s.Data.Next1Hours, s.Data.Next6Hours)
}

func (s *metnoStep) precipitationProbability() *float64 {
	if p := s.Data.Next1Hours; p != nil && p.Details.ProbabilityOfPrecipitation != nil {
		return p.Details.ProbabilityOfPrecipitation
	}
	if p := s.Data.Next6Hours; p != nil {
		return p.Details.ProbabilityOfPrecipitation
	}
	return nil
}

func (s *metnoStep) nextHourProbability() *float64 {
	if s.Data.Next1Hours == nil {
		return nil
	}
	return s.Data.Next1Hours.Details.ProbabilityOfPrecipitation
}

func (s *metnoStep) sixHourly() metnoDetails {
	if s.Data.Next6Hours == nil {
		return metnoDetails{}
	}
	return s.Data.Next6Hours.Details
}

func kmh(speed *float64) *float64 {
	if speed == nil {
		return nil
	}
	return reading(*speed * kmhPerMs)
}

func dayFlag(day bool) *float64 {
	if day {
		return reading(1)
	}
	return reading(0)
}

func midnight(t time.Time) time.Time {
	year, month, day := t.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, t.Location())
}

func nextMidnight(day time.Time) time.Time {
	return midnight(day.Add(36 * time.Hour))
}

// fromMETNorway maps a locationforecast/2.0/complete response into the Open-Meteo shape.
// MET Norway has no zone information, so the times are in zone, the machine's zone.
func fromMETNorway(body []byte, req Request, now time.Time, zone *time.Location) (*Forecast, error) {
	var response metnoResponse
	if err := decodeJSON(body, &response); err != nil {
		return nil, err
	}
	series := make([]metnoStep, 0, len(response.Properties.Timeseries))
	for _, step := range response.Properties.Timeseries {
		at, err := time.Parse(time.RFC3339, step.Time)
		if err != nil {
			continue
		}
		step.at = at.In(zone)
		series = append(series, step)
	}
	if len(series) == 0 {
		return nil, errors.New("MET Norway returned no timeseries")
	}

	_, offset := now.In(zone).Zone()
	return &Forecast{
		Latitude:         req.Latitude,
		Longitude:        req.Longitude,
		Timezone:         zoneName(zone, now),
		UTCOffsetSeconds: &offset,
		Current:          metnoCurrent(series, req, now),
		Hourly:           metnoHourly(series, req),
		Daily:            metnoDaily(series, req),
		zone:             zone,
	}, nil
}

func metnoCurrent(series []metnoStep, req Request, now time.Time) *Current {
	first := &series[0]
	for i := range series {
		if !series[i].at.After(now) && now.Before(series[i].at.Add(time.Hour)) {
			first = &series[i]
			break
		}
	}
	// No surface pressure: MET Norway's is at sea level, unlike Open-Meteo's.
	details := first.Data.Instant.Details
	return &Current{
		Time:                first.at.Format(localTime),
		Temperature:         details.AirTemperature,
		ApparentTemperature: details.ApparentAirTemperature,
		RelativeHumidity:    details.RelativeHumidity,
		IsDay:               dayFlag(sunUp(now, req.Latitude, req.Longitude)),
		WeatherCode:         first.code(),
		WindSpeed:           kmh(details.WindSpeed),
		WindDirection:       details.WindFromDirection,
	}
}

// Hours are indexed from local midnight. The forecast starts at the current hour, so the
// hours before it repeat the first step. Hourly steps run out after about two days, then
// become 6-hourly, which is where the hourly forecast ends. No UV index: MET Norway's
// assumes a clear sky, unlike Open-Meteo's.
func metnoHourly(series []metnoStep, req Request) *Hourly {
	steps := map[int64]*metnoStep{series[0].at.Unix(): &series[0]}
	last := series[0].at
	for i := 1; i < len(series) && series[i].at.Sub(series[i-1].at) == time.Hour; i++ {
		steps[series[i].at.Unix()] = &series[i]
		last = series[i].at
	}

	hourly := &Hourly{}
	for at := midnight(series[0].at); !at.After(last); at = at.Add(time.Hour) {
		step, ok := steps[at.Unix()]
		if !ok {
			step = &series[0]
		}
		details := step.Data.Instant.Details
		hourly.Time = append(hourly.Time, at.Format(localTime))
		hourly.Temperature = append(hourly.Temperature, details.AirTemperature)
		hourly.ApparentTemperature = append(hourly.ApparentTemperature, details.ApparentAirTemperature)
		hourly.RelativeHumidity = append(hourly.RelativeHumidity, details.RelativeHumidity)
		hourly.DewPoint = append(hourly.DewPoint, details.DewPointTemperature)
		hourly.WindSpeed = append(hourly.WindSpeed, kmh(details.WindSpeed))
		hourly.WeatherCode = append(hourly.WeatherCode, step.code())
		hourly.PrecipitationProbability = append(hourly.PrecipitationProbability, step.nextHourProbability())
		// Per hour, so day and night stay right when the machine's zone is far from the location's.
		hourly.IsDay = append(hourly.IsDay, dayFlag(sunUp(at, req.Latitude, req.Longitude)))
	}
	return hourly
}

func metnoDaily(series []metnoStep, req Request) *Daily {
	daily := &Daily{}
	first := midnight(series[0].at)
	end := series[len(series)-1].at
	for day := first; !day.After(end); day = nextMidnight(day) {
		next := nextMidnight(day)
		var steps []*metnoStep
		for i := range series {
			if !series[i].at.Before(day) && series[i].at.Before(next) {
				steps = append(steps, &series[i])
			}
		}
		// The 06:00 step's 12-hour symbol is MET Norway's daytime summary. A trailing
		// day with only its midnight step would show a night forecast.
		summary := daySummary(steps, day.Equal(first))
		if summary == nil {
			break
		}
		var highs, lows, chances []*float64
		for _, step := range steps {
			sixHourly := step.sixHourly()
			highs = append(highs, step.Data.Instant.Details.AirTemperature, sixHourly.AirTemperatureMax)
			lows = append(lows, step.Data.Instant.Details.AirTemperature, sixHourly.AirTemperatureMin)
			chances = append(chances, step.precipitationProbability())
		}
		sunrise, sunset := sunriseSunset(day, req.Latitude, req.Longitude)
		daily.Time = append(daily.Time, day.Format(localDate))
		daily.Sunrise = append(daily.Sunrise, sunrise)
		daily.Sunset = append(daily.Sunset, sunset)
		daily.TemperatureMax = append(daily.TemperatureMax, extreme(highs, math.Max))
		daily.TemperatureMin = append(daily.TemperatureMin, extreme(lows, math.Min))
		daily.WeatherCode = append(daily.WeatherCode, summary.symbol(summary.Data.Next12Hours, summary.Data.Next6Hours, summary.Data.Next1Hours))
		daily.PrecipitationProbabilityMax = append(daily.PrecipitationProbabilityMax, extreme(chances, math.Max))
	}
	return daily
}

func daySummary(steps []*metnoStep, firstDay bool) *metnoStep {
	if index := slices.IndexFunc(steps, func(step *metnoStep) bool { return step.at.Hour() >= firstLight }); index >= 0 {
		return steps[index]
	}
	if firstDay && len(steps) > 0 {
		return steps[0]
	}
	return nil
}

func extreme(values []*float64, pick func(a, b float64) float64) *float64 {
	var result *float64
	for _, value := range values {
		switch {
		case value == nil:
		case result == nil:
			result = reading(*value)
		default:
			*result = pick(*result, *value)
		}
	}
	return result
}

// Polar day and night have no sunrise or sunset. Like Open-Meteo, polar night gets both
// at midnight and polar day runs from midnight to midnight, so day/night checks keep working.
func sunriseSunset(day time.Time, lat, lon float64) (string, string) {
	sun := sunTimes(day.Add(12*time.Hour), lat, lon)
	if sun.risesAndSets {
		return sun.rise.In(day.Location()).Format(localTime), sun.set.In(day.Location()).Format(localTime)
	}
	if sunUp(sun.noon, lat, lon) {
		return day.Format(localTime), nextMidnight(day).Format(localTime)
	}
	return day.Format(localTime), day.Format(localTime)
}

// zoneName is the IANA name of zone where it can be found, else its abbreviation.
func zoneName(zone *time.Location, now time.Time) string {
	if name := zone.String(); name != "Local" {
		return name
	}
	if name := strings.TrimPrefix(os.Getenv("TZ"), ":"); name != "" && !strings.HasPrefix(name, "/") {
		return name
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if _, name, ok := strings.Cut(target, "zoneinfo/"); ok {
			return name
		}
	}
	abbreviation, _ := now.In(zone).Zone()
	return abbreviation
}

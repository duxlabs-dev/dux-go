package weather

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// readings are the values fill copies as they are. The feels-like temperature is left
// out, fillFeelsLike handles it.
func (c *Current) readings() []**float64 {
	return []**float64{
		&c.Temperature, &c.RelativeHumidity, &c.IsDay, &c.WeatherCode,
		&c.SurfacePressure, &c.WindSpeed, &c.WindDirection,
	}
}

func (h *Hourly) readings() []*[]*float64 {
	return []*[]*float64{
		&h.Temperature, &h.RelativeHumidity, &h.DewPoint, &h.WeatherCode,
		&h.PrecipitationProbability, &h.WindSpeed, &h.SurfacePressure, &h.Visibility, &h.UVIndex, &h.IsDay,
	}
}

func (d *Daily) readings() []*[]*float64 {
	return []*[]*float64{&d.TemperatureMax, &d.TemperatureMin, &d.WeatherCode, &d.PrecipitationProbabilityMax}
}

// An empty string is a missing time, Open-Meteo's null decodes to one. The order is
// sunrise, sunset, moonrise, moonset: Forecast.reported and polar sentinels rely on it.
func (d *Daily) times() []*[]string {
	return []*[]string{&d.Sunrise, &d.Sunset, &d.Moonrise, &d.Moonset}
}

// fill sets values missing from f to extra's, matched by hour and day, and reports
// whether it set any. Values f has always win.
//
// Providers report local times in different zones (Open-Meteo the location's, MET Norway
// the machine's), so hours are matched by their UTC instant when both forecasts have a
// zone. Days stay matched by date.
func (f *Forecast) fill(extra *Forecast) bool {
	filled := false
	if f.Current.ApparentTemperature == nil {
		if apparent := feelsLike(f.Current.Temperature, extra.Current.Temperature, extra.Current.ApparentTemperature); apparent != nil {
			f.Current.ApparentTemperature, filled = apparent, true
		}
	}
	targets, sources := f.Current.readings(), extra.Current.readings()
	for i, target := range targets {
		if *target == nil && *sources[i] != nil {
			*target, filled = *sources[i], true
		}
	}

	key, extraKey := f.hourKey(), extra.hourKey()
	if key == nil || extraKey == nil {
		key, extraKey = sameString, sameString
	}
	hours := matches(f.Hourly.Time, extra.Hourly.Time, key, extraKey)
	if f.fillFeelsLike(extra, hours) {
		filled = true
	}
	if fillSeries(f.Hourly.readings(), extra.Hourly.readings(), hours, len(f.Hourly.Time), isNil) {
		filled = true
	}
	days := matches(f.Daily.Time, extra.Daily.Time, sameString, sameString)
	if fillSeries(f.Daily.readings(), extra.Daily.readings(), days, len(f.Daily.Time), isNil) {
		filled = true
	}
	if f.fillTimes(extra) {
		filled = true
	}
	f.noteReported(extra)
	return filled
}

// fillFeelsLike fills the hourly feels-like temperature the way fill does the current one.
func (f *Forecast) fillFeelsLike(extra *Forecast, hours map[int]int) bool {
	filled := false
	target, source := f.Hourly, extra.Hourly
	for i, j := range hours {
		if j >= len(source.ApparentTemperature) || (i < len(target.ApparentTemperature) && target.ApparentTemperature[i] != nil) {
			continue
		}
		if apparent := feelsLike(valueAt(target.Temperature, i), valueAt(source.Temperature, j), source.ApparentTemperature[j]); apparent != nil {
			setAt(&target.ApparentTemperature, i, len(target.Time), apparent)
			filled = true
		}
	}
	return filled
}

// maxFeelsLikeGap is how far a filled feels-like may be from the temperature next to it.
// Wind chill and heat stay below 30 °C even in extremes. A gap beyond that is a provider's
// mistake, Kelvin or Fahrenheit above freezing among them, and is not taken.
const maxFeelsLikeGap = 30

// feelsLikeRange bounds a feels-like copied with no temperature on either side to compare
// it with.
const feelsLikeRange = 100

// feelsLike is the value to fill in for a missing feels-like temperature, nil for none.
//
// With both temperatures it is temperature shifted by how much colder or warmer the other
// provider's feels-like is than its own temperature: its absolute value comes from another
// model and can sit on the wrong side of the temperature shown next to it. Without one of
// them it is copied as it is; where temperature is missing, it is filled from the same
// provider. A value further than maxFeelsLikeGap from the temperature it goes with, beyond
// ±feelsLikeRange when there is none, or not finite, is dropped.
func feelsLike(temperature, otherTemperature, otherApparent *float64) *float64 {
	if otherApparent == nil {
		return nil
	}
	value, reference := *otherApparent, temperature
	if reference == nil {
		reference = otherTemperature
	} else if otherTemperature != nil {
		value = math.Round((*temperature+*otherApparent-*otherTemperature)*10) / 10
	}
	gap, limit := math.Abs(value), float64(feelsLikeRange)
	if reference != nil {
		gap, limit = math.Abs(value-*reference), maxFeelsLikeGap
	}
	// !(gap <= limit) also drops NaN.
	if math.IsInf(value, 0) || !(gap <= limit) {
		return nil
	}
	// Rounding a small negative offset gives -0, which would reach the JSON as "-0".
	value += 0
	return &value
}

// valueAt is values[i], nil past the end.
func valueAt(values []*float64, i int) *float64 {
	if i < 0 || i >= len(values) {
		return nil
	}
	return values[i]
}

// fillTimes fills sun and moon times, moved into f's zone when both zones are known. A
// time is only taken for the date it falls on there, and not for a date a provider
// already reported that field for: an empty value there is a day without that event.
func (f *Forecast) fillTimes(extra *Forecast) bool {
	move := extra.moveTimes(f)
	filled := false
	sources := extra.Daily.times()
	for field, target := range f.Daily.times() {
		byDate := map[string]string{}
		for _, value := range *sources[field] {
			local, ok := move(field, value)
			if !ok {
				continue
			}
			if _, taken := byDate[local[:len(localDate)]]; !taken {
				byDate[local[:len(localDate)]] = local
			}
		}
		for i, date := range f.Daily.Time {
			value, ok := byDate[date]
			if !ok || f.reported[field][date] || (i < len(*target) && (*target)[i] != "") {
				continue
			}
			setAt(target, i, len(f.Daily.Time), value)
			filled = true
		}
	}
	return filled
}

// moveTimes turns a Daily.times value of f into local time of target. Without both zones
// it stays as it is.
func (f *Forecast) moveTimes(target *Forecast) func(field int, value string) (string, bool) {
	from, to := f.location(), target.location()
	return func(field int, value string) (string, bool) {
		if len(value) < len(localTime) {
			return "", false
		}
		if from == nil || to == nil {
			return value, true
		}
		// Polar day and night put sunrise and sunset at midnight, which means nothing in
		// another zone.
		if field < 2 && strings.HasSuffix(value, "T00:00") {
			return "", false
		}
		at, err := time.ParseInLocation(localTime, value, from)
		if err != nil {
			return "", false
		}
		return at.In(to).Format(localTime), true
	}
}

// noteReported records the dates of f that source gave moonrise or moonset for: those its
// moon arrays cover, once it has any moon time at all. Sunrise and sunset are never empty
// for a real reason, polar days have midnight there.
func (f *Forecast) noteReported(source *Forecast) {
	times := source.Daily.times()
	known := func(value string) bool { return value != "" }
	if !slices.ContainsFunc(*times[2], known) && !slices.ContainsFunc(*times[3], known) {
		return
	}
	for field := 2; field < len(times); field++ {
		if len(*times[field]) == 0 {
			continue
		}
		if f.reported[field] == nil {
			f.reported[field] = map[string]bool{}
		}
		for _, date := range f.Daily.Time {
			if source.coversDate(f, date) {
				f.reported[field][date] = true
			}
		}
	}
}

// coversDate is whether f's days span all of date, a date of other. Without both zones,
// the same date is taken as the same day.
func (f *Forecast) coversDate(other *Forecast, date string) bool {
	from, to := other.location(), f.location()
	if f == other || from == nil || to == nil {
		return slices.Contains(f.Daily.Time, date)
	}
	start, err := time.ParseInLocation(localDate, date, from)
	if err != nil {
		return false
	}
	last := nextMidnight(start).Add(-time.Minute)
	return slices.Contains(f.Daily.Time, start.In(to).Format(localDate)) && slices.Contains(f.Daily.Time, last.In(to).Format(localDate))
}

func sameString(value string) string {
	return value
}

// hourKey turns a local hour into its UTC instant, nil when f has no zone. utc_offset_seconds
// is the offset at fetch time only, so the zone's rules come first: with the offset alone,
// a DST change in one of the zones shifts every later hour onto its neighbour.
func (f *Forecast) hourKey() func(string) string {
	location := f.location()
	if location == nil {
		return nil
	}
	return func(local string) string {
		at, err := time.ParseInLocation(localTime, local, location)
		if err != nil {
			return local
		}
		return strconv.FormatInt(at.Unix(), 10)
	}
}

func (f *Forecast) location() *time.Location {
	if f.zone != nil {
		return f.zone
	}
	if f.Timezone != "" {
		if location, err := time.LoadLocation(f.Timezone); err == nil {
			return location
		}
	}
	if f.UTCOffsetSeconds != nil {
		return time.FixedZone("", *f.UTCOffsetSeconds)
	}
	return nil
}

// matches maps an index into times to the index into others with the same key.
func matches(times, others []string, key, otherKey func(string) string) map[int]int {
	byKey := make(map[string]int, len(others))
	// The repeated hour of a DST change matches by local time twice, the first one wins.
	for i, other := range others {
		if _, taken := byKey[otherKey(other)]; !taken {
			byKey[otherKey(other)] = i
		}
	}
	found := make(map[int]int, len(times))
	for i, local := range times {
		if j, ok := byKey[key(local)]; ok {
			found[i] = j
		}
	}
	return found
}

func isNil(value *float64) bool {
	return value == nil
}

func fillSeries[T any](targets, sources []*[]T, matched map[int]int, length int, missing func(T) bool) bool {
	filled := false
	for r, target := range targets {
		source := *sources[r]
		for i, j := range matched {
			if j >= len(source) || missing(source[j]) || (i < len(*target) && !missing((*target)[i])) {
				continue
			}
			setAt(target, i, length, source[j])
			filled = true
		}
	}
	return filled
}

// setAt sets index i of values, growing it to length first.
func setAt[T any](values *[]T, i, length int, value T) {
	if len(*values) < length {
		*values = append(*values, make([]T, length-len(*values))...)
	}
	(*values)[i] = value
}

// completeSky computes the sun and moon times no provider had, in the forecast's zone or
// else fallback. A date some provider reported a field for is left as it is.
func (f *Forecast) completeSky(req Request, fallback *time.Location) {
	location := f.location()
	if location == nil {
		location = fallback
	}
	daily := f.Daily
	times := daily.times()
	for i, date := range daily.Time {
		day, err := time.ParseInLocation(localDate, date, location)
		if err != nil {
			continue
		}
		missing := func(field int) bool {
			values := *times[field]
			return !f.reported[field][date] && (i >= len(values) || values[i] == "")
		}
		set := func(field int, value string) {
			if value != "" && missing(field) {
				setAt(times[field], i, len(daily.Time), value)
			}
		}
		if missing(0) || missing(1) {
			sunrise, sunset := sunriseSunset(day, req.Latitude, req.Longitude)
			set(0, sunrise)
			set(1, sunset)
		}
		if !missing(2) && !missing(3) {
			continue
		}
		rise, moonset := moonTimes(day, req.Latitude, req.Longitude)
		set(2, onDate(rise, location, date))
		set(3, onDate(moonset, location, date))
	}
}

// onDate formats at in location, "" when it is nil or falls on another date.
func onDate(at *time.Time, location *time.Location, date string) string {
	if at == nil || at.In(location).Format(localDate) != date {
		return ""
	}
	return at.In(location).Format(localTime)
}

package weather

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func readings(count int, at func(int) *float64) []*float64 {
	values := make([]*float64, count)
	for i := range values {
		values[i] = at(i)
	}
	return values
}

func constant(v float64) func(int) *float64 {
	return func(int) *float64 { return reading(v) }
}

func hoursFrom(start string, count int) []string {
	at, _ := time.Parse(localTime, start)
	times := make([]string, count)
	for i := range times {
		times[i] = at.Add(time.Duration(i) * time.Hour).Format(localTime)
	}
	return times
}

// openMeteoFixture is Bern on 2 and 3 November in the location's zone.
func openMeteoFixture() *Forecast {
	offset := 3600
	return &Forecast{
		Latitude: 46.95, Longitude: 7.45, Timezone: "Europe/Zurich", UTCOffsetSeconds: &offset,
		Current: &Current{
			Time: "2026-11-02T14:15", Temperature: reading(7.6), ApparentTemperature: reading(5.2), RelativeHumidity: reading(81),
			IsDay: reading(1), WeatherCode: reading(3), SurfacePressure: reading(961.4), WindSpeed: reading(10.4), WindDirection: reading(40),
		},
		Hourly: &Hourly{Time: hoursFrom("2026-11-02T00:00", 48), Temperature: readings(48, constant(6))},
		Daily: &Daily{
			Time:                        []string{"2026-11-02", "2026-11-03"},
			Sunrise:                     []string{"2026-11-02T07:10", "2026-11-03T07:12"},
			Sunset:                      []string{"2026-11-02T17:07", "2026-11-03T17:05"},
			TemperatureMax:              []*float64{reading(8.4), reading(9.6)},
			TemperatureMin:              []*float64{reading(3.6), reading(2.4)},
			WeatherCode:                 []*float64{reading(3), reading(61)},
			PrecipitationProbabilityMax: []*float64{reading(5), nil},
		},
	}
}

func byIndex(i int) *float64 {
	return reading(float64(i))
}

func TestFillTakesOnlyMissingReadings(t *testing.T) {
	primary := openMeteoFixture()
	primary.Current.RelativeHumidity = nil
	primary.Hourly.RelativeHumidity = readings(48, constant(80))
	primary.Hourly.RelativeHumidity[14] = nil
	extra := openMeteoFixture()
	extra.Current.RelativeHumidity = reading(60)
	extra.Current.WindSpeed = reading(1)
	extra.Hourly.RelativeHumidity = readings(48, constant(55))
	extra.Hourly.UVIndex = readings(48, constant(3))
	extra.Daily.PrecipitationProbabilityMax = []*float64{reading(10), reading(90)}

	if !primary.fill(extra) {
		t.Fatal("fill reported nothing filled")
	}
	got := fmt.Sprint(value(primary.Current.RelativeHumidity), value(primary.Current.WindSpeed), value(primary.Hourly.RelativeHumidity[13]),
		value(primary.Hourly.RelativeHumidity[14]), value(primary.Hourly.UVIndex[47]), value(primary.Daily.PrecipitationProbabilityMax[0]),
		value(primary.Daily.PrecipitationProbabilityMax[1]))
	if got != "60 10.4 80 55 3 5 90" {
		t.Errorf("filled = %s", got)
	}
	if primary.fill(extra) {
		t.Error("a second fill found something missing")
	}
}

func TestFillCodesAndTemperaturesOnlyWhereMissing(t *testing.T) {
	primary := openMeteoFixture()
	primary.Current.WeatherCode, primary.Current.IsDay = nil, nil
	primary.Hourly.WeatherCode = readings(48, constant(3))
	primary.Hourly.WeatherCode[5] = nil
	primary.Daily.TemperatureMax[1] = nil
	primary.Daily.Moonrise = []string{"2026-11-02T00:10", ""}
	extra := openMeteoFixture()
	extra.Current.WeatherCode, extra.Current.Temperature = reading(61), reading(1)
	extra.Hourly.WeatherCode = readings(48, constant(61))
	extra.Hourly.IsDay = readings(48, constant(1))
	extra.Daily.TemperatureMax = []*float64{reading(20), reading(21)}
	extra.Daily.Moonrise = []string{"2026-11-02T00:12", "2026-11-03T01:20"}

	if !primary.fill(extra) {
		t.Fatal("fill reported nothing filled")
	}
	got := fmt.Sprint(value(primary.Current.WeatherCode), value(primary.Current.Temperature), value(primary.Current.IsDay),
		value(primary.Hourly.WeatherCode[4]), value(primary.Hourly.WeatherCode[5]), value(primary.Hourly.IsDay[0]),
		value(primary.Daily.TemperatureMax[0]), value(primary.Daily.TemperatureMax[1]), primary.Daily.Moonrise)
	if got != "61 7.6 1 3 61 1 8.4 21 [2026-11-02T00:10 2026-11-03T01:20]" {
		t.Errorf("filled = %s", got)
	}
}

func TestFillMatchesHoursByInstant(t *testing.T) {
	extra := openMeteoFixture()
	extra.Hourly.RelativeHumidity = readings(48, byIndex)
	for name, zone := range map[string]struct {
		timezone string
		offset   int
	}{
		// A machine zone 12 hours ahead of the location's, like MET Norway from Auckland.
		"zone":   {"Pacific/Auckland", 13 * 3600},
		"offset": {"", 13 * 3600},
	} {
		t.Run(name, func(t *testing.T) {
			primary := openMeteoFixture()
			primary.Timezone, primary.UTCOffsetSeconds = zone.timezone, &zone.offset
			primary.Hourly.Time = hoursFrom("2026-11-02T12:00", 48)
			primary.fill(extra)
			// 22:00 in Auckland is 09:00 UTC, 10:00 in Bern.
			if got := value(primary.Hourly.RelativeHumidity[10]); got != 10.0 {
				t.Errorf("hour 10 humidity = %v, want 10", got)
			}
		})
	}
}

func TestFillAcrossOneSidedDSTChange(t *testing.T) {
	// Fetched in summer time on 24 October, Zurich leaves it on the 25th, UTC never does.
	summer := 7200
	primary := openMeteoFixture()
	primary.UTCOffsetSeconds = &summer
	primary.Hourly.Time = hoursFrom("2026-10-25T03:00", 24)
	primary.Hourly.RelativeHumidity = nil
	utc := 0
	extra := openMeteoFixture()
	extra.Timezone, extra.UTCOffsetSeconds = "UTC", &utc
	extra.Hourly.Time = hoursFrom("2026-10-25T00:00", 48)
	extra.Hourly.RelativeHumidity = readings(48, byIndex)

	primary.fill(extra)
	// 12:00 in Zurich after the change is 11:00 UTC, not 10:00 as the summer offset says.
	if got := value(primary.Hourly.RelativeHumidity[9]); got != 11.0 {
		t.Errorf("Zurich 12:00 humidity = %v, want UTC 11:00's 11", got)
	}
}

func TestFillMatchesLocalTimeWithoutZone(t *testing.T) {
	primary := openMeteoFixture()
	primary.Timezone = "Pacific/Auckland"
	primary.Hourly.Time = hoursFrom("2026-11-02T12:00", 48)
	extra := openMeteoFixture()
	extra.Timezone, extra.UTCOffsetSeconds = "", nil
	extra.Hourly.RelativeHumidity = readings(48, byIndex)

	primary.fill(extra)
	if got := value(primary.Hourly.RelativeHumidity[10]); got != 22.0 {
		t.Errorf("22:00 humidity = %v, want the other 22:00's 22", got)
	}
}

// Without zones, the hour repeated when Zurich leaves summer time on 2026-10-25 shows up
// twice as 02:00. The first one, still in summer time, is the one taken.
func TestFillRepeatedLocalHourTakesTheFirst(t *testing.T) {
	primary := openMeteoFixture()
	primary.Timezone, primary.UTCOffsetSeconds = "", nil
	primary.Hourly.Time = []string{"2026-10-25T02:00"}
	primary.Hourly.Temperature = nil
	extra := openMeteoFixture()
	extra.Timezone, extra.UTCOffsetSeconds = "", nil
	extra.Hourly.Time = []string{"2026-10-25T01:00", "2026-10-25T02:00", "2026-10-25T02:00", "2026-10-25T03:00"}
	extra.Hourly.RelativeHumidity = readings(4, byIndex)

	primary.fill(extra)
	if got := value(primary.Hourly.RelativeHumidity[0]); got != 1.0 {
		t.Errorf("02:00 humidity = %v, want the first 02:00's 1", got)
	}
}

// Longyearbyen is in polar night on 2026-11-20, Open-Meteo puts its sunrise and sunset at
// midnight then. Moved to Auckland that would read as noon, so it is not taken.
func TestFillSkipsPolarSunTimesFromAnotherZone(t *testing.T) {
	primary := openMeteoFixture()
	primary.Timezone = "Pacific/Auckland"
	primary.Daily.Time = []string{"2026-11-20"}
	primary.Daily.Sunrise, primary.Daily.Sunset = nil, nil
	extra := openMeteoFixture()
	extra.Timezone = "Arctic/Longyearbyen"
	extra.Daily.Time = []string{"2026-11-20"}
	extra.Daily.Sunrise, extra.Daily.Sunset = []string{"2026-11-20T00:00"}, []string{"2026-11-20T00:00"}

	primary.fill(extra)
	if at(primary.Daily.Sunrise, 0) != "" || at(primary.Daily.Sunset, 0) != "" {
		t.Errorf("sun %v-%v taken from another zone's polar night", primary.Daily.Sunrise, primary.Daily.Sunset)
	}
}

// Every value of current, hourly and daily but the times is filled where it is missing.
// The fields are found by reflection, so a new field has to be fillable too. 21 stays
// within maxFeelsLikeGap of the fixture's temperatures.
func TestFillEveryField(t *testing.T) {
	parts := map[string]func(*Forecast) reflect.Value{
		"current": func(f *Forecast) reflect.Value { return reflect.ValueOf(f.Current).Elem() },
		"hourly":  func(f *Forecast) reflect.Value { return reflect.ValueOf(f.Hourly).Elem() },
		"daily":   func(f *Forecast) reflect.Value { return reflect.ValueOf(f.Daily).Elem() },
	}
	for name, part := range parts {
		fields := part(openMeteoFixture()).Type()
		for i := range fields.NumField() {
			field := fields.Field(i)
			if field.Name == "Time" {
				continue
			}
			t.Run(name+"."+field.Name, func(t *testing.T) {
				primary, extra := openMeteoFixture(), openMeteoFixture()
				target, source := part(primary).Field(i), part(extra).Field(i)
				target.Set(reflect.Zero(field.Type))
				want := ""
				switch field.Type {
				case reflect.TypeFor[*float64]():
					source.Set(reflect.ValueOf(reading(21)))
					want = "21"
				case reflect.TypeFor[[]*float64]():
					source.Set(reflect.ValueOf(readings(part(extra).FieldByName("Time").Len(), constant(21))))
					want = "21"
				case reflect.TypeFor[[]string]():
					times := make([]string, len(extra.Daily.Time))
					for j, date := range extra.Daily.Time {
						times[j] = date + "T12:34"
					}
					source.Set(reflect.ValueOf(times))
					want = extra.Daily.Time[0] + "T12:34"
				default:
					t.Fatalf("unhandled field type %s", field.Type)
				}

				primary.fill(extra)
				got := target.Interface()
				switch filled := got.(type) {
				case *float64:
					got = value(filled)
				case []*float64:
					if len(filled) > 0 {
						got = value(filled[0])
					}
				case []string:
					if len(filled) > 0 {
						got = filled[0]
					}
				}
				if fmt.Sprint(got) != want {
					t.Errorf("%s = %v, want %s", field.Name, got, want)
				}
			})
		}
	}
}

// A plugin without a zone gives a moonrise but no moonset for 2026-11-03: there is none
// that day by its account, and a later provider does not change that.
func TestFillKeepsReportedMissingMoonTimeWithoutZones(t *testing.T) {
	primary := openMeteoFixture()
	primary.Timezone, primary.UTCOffsetSeconds = "", nil
	primary.Daily.Time = []string{"2026-11-03"}
	primary.Daily.Moonrise, primary.Daily.Moonset = []string{"2026-11-03T00:31"}, []string{""}
	extra := openMeteoFixture()
	extra.Timezone, extra.UTCOffsetSeconds = "", nil
	extra.Daily.Time = []string{"2026-11-03"}
	extra.Daily.Moonset = []string{"2026-11-03T14:39"}

	primary.noteReported(primary)
	primary.fill(extra)
	if got := at(primary.Daily.Moonset, 0); got != "" {
		t.Errorf("moonset %q replaced the provider's none", got)
	}
}

// USNO has the moon rise at 47.0084, -95.9822 on 2026-10-24 at 22:21 and on 2026-10-25
// at 22:42 UTC. In Zurich both fall on the 25-hour 2026-10-25, at 00:21 and 23:42: the
// first is taken.
func TestFillTimesTakesTheFirstOnADate(t *testing.T) {
	primary := openMeteoFixture()
	primary.Daily.Time = []string{"2026-10-25"}
	extra := openMeteoFixture()
	extra.Timezone, extra.UTCOffsetSeconds = "UTC", nil
	extra.Daily.Time = []string{"2026-10-24", "2026-10-25"}
	extra.Daily.Moonrise = []string{"2026-10-24T22:21", "2026-10-25T22:42"}

	primary.fill(extra)
	if got := at(primary.Daily.Moonrise, 0); got != "2026-10-25T00:21" {
		t.Errorf("moonrise %q, want the first, 2026-10-25T00:21", got)
	}
}

// Bern's moon from Open-Meteo, its days from 2026-11-04, in a forecast on Auckland's
// time. Auckland's 2026-11-04 starts at noon on Bern's 2026-11-03, which the Bern days do
// not cover: its moonset, 02:39 in Open-Meteo's own Auckland view, is computed. Its
// moonrise, 01:48 on Bern's 2026-11-04, is moved over.
func TestFillHalfCoveredDayIsComputed(t *testing.T) {
	var bern Forecast
	recorded(t, "bern-openmeteo.json", &bern)
	bern.Daily = &Daily{Time: bern.Daily.Time[1:], Moonrise: bern.Daily.Moonrise[1:], Moonset: bern.Daily.Moonset[1:]}
	primary := openMeteoFixture()
	primary.Timezone = "Pacific/Auckland"
	primary.Daily = &Daily{Time: []string{"2026-11-04"}}

	primary.fill(&bern)
	primary.completeSky(Request{Latitude: 46.948, Longitude: 7.4474}, time.UTC)
	if got := at(primary.Daily.Moonrise, 0); got != "2026-11-04T13:48" {
		t.Errorf("moonrise %q, want 2026-11-04T13:48", got)
	}
	if got := at(primary.Daily.Moonset, 0); !near(t, got, "2026-11-04T02:39") {
		t.Errorf("moonset %q, Open-Meteo says 2026-11-04T02:39", got)
	}
}

// The source's temperature with the other provider's feels-like offset, not its absolute
// feels-like: MET Norway at 9.9 °C feels 9.9 °C, so next to MeteoSwiss' 9.7 °C it is 9.7 °C.
func TestFillFeelsLikeKeepsTheOtherProvidersOffset(t *testing.T) {
	primary := openMeteoFixture()
	primary.Current.Temperature, primary.Current.ApparentTemperature = reading(9.7), nil
	primary.Hourly.Temperature[14], primary.Hourly.Temperature[15] = reading(9.7), reading(-3.3)
	extra := openMeteoFixture()
	extra.Current.Temperature, extra.Current.ApparentTemperature = reading(9.9), reading(9.9)
	extra.Hourly.Temperature = readings(48, constant(12))
	extra.Hourly.ApparentTemperature = readings(48, constant(8.1))

	if !primary.fill(extra) {
		t.Fatal("fill reported nothing filled")
	}
	got := fmt.Sprint(value(primary.Current.ApparentTemperature), value(primary.Hourly.ApparentTemperature[14]),
		value(primary.Hourly.ApparentTemperature[15]), value(primary.Hourly.ApparentTemperature[0]))
	if got != "9.7 5.8 -7.2 2.1" {
		t.Errorf("feels like = %s", got)
	}
}

// Hours are matched by instant first, the offset is the one of the same hour.
func TestFillFeelsLikeOffsetOfTheSameInstant(t *testing.T) {
	primary := openMeteoFixture()
	primary.Hourly.Temperature = readings(48, constant(10))
	extra := openMeteoFixture()
	extra.Timezone, extra.UTCOffsetSeconds = "UTC", nil
	extra.Hourly.Time = hoursFrom("2026-11-02T00:00", 48)
	extra.Hourly.Temperature = readings(48, constant(20))
	extra.Hourly.ApparentTemperature = readings(48, func(i int) *float64 { return reading(20 - float64(i)) })

	primary.fill(extra)
	// 14:00 in Bern is 13:00 UTC, where the offset is -13: 10 - 13.
	if got := fmt.Sprint(value(primary.Hourly.ApparentTemperature[14])); got != "-3" {
		t.Errorf("feels like at 14:00 = %s, want -3", got)
	}
}

// Where the source has no temperature both are taken from the other provider, which
// matches. Without the other provider's temperature there is no offset, the value is
// copied. The source's own feels-like stays.
func TestFillFeelsLikeWithoutATemperature(t *testing.T) {
	primary := openMeteoFixture()
	primary.Current.Temperature, primary.Current.ApparentTemperature = nil, nil
	primary.Hourly.Temperature[3] = nil
	primary.Hourly.ApparentTemperature = readings(48, func(int) *float64 { return nil })
	primary.Hourly.ApparentTemperature[7] = reading(1.5)
	extra := openMeteoFixture()
	extra.Current.Temperature, extra.Current.ApparentTemperature = reading(4), reading(1)
	extra.Hourly.Temperature = readings(48, constant(4))
	extra.Hourly.Temperature[5] = nil
	extra.Hourly.ApparentTemperature = readings(48, constant(1))

	primary.fill(extra)
	got := fmt.Sprint(value(primary.Current.Temperature), value(primary.Current.ApparentTemperature),
		value(primary.Hourly.Temperature[3]), value(primary.Hourly.ApparentTemperature[3]),
		value(primary.Hourly.ApparentTemperature[5]), value(primary.Hourly.ApparentTemperature[6]),
		value(primary.Hourly.ApparentTemperature[7]))
	if got != "4 1 4 1 1 3 1.5" {
		t.Errorf("filled = %s", got)
	}
}

// Plugins can send anything. A constant unit offset on both temperatures, Kelvin here,
// still gives the right difference. A feels-like that cannot belong to the temperature
// next to it, mixed units or a broken value, is not filled at all rather than copied.
func TestFillFeelsLikeFromOddProviders(t *testing.T) {
	cases := []struct {
		name                 string
		temperature          *float64
		other, otherApparent *float64
		want                 string
	}{
		{"kelvin on both", reading(9.7), reading(283.05), reading(281.05), "7.7"},
		{"feels-like in celsius, temperature in kelvin", reading(9.7), reading(283.05), reading(8), "<nil>"},
		{"far off without the other temperature", reading(9.7), nil, reading(80), "<nil>"},
		{"far off where the source has no temperature", nil, reading(10), reading(-45), "<nil>"},
		{"overflow", reading(1e308), reading(-1e308), reading(1e308), "<nil>"},
		{"fahrenheit feels-like next to celsius", reading(10), nil, reading(50), "<nil>"},
		{"strong wind chill", reading(-30), reading(-30), reading(-55), "-55"},
		{"no temperatures at all", nil, nil, reading(3), "3"},
		{"no temperatures, broken value", nil, nil, reading(1e6), "<nil>"},
		{"offset rounds to zero", reading(0), reading(10.02), reading(10), "0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			primary := openMeteoFixture()
			primary.Current.Temperature, primary.Current.ApparentTemperature = tc.temperature, nil
			primary.Hourly.Temperature = readings(48, func(int) *float64 { return tc.temperature })
			extra := openMeteoFixture()
			extra.Current.Temperature, extra.Current.ApparentTemperature = tc.other, tc.otherApparent
			extra.Hourly.Temperature = readings(48, func(int) *float64 { return tc.other })
			extra.Hourly.ApparentTemperature = readings(48, func(int) *float64 { return tc.otherApparent })

			primary.fill(extra)
			got := fmt.Sprint(value(primary.Current.ApparentTemperature))
			hourly := fmt.Sprint(value(valueAt(primary.Hourly.ApparentTemperature, 20)))
			if got != tc.want || hourly != tc.want {
				t.Errorf("feels like = %s, hourly %s, want %s", got, hourly, tc.want)
			}
			encoded, err := json.Marshal(primary.Current)
			if err != nil {
				t.Fatalf("forecast does not encode: %v", err)
			}
			if strings.Contains(string(encoded), ":-0,") {
				t.Errorf("negative zero in %s", encoded)
			}
		})
	}
}

// A plugin's hourly arrays can be shorter than its times, or than the other provider's.
func TestFillFeelsLikeShortArrays(t *testing.T) {
	primary := openMeteoFixture()
	primary.Hourly.Temperature = readings(10, constant(10))
	extra := openMeteoFixture()
	extra.Hourly.Temperature = readings(5, constant(12))
	extra.Hourly.ApparentTemperature = readings(20, constant(9))

	primary.fill(extra)
	got := fmt.Sprint(len(primary.Hourly.ApparentTemperature), value(primary.Hourly.ApparentTemperature[2]),
		value(primary.Hourly.ApparentTemperature[7]), value(primary.Hourly.ApparentTemperature[15]))
	// Hour 2 has both temperatures and is shifted. Hour 7 has only the source's and hour 15
	// neither, there the value is copied.
	if got != "48 7 9 9" {
		t.Errorf("filled = %s", got)
	}
}

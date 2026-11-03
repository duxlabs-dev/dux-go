# weather

Weather forecast for a location. Asks Open-Meteo and MET Norway, or whatever providers you list, at the same time. The first one in your order that answers is used, the others fill in readings it lacks. Every provider answers in the [Open-Meteo forecast](https://open-meteo.com/en/docs) shape, so one parser reads them all.

This is the provider logic from the Dux Shell weather service, moved out of QML.

## Usage

```go
client := weather.New(weather.Options{UserAgent: "myshell/1.0 (+https://example.com)"})

result, err := client.Lookup(ctx, weather.Request{Latitude: 46.95, Longitude: 7.45})
if err != nil {
    return
}
fmt.Println(result.Source, *result.Forecast.Current.Temperature)
```

`Providers` on the `Request` is the priority order. nil = `DefaultProviders()`, which is Open-Meteo, MET Norway. An empty non-nil slice is `ErrNoProviders`. A latitude or longitude out of range is `ErrInvalidLocation`.

Set a `UserAgent` with a way to reach you, [MET Norway's terms](https://api.met.no/doc/TermsOfService) require one. `Options.HTTPClient` replaces the default client, which refuses redirects to other hosts. `Options.Location` is the zone for providers without zone information, MET Norway, and defaults to `time.Local`. `Options.Now` is for tests.

There is no cache and no rate limit. Ask every 15 minutes or so, not on every repaint.

## Providers

| Provider    | Times in              | Not provided                                              |
| ----------- | --------------------- | --------------------------------------------------------- |
| `openmeteo` | the location's zone   | hourly `is_day`                                           |
| `metno`     | the machine's zone    | `surface_pressure`, `visibility`, `uv_index`, `moonrise`, `moonset` (computed when no provider has them) |

MET Norway's own pressure is at sea level and its UV index assumes a clear sky, so neither is passed on as Open-Meteo's surface pressure and UV index. Its answer is mapped into the Open-Meteo shape:

- Hours start at local midnight. The hours before the forecast starts repeat its first step. Hourly steps run out after about two days, the hourly forecast ends there.
- Days run until the last step. A day's weather code is the 12-hour symbol of its first step from 06:00. The daily range takes the 6-hourly minimum and maximum into account, the precipitation probability falls back to the 6-hourly one where there are no hourly steps.
- Symbol codes become WMO weather codes. Anything with thunder is 95.
- Sunrise and sunset are computed with [SunCalc](https://github.com/mourner/suncalc)'s formulas. Polar night has both at midnight, polar day runs from midnight to the next midnight, as Open-Meteo does.
- `is_day` is 1 per hour while the sun is above -0.833°, so day and night follow the location even when the machine's zone is half a day away.
- `utc_offset_seconds` and `timezone` are those of `Options.Location`, the machine's by default. For `time.Local` the IANA name comes from `TZ` or the `/etc/localtime` link, else the zone's abbreviation is used.

## Fallback and fill

Every provider gets 15 seconds. The first provider in priority order with a forecast is `Result.Source`, even when a later one answered first. A forecast without `current`, `hourly` or `daily`, or without any hours or days, counts as a failure.

Once the source is known, the providers still out get 3 more seconds. Each later provider with a forecast then fills what the source lacks, in priority order: every `current`, `hourly` and `daily` value that is null, missing or an empty string, weather codes, temperatures, `is_day` and sun and moon times included. A value the source has is never replaced. Only the `time` arrays and `current.time` are never filled. `Result.Filled` lists the providers that filled at least one value.

Hours are matched by their UTC instant when both forecasts have a zone, else by the local time string, where the hour repeated at the end of summer time matches its first occurrence. A forecast's zone is its `timezone` when that is an IANA name, so a DST change in only one of the two zones still lines the hours up. Without one it is `utc_offset_seconds`, which is the offset at fetch time only: after a DST change in that zone, hours land on their neighbour. Days are matched by date.

Sun and moon times are moved into the zone of the source's forecast and go to the date they fall on there, so a moonrise from a provider in Bern lands on the right day of a forecast in Auckland's time. Midnight sunrises and sunsets, the polar day and night marker, are not moved. A provider with moon times reports every day its moon arrays cover: an empty moonrise or moonset there means the moon does not rise or set that day, and it is neither filled nor computed.

Sunrise, sunset, moonrise and moonset still missing after that are computed for the location, in the forecast's zone, else `Options.Location`. Sun times use SunCalc's formulas, polar day and night as above. Moon times scan the day, all 23 or 25 hours of a DST change day, the way SunCalc does, with the moon's position from the Astronomical Almanac's low-precision series, within about 4 minutes of Open-Meteo's and the US Naval Observatory's. A day on which the moon does not rise or set gets an empty string, which reads the same as Open-Meteo's null to the shell. A day on which it rises twice, which happens on a 25-hour day, gets the first. Computed times do not count towards `Filled`.

When every provider fails, the error is a `*LookupError` with one `*ProviderError` per provider:

```
no weather forecast: openmeteo: HTTP 503; metno: context deadline exceeded
```

Network errors leave out the request URL, which holds the location. `errors.Is` and `errors.As` see through both, so `errors.Is(err, weather.ErrTooLarge)` works. A provider id that is neither built in nor resolved fails as `unknown weather provider`, the others still run.

## Adding things

A new built-in provider: add the `Provider` const, write a `fetchX` method on `Client` that returns a `*Forecast`, add a row to `builtins` in `providers.go`.

## Custom providers

A provider is a `Source`: a `Name`, a `URL` and a `Fetch` func. `Options.Resolve` supplies Sources for ids that are not built in. It runs on every `Lookup`, so the set can change while the client lives.

```go
client := weather.New(weather.Options{
    Resolve: func(id weather.Provider) (weather.Source, bool) {
        if id != "myprovider" {
            return weather.Source{}, false
        }
        return weather.Source{
            Name:  "My Provider",
            URL:   "https://example.com",
            Fetch: weather.Command("/usr/lib/myprovider/fetch"),
        }, true
    },
})

result, err := client.Lookup(ctx, weather.Request{
    Latitude:  46.95,
    Longitude: 7.45,
    Providers: []weather.Provider{"myprovider", weather.OpenMeteo},
})
```

A custom Source only runs when it is in `Providers`, `DefaultProviders()` is built-ins only.

### Command

`weather.Command(name, args...)` is a `Fetch` that runs a program per lookup, so a provider can be written in anything. The location comes in on stdin:

```json
{ "latitude": 46.95, "longitude": 7.45 }
```

The forecast goes to stdout in the Open-Meteo shape, see JSON below. Fields the program does not know are left out or null, the other providers fill them and sun and moon times are computed. Give `timezone` (an IANA name) and `utc_offset_seconds` for the zone the times are in, they let hours be matched with other providers by instant.

No output is an error, `no forecast`. A non-zero exit is an error too, with the end of stderr added to it; otherwise stderr is dropped. A program that runs out of time fails with `timed out after 15s`. Output over 2 MiB is `ErrTooLarge`. After 15 seconds, or once the lookup is done with it, the program is killed along with everything it started.

Try one by hand:

```
echo '{"latitude":46.95,"longitude":7.45}' | ./fetch
```

## JSON

`Result` marshals to this, the forecast in the shape the Dux Shell reads. Made-up values, every field shown, one hour and one day:

```json
{
  "source": "openmeteo",
  "filled": ["metno"],
  "forecast": {
    "latitude": 46.95,
    "longitude": 7.45,
    "timezone": "Europe/Zurich",
    "utc_offset_seconds": 3600,
    "current": {
      "time": "2026-11-02T14:15",
      "temperature_2m": 7.6,
      "apparent_temperature": 5.2,
      "relative_humidity_2m": 81,
      "is_day": 1,
      "weather_code": 3,
      "surface_pressure": 961.4,
      "wind_speed_10m": 10.4,
      "wind_direction_10m": 40
    },
    "hourly": {
      "time": ["2026-11-02T14:00"],
      "temperature_2m": [7.6],
      "apparent_temperature": [5.2],
      "relative_humidity_2m": [81],
      "dew_point_2m": [4.5],
      "weather_code": [3],
      "precipitation_probability": [5],
      "wind_speed_10m": [10.4],
      "surface_pressure": [961.4],
      "visibility": [24140],
      "uv_index": [1.5],
      "is_day": [1]
    },
    "daily": {
      "time": ["2026-11-02"],
      "sunrise": ["2026-11-02T07:10"],
      "sunset": ["2026-11-02T17:07"],
      "moonrise": ["2026-11-02T19:41"],
      "moonset": ["2026-11-02T11:02"],
      "temperature_2m_max": [8.4],
      "temperature_2m_min": [3.6],
      "weather_code": [3],
      "precipitation_probability_max": [5]
    }
  }
}
```

Times are local wall-clock times without a zone. Temperatures are °C, wind speed km/h, wind direction degrees, pressure hPa, visibility metres, humidity and probabilities percent, `is_day` 1 or 0. A missing reading is null. `current` readings are always present, hourly and daily arrays are left out when the provider has none of them. `filled`, `timezone` and `utc_offset_seconds` are left out when unknown.

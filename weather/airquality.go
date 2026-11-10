package weather

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
)

const airQualityURL = "https://air-quality-api.open-meteo.com/v1/air-quality"

// fetchAirQuality gets the current air quality from Open-Meteo, as a forecast with only
// those readings to fill in.
func (c *Client) fetchAirQuality(ctx context.Context, req Request) (*Forecast, error) {
	query := strings.Join([]string{
		"latitude=" + strconv.FormatFloat(req.Latitude, 'f', -1, 64),
		"longitude=" + strconv.FormatFloat(req.Longitude, 'f', -1, 64),
		"current=european_aqi,us_aqi,pm2_5,pm10",
		"hourly=pm2_5",
		"past_hours=24",
		"forecast_hours=0",
	}, "&")
	body, err := c.get(ctx, c.airQualityURL+"?"+query)
	if err != nil {
		return nil, err
	}
	var response struct {
		Current *Current `json:"current"`
		Hourly  struct {
			PM25 []*float64 `json:"pm2_5"`
		} `json:"hourly"`
	}
	if err := decodeJSON(body, &response); err != nil {
		return nil, err
	}
	if response.Current == nil {
		return nil, errors.New("no air quality")
	}
	// Only the air quality: the other Current fields of this API are not weather.
	current := response.Current
	return &Forecast{
		Current: &Current{
			EuropeanAQI: current.EuropeanAQI, USAQI: current.USAQI, PM25: current.PM25, PM10: current.PM10,
			PM25Mean24h: mean24h(response.Hourly.PM25),
		},
		Hourly: &Hourly{},
		Daily:  &Daily{},
	}, nil
}

// minHoursForMean is how many of the 24 hours a mean needs, the EEA's 75 % for a daily mean.
const minHoursForMean = 18

// mean24h is the mean of the last 24 hourly values, rounded to 0.1, nil with fewer than
// minHoursForMean of them.
func mean24h(hourly []*float64) *float64 {
	if len(hourly) > 24 {
		hourly = hourly[len(hourly)-24:]
	}
	sum, count := 0.0, 0
	for _, value := range hourly {
		if value != nil {
			sum += *value
			count++
		}
	}
	if count < minHoursForMean {
		return nil
	}
	mean := math.Round(sum/float64(count)*10) / 10
	if math.IsInf(mean, 0) || math.IsNaN(mean) {
		return nil
	}
	return &mean
}

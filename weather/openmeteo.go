package weather

import (
	"context"
	"strconv"
	"strings"
)

const openMeteoURL = "https://api.open-meteo.com/v1/forecast"

var openMeteoParams = []string{
	"current=temperature_2m,relative_humidity_2m,apparent_temperature,is_day,precipitation,weather_code,surface_pressure,wind_speed_10m,wind_direction_10m",
	"daily=sunrise,sunset,moonrise,moonset,temperature_2m_max,temperature_2m_min,weather_code,precipitation_probability_max",
	"hourly=temperature_2m,weather_code,precipitation_probability,wind_speed_10m,apparent_temperature,relative_humidity_2m,surface_pressure,visibility,cloud_cover,uv_index,dew_point_2m",
	"timezone=auto",
	"forecast_days=7",
}

func openMeteoQuery(req Request) string {
	params := append([]string{
		"latitude=" + strconv.FormatFloat(req.Latitude, 'f', -1, 64),
		"longitude=" + strconv.FormatFloat(req.Longitude, 'f', -1, 64),
	}, openMeteoParams...)
	return strings.Join(params, "&")
}

func (c *Client) fetchOpenMeteo(ctx context.Context, req Request) (*Forecast, error) {
	body, err := c.get(ctx, c.openMeteoURL+"?"+openMeteoQuery(req))
	if err != nil {
		return nil, err
	}
	var forecast Forecast
	if err := decodeJSON(body, &forecast); err != nil {
		return nil, err
	}
	return &forecast, nil
}

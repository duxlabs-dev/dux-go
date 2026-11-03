package weather

// The formulas are from SunCalc, https://github.com/mourner/suncalc, based on
// https://aa.quae.nl/en/reken/zonpositie.html.
//
// Copyright (c) 2026, Volodymyr Agafonkin
// All rights reserved.
//
// Redistribution and use in source and binary forms, with or without modification, are
// permitted provided that the following conditions are met:
//
//    1. Redistributions of source code must retain the above copyright notice, this list of
//       conditions and the following disclaimer.
//
//    2. Redistributions in binary form must reproduce the above copyright notice, this list
//       of conditions and the following disclaimer in the documentation and/or other materials
//       provided with the distribution.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS" AND ANY
// EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED WARRANTIES OF
// MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE
// COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL,
// EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF
// SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION)
// HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR
// TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS
// SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

import (
	"math"
	"time"
)

const (
	rad       = math.Pi / 180
	dayMillis = 24 * 60 * 60 * 1000
	j1970     = 2440588
	j2000     = 2451545
	j0        = 0.0009
	obliquity = rad * 23.4397
	// The sun's centre at sunrise and sunset, with refraction and its radius.
	horizon = -0.833 * rad
)

func toDays(t time.Time) float64 {
	return float64(t.UnixMilli())/dayMillis - 0.5 + j1970 - j2000
}

func fromJulian(j float64) time.Time {
	return time.UnixMilli(int64((j + 0.5 - j1970) * dayMillis))
}

func solarMeanAnomaly(d float64) float64 {
	return rad * (357.5291 + 0.98560028*d)
}

func eclipticLongitude(m float64) float64 {
	center := rad * (1.9148*math.Sin(m) + 0.02*math.Sin(2*m) + 0.0003*math.Sin(3*m))
	perihelion := rad * 102.9372
	return m + center + perihelion + math.Pi
}

func declination(l, b float64) float64 {
	return math.Asin(math.Sin(b)*math.Cos(obliquity) + math.Cos(b)*math.Sin(obliquity)*math.Sin(l))
}

func rightAscension(l, b float64) float64 {
	return math.Atan2(math.Sin(l)*math.Cos(obliquity)-math.Tan(b)*math.Sin(obliquity), math.Cos(l))
}

func siderealTime(d, lw float64) float64 {
	return rad*(280.16+360.9856235*d) - lw
}

func altitude(hourAngle, phi, dec float64) float64 {
	return math.Asin(math.Sin(phi)*math.Sin(dec) + math.Cos(phi)*math.Cos(dec)*math.Cos(hourAngle))
}

func solarTransit(ds, m, l float64) float64 {
	return j2000 + ds + 0.0053*math.Sin(m) - 0.0069*math.Sin(2*l)
}

func sunAltitude(t time.Time, lat, lon float64) float64 {
	lw, phi, d := rad*-lon, rad*lat, toDays(t)
	l := eclipticLongitude(solarMeanAnomaly(d))
	return altitude(siderealTime(d, lw)-rightAscension(l, 0), phi, declination(l, 0))
}

func sunUp(t time.Time, lat, lon float64) bool {
	return sunAltitude(t, lat, lon) > horizon
}

type sunDay struct {
	noon, rise, set time.Time
	// risesAndSets is false in polar day and polar night, rise and set are zero then.
	risesAndSets bool
}

func sunTimes(t time.Time, lat, lon float64) sunDay {
	lw, phi, d := rad*-lon, rad*lat, toDays(t)
	cycle := math.Floor(d - j0 - lw/(2*math.Pi) + 0.5)
	ds := j0 + lw/(2*math.Pi) + cycle
	m := solarMeanAnomaly(ds)
	l := eclipticLongitude(m)
	dec := declination(l, 0)
	noon := solarTransit(ds, m, l)

	day := sunDay{noon: fromJulian(noon)}
	hourAngle := math.Acos((math.Sin(horizon) - math.Sin(phi)*math.Sin(dec)) / (math.Cos(phi) * math.Cos(dec)))
	if math.IsNaN(hourAngle) {
		return day
	}
	set := solarTransit(j0+(hourAngle+lw)/(2*math.Pi)+cycle, m, l)
	day.rise, day.set, day.risesAndSets = fromJulian(noon-(set-noon)), fromJulian(set), true
	return day
}

// moonAboveHorizon is how far the moon's upper limb is above the horizon, in radians,
// with refraction. SunCalc's single-term moon is several minutes off at moonrise, so the
// position and parallax are from "Low-precision formulae for geocentric coordinates of
// the Moon" in section D of The Astronomical Almanac (US Naval Observatory and HM Nautical
// Almanac Office), good to about 0.3°. The formulas are not under copyright.
func moonAboveHorizon(t time.Time, lat, lon float64) float64 {
	d := toDays(t)
	c := d / 36525
	term := func(amplitude, phase, rate float64) float64 { return amplitude * math.Sin(rad*(phase+rate*c)) }
	wave := func(amplitude, phase, rate float64) float64 { return amplitude * math.Cos(rad*(phase+rate*c)) }
	l := rad * (218.32 + 481267.881*c + term(6.29, 135.0, 477198.87) + term(-1.27, 259.3, -413335.36) +
		term(0.66, 235.7, 890534.22) + term(0.21, 269.9, 954397.74) + term(-0.19, 357.5, 35999.05) + term(-0.11, 186.5, 966404.03))
	b := rad * (term(5.13, 93.3, 483202.02) + term(0.28, 228.2, 960400.89) + term(-0.28, 318.3, 6003.15) + term(-0.17, 217.6, -407332.21))
	parallax := rad * (0.9508 + wave(0.0518, 135.0, 477198.87) + wave(0.0095, 259.3, -413335.36) +
		wave(0.0078, 235.7, 890534.22) + wave(0.0028, 269.9, 954397.74))

	h := altitude(siderealTime(d, rad*-lon)-rightAscension(l, b), rad*lat, declination(l, b))
	// Rise and set are at 0.7275 parallax minus 34 arc minutes of refraction.
	return h - (0.7275*parallax - rad*34.0/60)
}

// moonTimes finds moonrise and moonset from midnight to the next, nil where the moon does
// not rise or set. It fits a parabola to the altitude every two hours, as SunCalc does. The
// last two-hour step can run past a 23-hour day, callers check the date.
func moonTimes(midnight time.Time, lat, lon float64) (rise, set *time.Time) {
	hours := nextMidnight(midnight).Sub(midnight).Hours()
	at := func(hours float64) *time.Time {
		t := midnight.Add(time.Duration(hours * float64(time.Hour)))
		return &t
	}
	h0 := moonAboveHorizon(midnight, lat, lon)
	for i := 1.0; i <= hours && (rise == nil || set == nil); i += 2 {
		h1 := moonAboveHorizon(*at(i), lat, lon)
		h2 := moonAboveHorizon(*at(i + 1), lat, lon)
		a := (h0+h2)/2 - h1
		b := (h2 - h0) / 2
		xe := -b / (2 * a)
		ye := (a*xe+b)*xe + h1
		discriminant := b*b - 4*a*h1
		risingAtStart := h0 < 0
		h0 = h2
		if discriminant < 0 {
			continue
		}
		dx := math.Sqrt(discriminant) / (math.Abs(a) * 2)
		x1, x2 := xe-dx, xe+dx
		inside1, inside2 := math.Abs(x1) <= 1, math.Abs(x2) <= 1
		if x1 < -1 {
			x1 = x2
		}
		switch {
		case inside1 && inside2 && ye < 0:
			rise, set = at(i+x2), at(i+x1)
		case inside1 && inside2:
			rise, set = at(i+x1), at(i+x2)
		case (inside1 || inside2) && risingAtStart:
			rise = at(i + x1)
		case inside1 || inside2:
			set = at(i + x1)
		}
	}
	return rise, set
}

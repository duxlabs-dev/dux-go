// Package weather fetches a forecast from an ordered list of providers, falling back
// to the next on failure and filling readings the first one lacks from the others.
// Every provider answers in the Open-Meteo response shape.
package weather

package lyrics

import (
	"context"
	"errors"
	"math"
	"net/url"
	"strings"
	"time"
)

const (
	lrclibGetURL    = "https://lrclib.net/api/get"
	lrclibSearchURL = "https://lrclib.net/api/search"
)

type lrclibRecord struct {
	TrackName    string  `json:"trackName"`
	ArtistName   string  `json:"artistName"`
	Duration     float64 `json:"duration"`
	Instrumental bool    `json:"instrumental"`
	HasWordSync  bool    `json:"hasWordSync"`
	PlainLyrics  string  `json:"plainLyrics"`
	SyncedLyrics string  `json:"syncedLyrics"`
	Lyricsfile   string  `json:"lyricsfile"`
}

// /api/get prefers sync only among records with the track's exact signature, and the same
// song is published under many. A plain or missing answer gets a title search instead.
func (c *Client) fetchLrclib(ctx context.Context, req Request) (*Lyrics, error) {
	var exact *lrclibRecord
	err := c.getJSON(ctx, lrclibGetURL, req.query("track_name", "artist_name", "album_name", "duration"), 8*time.Second, &exact)
	if err != nil && !errors.Is(err, errNotFound) {
		return nil, err
	}
	if exact != nil && (exact.Instrumental || exact.tier() > 0) {
		return decodeLrclib(*exact)
	}
	// A failed search is an error even with a plain record in hand, a cached plain answer never upgrades.
	var records []lrclibRecord
	if err := c.getJSON(ctx, lrclibSearchURL, url.Values{"track_name": {req.Title}}, 8*time.Second, &records); err != nil {
		return nil, err
	}
	best := lrclibBest(records, req)
	switch {
	case best != nil && (exact == nil || best.tier() > 0):
		return decodeLrclib(*best)
	case exact != nil:
		return decodeLrclib(*exact)
	}
	return nil, errNotFound
}

// Search pads results with other songs, other artists and other cuts of the same recording.
func lrclibBest(records []lrclibRecord, req Request) *lrclibRecord {
	var best *lrclibRecord
	for i := range records {
		record := &records[i]
		if !req.matchesDuration(record.seconds()) || !req.matchesTrack(record.TrackName, record.artists()) {
			continue
		}
		if best == nil || record.outranks(*best, req) {
			best = record
		}
	}
	return best
}

// Within a sync tier the closest duration wins, a record timed for another cut drifts.
func (r lrclibRecord) outranks(other lrclibRecord, req Request) bool {
	if r.tier() != other.tier() {
		return r.tier() > other.tier()
	}
	return req.durationGap(r.seconds()) < req.durationGap(other.seconds())
}

func (r lrclibRecord) tier() int {
	switch {
	case r.HasWordSync:
		return 2
	case r.SyncedLyrics != "":
		return 1
	}
	return 0
}

func (r lrclibRecord) seconds() int {
	return int(math.Round(r.Duration))
}

// YouTube's auto-generated channels are "<artist> - Topic", uploads scraped from them keep it.
func (r lrclibRecord) artists() []string {
	name := strings.TrimSuffix(r.ArtistName, " - Topic")
	return strings.FieldsFunc(name, func(c rune) bool { return strings.ContainsRune(",;/&\x00", c) })
}

func decodeLrclib(record lrclibRecord) (*Lyrics, error) {
	if record.Instrumental {
		return &Lyrics{Instrumental: true}, nil
	}
	var lyricsfileErr error
	if record.Lyricsfile != "" {
		parsed, err := ParseLyricsfile([]byte(record.Lyricsfile))
		if err == nil && !parsed.Empty() {
			return record.stamp(parsed), nil
		}
		lyricsfileErr = err
	}

	parsed, err := ParseLRC([]byte(record.SyncedLyrics))
	if err != nil {
		return nil, err
	}
	if parsed.Plain == "" {
		if strings.Count(record.PlainLyrics, "\n")+1 > MaxLines {
			return nil, ErrTooLarge
		}
		parsed.Plain = record.PlainLyrics
	}
	if parsed.Empty() && lyricsfileErr != nil {
		return nil, lyricsfileErr
	}
	return record.stamp(parsed), nil
}

func (r lrclibRecord) stamp(lyrics *Lyrics) *Lyrics {
	if lyrics.DurationMS == 0 {
		lyrics.DurationMS = Seconds(r.Duration).ms()
	}
	return lyrics
}

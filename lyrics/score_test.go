package lyrics

import (
	"cmp"
	"context"
	"encoding/json"
	"testing"
	"time"
)

func timeline(offset Seconds, words bool, starts ...Seconds) *Lyrics {
	texts := []string{"Riding for the battlefield", "Fury of the darkest evil", "Far beyond the boundaries", "On the road to lands unknown", "Through the caverns"}
	lyrics := &Lyrics{}
	for i, start := range starts {
		line := Line{Start: start + offset, Text: texts[i%len(texts)]}
		if words {
			line.Words = []Word{{Start: line.Start, Text: line.Text}}
		}
		lyrics.Synced = append(lyrics.Synced, line)
	}
	return lyrics
}

func withLength(lyrics *Lyrics, seconds Seconds) *Lyrics {
	lyrics.DurationMS = seconds.ms()
	return lyrics
}

func TestSourcesReportTrackLength(t *testing.T) {
	var plus lyricsPlusResponse
	if err := json.Unmarshal([]byte(`{"type":"Line","metadata":{"totalDuration":"3:21.570"},"lyrics":[{"time":1200,"text":"Hello"}]}`), &plus); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		parse func() (*Lyrics, error)
		want  int64
	}{
		{"ttml body", func() (*Lyrics, error) {
			return ParseTTML([]byte(`<tt xmlns="http://www.w3.org/ns/ttml"><body dur="6:46.827"><div><p begin="51.301" end="54.244">We are riding</p></div></body></tt>`))
		}, 406_827},
		{"lrc length tag", func() (*Lyrics, error) { return ParseLRC([]byte("[length: 03:25.50]\n[00:11]What a thrill")) }, 205_500},
		{"lyricsplus metadata", func() (*Lyrics, error) { return decodeLyricsPlus(plus) }, 201_570},
	} {
		lyrics, err := tc.parse()
		if err != nil || lyrics.DurationMS != tc.want {
			t.Errorf("%s: DurationMS=%v err=%v, want %d", tc.name, lyrics, err, tc.want)
		}
	}
}

func TestPickRanksTimelineFitAboveSyncAndPriority(t *testing.T) {
	even := []Seconds{50, 55, 60, 65, 70}
	for _, tc := range []struct {
		name    string
		seconds int
		answers map[Provider]*Lyrics
		want    Provider
	}{
		{
			name: "offset outlier loses consensus",
			answers: map[Provider]*Lyrics{
				BetterLyrics: timeline(4, true, even...),
				LRCLIB:       timeline(0, false, even...),
				YouTubeMusic: timeline(0.2, false, even...),
			},
			want: LRCLIB,
		},
		{
			name: "drifting outlier loses consensus",
			answers: map[Provider]*Lyrics{
				BetterLyrics: timeline(0, true, 50, 56, 62, 68, 74),
				LRCLIB:       timeline(0, false, even...),
				YouTubeMusic: timeline(0.2, false, even...),
			},
			want: LRCLIB,
		},
		{
			name: "timeline past the track end loses",
			answers: map[Provider]*Lyrics{
				BetterLyrics: timeline(0, true, 150, 160, 170, 180, 190),
				LRCLIB:       timeline(0, false, even...),
			},
			want: LRCLIB,
		},
		{
			name: "lrclib duration for another cut loses",
			answers: map[Provider]*Lyrics{
				LRCLIB:       func() *Lyrics { l := timeline(0, false, even...); l.DurationMS = 160_000; return l }(),
				YouTubeMusic: timeline(1, false, even...),
			},
			want: YouTubeMusic,
		},
		{
			name:    "lyrics for a cut with intro footage lose",
			seconds: 406,
			answers: map[Provider]*Lyrics{
				KuGou:  withLength(timeline(10, true, even...), 416),
				LRCLIB: timeline(0, false, even...),
			},
			want: LRCLIB,
		},
		{
			name:    "reported length over 1% off loses",
			seconds: 325,
			answers: map[Provider]*Lyrics{
				KuGou:  withLength(timeline(0, true, even...), 328.5),
				LRCLIB: timeline(1, false, even...),
			},
			want: LRCLIB,
		},
		{
			name:    "reported length within 1% is kept",
			seconds: 325,
			answers: map[Provider]*Lyrics{
				KuGou:  withLength(timeline(0, true, even...), 328),
				LRCLIB: timeline(1, false, even...),
			},
			want: KuGou,
		},
		{
			name: "matching reported length beats an unknown one",
			answers: map[Provider]*Lyrics{
				BetterLyrics: timeline(0, false, even...),
				LRCLIB:       withLength(timeline(1, false, even...), 120),
			},
			want: LRCLIB,
		},
		{
			name:    "late word sync loses to two close line syncs",
			seconds: 451,
			answers: map[Provider]*Lyrics{
				KuGou:        timeline(2.3, true, even...),
				LRCLIB:       timeline(0, false, even...),
				YouTubeMusic: timeline(0.53, false, even...),
			},
			want: LRCLIB,
		},
		{
			name: "provider order decides among agreeing timelines",
			answers: map[Provider]*Lyrics{
				LRCLIB:       timeline(0, false, even...),
				YouTubeMusic: timeline(0.1, true, even...),
			},
			want: LRCLIB,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := isolate(t)
			client.syncGrace = time.Minute
			stub(client, func(_ context.Context, provider Provider, _ Request) (*Lyrics, error) {
				if answer, ok := tc.answers[provider]; ok {
					return answer, nil
				}
				return nil, errNotFound
			})
			seconds := cmp.Or(tc.seconds, 120)
			result, err := client.Lookup(context.Background(), Request{Artist: "DragonForce", Title: "Fury of the Storm", Duration: time.Duration(seconds) * time.Second, Providers: []Provider{KuGou, BetterLyrics, LRCLIB, YouTubeMusic}})
			if err != nil || result.Source != tc.want {
				t.Fatalf("source=%s err=%v, want %s", result.Source, err, tc.want)
			}
		})
	}
}

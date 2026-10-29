package lyrics

import (
	"math"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type Provider string

const (
	LRCLIB       Provider = "lrclib"
	BetterLyrics Provider = "betterlyrics"
	LyricsPlus   Provider = "lyricsplus"
	Unison       Provider = "unison"
	KuGou        Provider = "kugou"
	YouTubeMusic Provider = "youtubemusic"

	// Sidecar is a Result.Source only, not requestable.
	Sidecar Provider = "sidecar"
)

// DefaultProviders is used when Request.Providers is nil.
func DefaultProviders() []Provider {
	return []Provider{BetterLyrics, Unison, LyricsPlus, KuGou, LRCLIB, YouTubeMusic}
}

// Seconds from the start of the track.
type Seconds float64

func (s Seconds) Duration() time.Duration {
	return time.Duration(float64(s) * float64(time.Second))
}

func milliseconds(ms int64) Seconds {
	return Seconds(float64(ms) / 1000)
}

func (s Seconds) ms() int64 {
	return int64(math.Round(float64(s) * 1000))
}

func (s Seconds) finite() bool {
	return !math.IsInf(float64(s), 0) && !math.IsNaN(float64(s))
}

type VoiceID string

type VoiceType string

const (
	VoicePerson VoiceType = "person"
	VoiceGroup  VoiceType = "group"
)

type Voice struct {
	Name string    `json:"name,omitempty"`
	Type VoiceType `json:"type,omitempty"`
}

func boundedVoiceID(value string) VoiceID {
	if len(value) > 128 {
		return ""
	}
	return VoiceID(value)
}

func voiceMetadata(name, kind string) Voice {
	if len(name) > 512 {
		name = ""
	}
	switch VoiceType(kind) {
	case VoicePerson, VoiceGroup:
		return Voice{Name: name, Type: VoiceType(kind)}
	}
	return Voice{Name: name}
}

// Parser is the shape of ParseLRC, ParseTTML and ParseLyricsfile.
type Parser func(data []byte) (*Lyrics, error)

// Concatenating a line's Word.Text gives Line.Text exactly.
type Word struct {
	Start Seconds `json:"t"`
	End   Seconds `json:"e,omitempty"`
	Text  string  `json:"x"`
}

type Line struct {
	Start Seconds `json:"t"`
	End   Seconds `json:"e,omitempty"`
	Text  string  `json:"x"`
	Words []Word  `json:"w,omitempty"`
	// Voice is a key into Lyrics.Voices.
	Voice      VoiceID `json:"voice,omitempty"`
	Background bool    `json:"background,omitempty"`
	// Group is shared by lines split from one source line (lead + backing vocal). 0 = not grouped.
	Group int `json:"group,omitempty"`
}

// SourceFormat identifies the parser that produced a Lyrics value.
type SourceFormat uint8

const (
	SourceUnknown SourceFormat = iota
	SourceLRC
	SourceTTML
	SourceLyricsfile
)

type Lyrics struct {
	// Synced is sorted by Start, empty if the source has no timing.
	Synced       []Line            `json:"synced"`
	Plain        string            `json:"plain"`
	Instrumental bool              `json:"instrumental"`
	Voices       map[VoiceID]Voice `json:"voices,omitempty"`
	// Title, Artist, Album and DurationMS identify the track the lyrics belong to.
	// Parsers fill what their source carries; callers fill the rest.
	Title      string       `json:"title,omitempty"`
	Artist     string       `json:"artist,omitempty"`
	Album      string       `json:"album,omitempty"`
	DurationMS int64        `json:"duration_ms,omitempty"`
	Source     SourceFormat `json:"-"`
	// raw is the verbatim input ParseLyricsfile received.
	raw []byte
}

func sortLines(lines []Line) {
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Start < lines[j].Start })
}

func joinWords(words []Word) string {
	var text strings.Builder
	for _, word := range words {
		text.WriteString(word.Text)
	}
	return text.String()
}

func trimWords(words []Word) []Word {
	for len(words) > 0 {
		words[0].Text = strings.TrimLeftFunc(words[0].Text, unicode.IsSpace)
		if words[0].Text != "" {
			break
		}
		words = words[1:]
	}
	for len(words) > 0 {
		last := len(words) - 1
		words[last].Text = strings.TrimRightFunc(words[last].Text, unicode.IsSpace)
		if words[last].Text != "" {
			break
		}
		words = words[:last]
	}
	return words
}

func (l *Lyrics) timed() bool {
	return slices.ContainsFunc(l.Synced, func(line Line) bool {
		return line.Start > 0 || line.End > 0 || slices.ContainsFunc(line.Words, func(word Word) bool { return word.Start > 0 || word.End > 0 })
	})
}

func (l *Lyrics) wordSynced() bool {
	return slices.ContainsFunc(l.Synced, func(line Line) bool { return len(line.Words) > 0 })
}

func (l *Lyrics) Empty() bool {
	return l == nil || (!l.Instrumental && len(l.Synced) == 0 && strings.TrimSpace(l.Plain) == "")
}

type Request struct {
	Title  string
	Artist string
	Album  string
	// Duration is rounded to the second. 0 = unknown.
	Duration time.Duration
	// FileURL is the track's file:// URL. A .lrc or .lyricsfile.yaml next to it beats every provider.
	FileURL string
	// CacheOnly never hits the network.
	CacheOnly bool
	// Providers in priority order. nil = DefaultProviders, empty non-nil = sidecars only.
	Providers []Provider
}

func (r Request) seconds() int {
	return max(0, int(r.Duration.Round(time.Second)/time.Second))
}

const durationSlack = 8

var bracketed = regexp.MustCompile(`[(\[（【][^)\]）】]*[)\]）】]`)

// matchesDuration allows for catalogs that time the same recording a few seconds apart.
func (r Request) matchesDuration(seconds int) bool {
	return r.durationGap(seconds) <= durationSlack
}

// durationGap is 0 when the request has no duration.
func (r Request) durationGap(seconds int) int {
	want := r.seconds()
	if want == 0 {
		return 0
	}
	return max(want-seconds, seconds-want)
}

// Provider searches pad their results with other songs by the artist and other people's uploads.
func (r Request) matchesTrack(title string, artists []string) bool {
	name := searchable(title)
	if name == "" || !containsWords(searchable(r.Title), name) {
		return false
	}
	known := searchable(r.Artist + " " + r.Title)
	for _, artist := range artists {
		if artist := searchable(artist); artist != "" && containsWords(known, artist) {
			return true
		}
	}
	return false
}

// searchable drops notes like "(Radio Edit)", quotes and punctuation.
func searchable(text string) string {
	text = bracketed.ReplaceAllString(strings.ToLower(text), " ")
	return strings.Join(strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}), " ")
}

// addsNote is a bracketed note like "(Live)" in title that the requested title lacks.
func (r Request) addsNote(title string) bool {
	want := strings.ToLower(r.Title)
	for _, note := range bracketed.FindAllString(strings.ToLower(title), -1) {
		if !strings.Contains(want, strings.Trim(note, "()[]（）【】")) {
			return true
		}
	}
	return false
}

func containsWords(text, words string) bool {
	return strings.Contains(" "+text+" ", " "+words+" ")
}

func (r Request) query(title, artist, album, duration string) url.Values {
	query := url.Values{title: {r.Title}, artist: {r.Artist}}
	if r.Album != "" {
		query.Set(album, r.Album)
	}
	if seconds := r.seconds(); seconds > 0 {
		query.Set(duration, strconv.Itoa(seconds))
	}
	return query
}

type Result struct {
	Found       bool        `json:"found"`
	Source      Provider    `json:"source"`
	Cached      bool        `json:"cached"`
	Attribution Attribution `json:"attribution,omitzero"`
	Lyrics
}

package lyrics

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	youtubeMusicAPI  = "https://music.youtube.com/youtubei/v1/"
	youtubeSongsOnly = "EgWKAQIIAWoMEA4QChADEAQQCRAF"
	youtubeLyricsTab = "MUSIC_PAGE_TYPE_TRACK_LYRICS"

	youtubeSongs = "contents.tabbedSearchResultsRenderer.tabs.tabRenderer.content.sectionListRenderer.contents.musicShelfRenderer.contents.musicResponsiveListItemRenderer"
	youtubeTabs  = "contents.singleColumnMusicWatchNextResultsRenderer.tabbedRenderer.watchNextTabbedResultsRenderer.tabs.tabRenderer"
	youtubeRows  = "contents.elementRenderer.newElement.type.componentType.model.timedLyricsModel.lyricsData.timedLyricsData"

	youtubeColumns  = "flexColumns.musicResponsiveListItemFlexColumnRenderer"
	youtubeRuns     = youtubeColumns + ".text.runs.text"
	youtubeBrowse   = "endpoint.browseEndpoint"
	youtubePageType = youtubeBrowse + ".browseEndpointContextSupportedConfigs.browseEndpointContextMusicConfig.pageType"
)

// Only the mobile client gets timed lyrics.
var youtubeMobile = youtubeClient{Name: "ANDROID_MUSIC", Version: "7.21.50", Language: "en"}

type youtubeClient struct {
	Name     string `json:"clientName"`
	Version  string `json:"clientVersion"`
	Language string `json:"hl"`
}

type youtubeContext struct {
	Client youtubeClient `json:"client"`
}

type youtubeRequest struct {
	Context  youtubeContext `json:"context"`
	Query    string         `json:"query,omitempty"`
	Params   string         `json:"params,omitempty"`
	VideoID  string         `json:"videoId,omitempty"`
	BrowseID string         `json:"browseId,omitempty"`
}

type youtubeLine struct {
	Text string      `json:"lyricLine"`
	Cue  *youtubeCue `json:"cueRange"`
}

type youtubeCue struct {
	Start string `json:"startTimeMilliseconds"`
	End   string `json:"endTimeMilliseconds"`
}

func (c *Client) fetchYouTubeMusic(ctx context.Context, req Request) (*Lyrics, error) {
	web := youtubeContext{Client: youtubeClient{Name: "WEB_REMIX", Version: "1." + time.Now().UTC().Format("20060102") + ".01.00", Language: youtubeLanguage(req)}}

	search, err := c.youtube(ctx, "search", youtubeSongs+"(playlistItemData.videoId,"+youtubeRuns+")",
		youtubeRequest{Context: web, Query: req.Title + " " + req.Artist, Params: youtubeSongsOnly})
	if err != nil {
		return nil, err
	}
	videoID, seconds := youtubeVideoID(search, req)
	if videoID == "" {
		return nil, errNotFound
	}

	next, err := c.youtube(ctx, "next", youtubeTabs+"(unselectable,"+youtubeBrowse+"(browseId,browseEndpointContextSupportedConfigs))",
		youtubeRequest{Context: web, VideoID: videoID})
	if err != nil {
		return nil, err
	}
	lyricsID := youtubeLyricsID(next)
	if lyricsID == "" {
		return nil, errNotFound
	}

	page, err := c.youtube(ctx, "browse", youtubeRows,
		youtubeRequest{Context: youtubeContext{Client: youtubeMobile}, BrowseID: lyricsID})
	if err != nil {
		return nil, err
	}
	found := dig(page, youtubeRows)
	if len(found) == 0 {
		return nil, errNotFound
	}
	var rows []youtubeLine
	if err := json.Unmarshal(found[0], &rows); err != nil {
		return nil, err
	}
	lyrics, err := decodeYouTubeMusic(rows)
	if err == nil && lyrics.DurationMS == 0 {
		lyrics.DurationMS = int64(seconds) * 1000
	}
	return lyrics, err
}

func (c *Client) youtube(ctx context.Context, endpoint, fields string, body youtubeRequest) (json.RawMessage, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, youtubeMusicAPI+endpoint+"?prettyPrint=false", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// the lyrics page alone is over MaxSourceBytes without a field mask
	req.Header.Set("X-Goog-FieldMask", fields)
	response, err := c.do(req)
	if err != nil {
		return nil, err
	}
	var payload json.RawMessage
	return payload, decodeJSON(response, &payload)
}

// Search lists music videos with intro footage next to the album track, so the closest length wins.
func youtubeVideoID(search json.RawMessage, req Request) (string, int) {
	bestID, bestSeconds := "", 0
	for _, song := range dig(search, youtubeSongs) {
		columns := dig(song, youtubeColumns)
		if len(columns) < 2 {
			continue
		}
		title := strings.Join(digStrings(columns[0], "text.runs.text"), "")
		details := digStrings(columns[1], "text.runs.text")
		videoID := digString(song, "playlistItemData.videoId")
		seconds := youtubeSeconds(details)
		if videoID == "" || !req.matchesDuration(seconds) || !req.matchesTrack(title, youtubeArtists(details)) {
			continue
		}
		if bestID == "" || req.durationGap(seconds) < req.durationGap(bestSeconds) {
			bestID, bestSeconds = videoID, seconds
		}
	}
	return bestID, bestSeconds
}

// Search localizes names: with hl=en 米津玄師 comes back as "Kenshi Yonezu" and 아이유 as "IU".
func youtubeLanguage(req Request) string {
	for _, r := range req.Artist + req.Title {
		switch {
		case unicode.Is(unicode.Hangul, r):
			return "ko"
		case unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana):
			return "ja"
		}
	}
	return "en"
}

// A song's details read "Kygo", " & ", "Zak Abel", " • ", album, " • ", "3:19".
func youtubeArtists(details []string) []string {
	if end := slices.Index(details, " • "); end >= 0 {
		return details[:end]
	}
	return details
}

func youtubeSeconds(details []string) int {
	if len(details) == 0 {
		return 0
	}
	seconds, _ := clockSeconds(details[len(details)-1])
	return seconds
}

func youtubeLyricsID(next json.RawMessage) string {
	for _, tab := range dig(next, youtubeTabs) {
		if digString(tab, youtubePageType) == youtubeLyricsTab && len(dig(tab, "unselectable")) == 0 {
			return digString(tab, youtubeBrowse+".browseId")
		}
	}
	return ""
}

func clockSeconds(text string) (int, bool) {
	parts := strings.Split(text, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	total := 0
	for _, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return 0, false
		}
		total = total*60 + value
	}
	return total, true
}

func decodeYouTubeMusic(rows []youtubeLine) (*Lyrics, error) {
	if len(rows) > MaxLines {
		return nil, ErrTooLarge
	}
	plain := make([]string, len(rows))
	synced := make([]Line, 0, len(rows))
	for i, row := range rows {
		plain[i] = row.Text
		if line, ok := row.line(); ok {
			synced = append(synced, line)
		}
	}
	result := &Lyrics{Plain: strings.Join(plain, "\n")}
	if len(synced) == len(rows) {
		sortLines(synced)
		result.Synced = synced
	}
	return result, nil
}

func (l youtubeLine) line() (Line, bool) {
	if l.Cue == nil {
		return Line{}, false
	}
	start, err := strconv.ParseInt(l.Cue.Start, 10, 64)
	if err != nil {
		return Line{}, false
	}
	end, err := strconv.ParseInt(l.Cue.End, 10, 64)
	if err != nil {
		return Line{}, false
	}
	return Line{Start: milliseconds(start), End: milliseconds(end), Text: l.Text}, true
}

// dig reads a path the way a field mask does, stepping into every element of an array on the way.
func dig(data json.RawMessage, path string) []json.RawMessage {
	values := []json.RawMessage{data}
	for key := range strings.SplitSeq(path, ".") {
		var next []json.RawMessage
		for _, value := range values {
			for _, item := range jsonItems(value) {
				var object map[string]json.RawMessage
				if json.Unmarshal(item, &object) == nil && object[key] != nil {
					next = append(next, object[key])
				}
			}
		}
		values = next
	}
	return values
}

func jsonItems(value json.RawMessage) []json.RawMessage {
	var items []json.RawMessage
	if json.Unmarshal(value, &items) == nil {
		return items
	}
	return []json.RawMessage{value}
}

func digStrings(data json.RawMessage, path string) []string {
	var texts []string
	for _, value := range dig(data, path) {
		var text string
		if json.Unmarshal(value, &text) == nil {
			texts = append(texts, text)
		}
	}
	return texts
}

func digString(data json.RawMessage, path string) string {
	texts := digStrings(data, path)
	if len(texts) == 0 {
		return ""
	}
	return texts[0]
}

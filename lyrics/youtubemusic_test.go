package lyrics

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestYouTubeMusicPicksTheMatchingSong(t *testing.T) {
	song := func(id, title, artist, length string) string {
		return `{"musicResponsiveListItemRenderer":{"playlistItemData":{"videoId":"` + id + `"},"flexColumns":[` +
			`{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":` + strconv.Quote(title) + `}]}}},` +
			`{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"` + artist + `"},{"text":" • "},{"text":` + strconv.Quote(title) + `},{"text":" • "},{"text":"` + length + `"}]}}}]}}`
	}
	search := json.RawMessage(`{"contents":{"tabbedSearchResultsRenderer":{"tabs":[{"tabRenderer":{"content":{"sectionListRenderer":{"contents":[{"musicShelfRenderer":{"contents":[` +
		song("", `Dat New "New"`, "Kid Cudi", "4:15") + `,` +
		song("cudizone", "Cudi Zone", "Kid Cudi", "4:20") + `,` +
		song("remix", "Dat new new (Remix)", "DCG", "4:15") + `,` +
		song("video", `Dat New "New"`, "Kid Cudi", "4:21") + `,` +
		song("original", `Dat New "New"`, "Kid Cudi", "4:15") + `]}}]}}}}]}}}`)
	req := Request{Title: `Kid Cudi - Dat New "New" (Dirty)`, Artist: "Fool's Gold Records", Duration: 255 * time.Second}
	if got, seconds := youtubeVideoID(search, req); got != "original" || seconds != 255 {
		t.Fatalf("picked %q at %ds", got, seconds)
	}

	next := json.RawMessage(`{"contents":{"singleColumnMusicWatchNextResultsRenderer":{"tabbedRenderer":{"watchNextTabbedResultsRenderer":{"tabs":[` +
		`{"tabRenderer":{}},{"tabRenderer":{"unselectable":true,"endpoint":{"browseEndpoint":{"browseId":"MPLYt_none","browseEndpointContextSupportedConfigs":{"browseEndpointContextMusicConfig":{"pageType":"MUSIC_PAGE_TYPE_TRACK_LYRICS"}}}}}}]}}}}}`)
	if got := youtubeLyricsID(next); got != "" {
		t.Fatalf("followed an unavailable lyrics tab to %q", got)
	}
}

func TestYouTubeMusicTiming(t *testing.T) {
	var rows []youtubeLine
	err := json.Unmarshal([]byte(`[{"lyricLine":"Hello","cueRange":{"startTimeMilliseconds":"1500","endTimeMilliseconds":"3000"}},{"lyricLine":"world","cueRange":{"startTimeMilliseconds":"3000","endTimeMilliseconds":"4500"}}]`), &rows)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeYouTubeMusic(rows)
	want := []Line{{Start: 1.5, End: 3, Text: "Hello"}, {Start: 3, End: 4.5, Text: "world"}}
	if err != nil || !reflect.DeepEqual(got.Synced, want) || got.Plain != "Hello\nworld" {
		t.Fatalf("got %+v, %v", got, err)
	}

	rows[1].Cue = nil
	got, err = decodeYouTubeMusic(rows)
	if err != nil || got.Synced != nil || got.Plain != "Hello\nworld" {
		t.Fatalf("partly timed lyrics kept their timing: %+v, %v", got, err)
	}
}

package lyrics

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestConcurrentProviderPriorityAndCancellation(t *testing.T) {
	for _, highFound := range []bool{true, false} {
		t.Run(map[bool]string{true: "highest wins", false: "fallback wins"}[highFound], func(t *testing.T) {
			client := isolate(t)
			releaseHigh := make(chan struct{})
			lowerReady := make(chan struct{})
			lastStarted := make(chan struct{})
			lastCanceled := make(chan struct{})
			stub(client, func(ctx context.Context, provider Provider, _ Request) (*Lyrics, error) {
				switch provider {
				case "betterlyrics":
					<-releaseHigh
					if !highFound {
						return nil, errNotFound
					}
					return &Lyrics{Plain: "highest priority"}, nil
				case "lrclib":
					close(lowerReady)
					return &Lyrics{Plain: "second priority"}, nil
				default:
					close(lastStarted)
					<-ctx.Done()
					close(lastCanceled)
					return nil, ctx.Err()
				}
			})
			response := make(chan providerResponse, 1)
			go func() {
				result, err := client.Lookup(context.Background(), Request{Artist: "Artist", Title: "Track", Providers: []Provider{BetterLyrics, LRCLIB, LyricsPlus}})
				response <- providerResponse{result: result, err: err}
			}()
			<-lowerReady
			<-lastStarted
			select {
			case <-response:
				t.Fatal("lower priority won before the first provider finished")
			default:
			}
			close(releaseHigh)
			result := <-response
			want := LRCLIB
			if highFound {
				want = BetterLyrics
			}
			if result.err != nil || result.result.Source != want {
				t.Fatalf("result=%+v err=%v", result.result, result.err)
			}
			select {
			case <-lastCanceled:
			case <-time.After(time.Second):
				t.Fatal("unused provider was not canceled")
			}
		})
	}
}

func TestLineSyncOutranksPlainAtHigherPriority(t *testing.T) {
	client := isolate(t)
	client.syncGrace = time.Minute
	plainReady := make(chan struct{})
	stub(client, func(_ context.Context, provider Provider, _ Request) (*Lyrics, error) {
		if provider == BetterLyrics {
			close(plainReady)
			return &Lyrics{Plain: "plain"}, nil
		}
		<-plainReady
		return &Lyrics{Synced: []Line{{Start: 1, Text: "line synced"}}}, nil
	})
	result, err := client.Lookup(context.Background(), Request{Artist: "Artist", Title: "Track", Providers: []Provider{BetterLyrics, LRCLIB}})
	if err != nil || result.Source != LRCLIB {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestProviderCacheAndDisabledProviders(t *testing.T) {
	client := isolate(t)
	calls := map[Provider]int{}
	var mu sync.Mutex
	stub(client, func(_ context.Context, provider Provider, _ Request) (*Lyrics, error) {
		mu.Lock()
		calls[provider]++
		mu.Unlock()
		if provider == "betterlyrics" {
			return nil, errNotFound
		}
		return &Lyrics{Plain: "cached line"}, nil
	})
	req := Request{Artist: "Artist", Title: "Track", Providers: []Provider{BetterLyrics, LyricsPlus}}
	for range 2 {
		result, err := client.Lookup(context.Background(), req)
		if err != nil || result.Source != "lyricsplus" {
			t.Fatalf("fallback=%+v err=%v", result, err)
		}
	}
	if !reflect.DeepEqual(calls, map[Provider]int{"betterlyrics": 1, "lyricsplus": 1}) {
		t.Fatalf("cached lookup fetched again: %v", calls)
	}
	req.Providers = []Provider{}
	result, err := client.Lookup(context.Background(), req)
	if err != nil || result.Found || len(calls) != 2 {
		t.Fatalf("disabled providers returned remote data: %+v, %v, calls=%v", result, err, calls)
	}
	req.Providers = []Provider{LRCLIB}
	if _, err := client.Lookup(context.Background(), req); err != nil || len(calls) != 3 || calls["lrclib"] != 1 {
		t.Fatalf("provider cache leaked: calls=%v err=%v", calls, err)
	}
}

func TestProviderFailureFallsBack(t *testing.T) {
	client := isolate(t)
	stub(client, func(_ context.Context, provider Provider, _ Request) (*Lyrics, error) {
		if provider == "betterlyrics" {
			return nil, errors.New("provider unavailable")
		}
		return &Lyrics{Plain: "usable lyrics"}, nil
	})
	result, err := client.Lookup(context.Background(), Request{Artist: "Artist", Title: "Track", Providers: []Provider{BetterLyrics, LyricsPlus}})
	if err != nil || !result.Found || result.Plain != "usable lyrics" {
		t.Fatalf("fallback=%+v err=%v", result, err)
	}
}

func TestProviderErrorsAreNotCached(t *testing.T) {
	client := isolate(t)
	calls := 0
	stub(client, func(context.Context, Provider, Request) (*Lyrics, error) {
		calls++
		return nil, &StatusError{Code: http.StatusServiceUnavailable}
	})
	req := Request{Artist: "Artist", Title: "Track", Providers: []Provider{BetterLyrics}}
	for range 2 {
		if _, err := client.Lookup(context.Background(), req); err == nil {
			t.Fatal("provider error was hidden")
		}
	}
	if calls != 2 {
		t.Fatal("transient provider failure was cached as missing lyrics")
	}
}

func TestLyricsPlusWordTiming(t *testing.T) {
	var payload lyricsPlusResponse
	err := json.Unmarshal([]byte(`{"type":"Word","lyrics":[{"time":1200,"text":"Hello world","syllabus":[{"time":1200,"duration":400,"text":"Hello "},{"time":1750,"duration":750,"text":"world"}]}]}`), &payload)
	if err != nil {
		t.Fatal(err)
	}
	result, err := decodeLyricsPlus(payload)
	if err != nil || result.Synced[0].Start != 1.2 || !reflect.DeepEqual(result.Synced[0].Words, []Word{{Start: 1.2, End: 1.6, Text: "Hello "}, {Start: 1.75, End: 2.5, Text: "world"}}) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	payload.Lyrics[0].Words[0].Text = "wrong text"
	result, err = decodeLyricsPlus(payload)
	if err != nil || len(result.Synced[0].Words) != 0 || result.Synced[0].Text != "Hello world" {
		t.Fatalf("invalid word timing did not retain the original line: %+v, %v", result, err)
	}
}

func TestLyricsPlusVoicesAndBacking(t *testing.T) {
	var payload lyricsPlusResponse
	err := json.Unmarshal([]byte(`{"type":"Word","metadata":{"agents":{"voice1":{"type":"person","name":"Singer","alias":"v1"}}},"lyrics":[{"time":1000,"duration":4000,"text":"Lead Echo","element":{"singer":"v1"},"syllabus":[{"time":2000,"duration":2000,"text":"Lead "},{"time":1000,"duration":4000,"text":"Echo","isBackground":true}]}]}`), &payload)
	if err != nil {
		t.Fatal(err)
	}
	result, err := decodeLyricsPlus(payload)
	want := []Line{
		{Start: 1, End: 5, Text: "Echo", Voice: "v1", Background: true, Group: 1, Words: []Word{{Start: 1, End: 5, Text: "Echo"}}},
		{Start: 2, End: 4, Text: "Lead", Voice: "v1", Group: 1, Words: []Word{{Start: 2, End: 4, Text: "Lead"}}},
	}
	if err != nil || !reflect.DeepEqual(result.Synced, want) || result.Voices["v1"].Name != "Singer" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestLyricsPlusBoundsIncludeUntimedFallbacks(t *testing.T) {
	start := int64(0)
	payload := lyricsPlusResponse{Type: "Word"}
	for range MaxLines / 2 {
		payload.Lyrics = append(payload.Lyrics, lyricsPlusLine{Time: &start, Text: "LeadEcho", Words: []lyricsPlusWord{{Time: &start, Text: "Lead"}, {Time: &start, Text: "Echo", Background: true}}})
	}
	for range MaxLines / 2 {
		payload.Lyrics = append(payload.Lyrics, lyricsPlusLine{Time: &start, Text: "Untimed"})
	}
	if _, err := decodeLyricsPlus(payload); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expanded lines error=%v", err)
	}
}

// betterlyrics answers 401 for a track it has not cached, so a status-only miss
// must not surface as an error, and must not mask another provider's real answer.
func TestLyricsPlusAllZeroTimingIsAMiss(t *testing.T) {
	zero, later := int64(0), int64(3725)
	payload := lyricsPlusResponse{Type: "Word", Lyrics: []lyricsPlusLine{{Time: &zero, Text: "A crumbling Georgian folly"}, {Time: &zero, Text: "Fantastic shapes absurd"}}}
	if _, err := decodeLyricsPlus(payload); !errors.Is(err, errNotFound) {
		t.Fatalf("err=%v", err)
	}
	payload.Lyrics[1].Time = &later
	if result, err := decodeLyricsPlus(payload); err != nil || len(result.Synced) != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestProviderMissStatusesAreNotErrors(t *testing.T) {
	for status, wantMiss := range map[int]bool{
		http.StatusNotFound: true, http.StatusUnauthorized: true, http.StatusForbidden: true,
		http.StatusTooManyRequests: false, http.StatusInternalServerError: false,
	} {
		got := classifyStatus(status)
		if errors.Is(got, errNotFound) != wantMiss {
			t.Fatalf("status %d: got %v, treated as miss=%v", status, got, !wantMiss)
		}
	}
}

func TestDefiniteMissOutranksProviderFailure(t *testing.T) {
	client := isolate(t)
	stub(client, func(_ context.Context, provider Provider, _ Request) (*Lyrics, error) {
		if provider == "betterlyrics" {
			return nil, &StatusError{Code: http.StatusTooManyRequests}
		}
		return &Lyrics{}, nil
	})
	result, err := client.Lookup(context.Background(), Request{
		Artist: "Artist", Title: "Track",
		Providers: []Provider{BetterLyrics, LRCLIB},
	})
	if err != nil || result.Found {
		t.Fatalf("a failing provider hid a real miss: result=%+v err=%v", result, err)
	}
}

func TestEveryProviderFailingStaysAnError(t *testing.T) {
	client := isolate(t)
	stub(client, func(context.Context, Provider, Request) (*Lyrics, error) {
		return nil, &StatusError{Code: http.StatusTooManyRequests}
	})
	if _, err := client.Lookup(context.Background(), Request{
		Artist: "Artist", Title: "Track",
		Providers: []Provider{BetterLyrics, LRCLIB},
	}); err == nil {
		t.Fatal("a total provider outage was reported as missing lyrics")
	}
}

func TestNilProvidersUseDefaults(t *testing.T) {
	client := isolate(t)
	var mu sync.Mutex
	var asked []Provider
	stub(client, func(_ context.Context, provider Provider, _ Request) (*Lyrics, error) {
		mu.Lock()
		asked = append(asked, provider)
		mu.Unlock()
		return nil, errNotFound
	})
	if _, err := client.Lookup(context.Background(), Request{Artist: "Artist", Title: "Track"}); err != nil {
		t.Fatal(err)
	}
	if len(asked) != len(DefaultProviders()) {
		t.Fatalf("nil providers asked %v", asked)
	}
}

package lyrics

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func trackWithSidecar(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "track.lrc"), []byte("[00:11.82]What a thrill"), 0o644); err != nil {
		t.Fatal(err)
	}
	return "file://" + filepath.Join(dir, "track.flac")
}

func TestChoicesListEverySourceWithLyrics(t *testing.T) {
	client := isolate(t)
	stub(client, func(_ context.Context, provider Provider, _ Request) (*Lyrics, error) {
		switch provider {
		case BetterLyrics:
			return &Lyrics{Synced: []Line{{Start: 1, Text: "word", Words: []Word{{Start: 1, Text: "word"}}}}}, nil
		case KuGou:
			return &Lyrics{Synced: []Line{{Start: 1, Text: "another cut"}}, DurationMS: 190_000}, nil
		case LRCLIB:
			time.Sleep(20 * client.syncGrace)
			return &Lyrics{Plain: "slower than the lookup grace"}, nil
		}
		return nil, errNotFound
	})
	req := Request{Artist: "Artist", Title: "Track", Duration: 200 * time.Second, FileURL: trackWithSidecar(t), Providers: []Provider{BetterLyrics, KuGou, Unison, LRCLIB}}

	choices, err := client.Choices(context.Background(), req)
	if err != nil {
		t.Fatalf("Choices() error = %v", err)
	}
	want := []Choice{
		{Source: Sidecar, Sync: SyncLine},
		{Source: BetterLyrics, Attribution: client.sources[BetterLyrics].Attribution, Sync: SyncWord},
		{Source: KuGou, Attribution: client.sources[KuGou].Attribution, Sync: SyncLine, Problem: ProblemLength},
		{Source: LRCLIB, Attribution: client.sources[LRCLIB].Attribution, Sync: SyncNone},
	}
	if !reflect.DeepEqual(choices, want) {
		t.Fatalf("choices = %+v, want %+v", choices, want)
	}
}

func TestChosenSourceOutranksSidecarAndRanking(t *testing.T) {
	client := isolate(t)
	stub(client, func(_ context.Context, provider Provider, _ Request) (*Lyrics, error) {
		switch provider {
		case BetterLyrics:
			return &Lyrics{Synced: []Line{{Start: 1, Text: "ranked first"}}}, nil
		case LRCLIB:
			return &Lyrics{Plain: "ranked last"}, nil
		}
		return nil, errNotFound
	})
	req := Request{Artist: "Artist", Title: "Track", FileURL: trackWithSidecar(t), Providers: []Provider{BetterLyrics, KuGou, LRCLIB}}
	streamed := req
	streamed.FileURL = ""

	steps := []struct {
		name   string
		choose Provider
		req    Request
		want   Provider
	}{
		{"no choice keeps the sidecar", "", req, Sidecar},
		{"chosen provider beats the sidecar", LRCLIB, req, LRCLIB},
		{"chosen provider beats the ranking", LRCLIB, streamed, LRCLIB},
		{"chosen provider without lyrics falls back", KuGou, req, Sidecar},
		{"chosen sidecar that is gone falls back", Sidecar, streamed, BetterLyrics},
		{"cleared choice returns to the ranking", "", streamed, BetterLyrics},
	}
	for _, step := range steps {
		if err := client.Choose(req, step.choose); err != nil {
			t.Fatalf("%s: Choose() error = %v", step.name, err)
		}
		result, err := client.Lookup(context.Background(), step.req)
		if err != nil || result.Source != step.want {
			t.Fatalf("%s: source = %q err = %v, want %q", step.name, result.Source, err, step.want)
		}
	}

	if err := client.Choose(req, LRCLIB); err != nil {
		t.Fatal(err)
	}
	streamed.Providers = []Provider{BetterLyrics}
	result, err := client.Lookup(context.Background(), streamed)
	if err != nil || result.Source != BetterLyrics {
		t.Fatalf("a choice outside the requested providers was used: source = %q err = %v", result.Source, err)
	}
}

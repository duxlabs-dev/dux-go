package lyrics

import (
	"context"
	"reflect"
	"testing"
)

func TestStripMarkup(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"I <3 you", "I <3 you"},
		{"R&B &lt;b&gt; < 5", "R&B &lt;b&gt; < 5"},
		{"I <i>feel</i> fine", "I feel fine"},
		{"<script>alert(1)</script>", "alert(1)"},
		{"<<script>script>alert(1)", "alert(1)"},
		{"<img src=x onerror=alert(1)", "img src=x onerror=alert(1)"},
		{"you </3 me", "you /3 me"},
		{"<!-- x --> <!doctype> <?php", "  ?php"},
		{"<<<a", "a"},
		{"<ü>", "<ü>"},
	} {
		got := stripMarkup(tc.in)
		if got != tc.want || markupOpen.MatchString(got) {
			t.Errorf("stripMarkup(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStripLineAcrossWords(t *testing.T) {
	line := Line{Text: "a <scr ipt>b <", Words: []Word{{Start: 1, Text: "a <scr "}, {Start: 2, Text: "ipt>"}, {Start: 3, Text: "b <"}}}
	got := stripLine(line)
	want := Line{Text: "a b <", Words: []Word{{Start: 1, Text: "a "}, {Start: 3, Text: "b <"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}

	line = Line{Text: "x <b", Words: []Word{{Start: 1, Text: "x <"}, {Start: 2, Text: "b"}}}
	if got := stripLine(line); got.Text != "x b" || joinWords(got.Words) != got.Text {
		t.Fatalf("got %+v", got)
	}
}

func TestLookupStripsMarkup(t *testing.T) {
	client := isolate(t)
	words := []Word{{Start: 1, Text: "<b>hi</b> "}, {Start: 2, Text: "<img src=x>"}}
	stub(client, func(context.Context, Provider, Request) (*Lyrics, error) {
		return &Lyrics{
			Synced: []Line{{Start: 1, Text: joinWords(words), Words: words}},
			Plain:  "<script>hi</script>",
			Voices: map[VoiceID]Voice{"v1": {Name: "<i>Lead</i>"}},
		}, nil
	})

	for range 2 {
		result, err := client.Lookup(context.Background(), Request{Artist: "a", Title: "b", Providers: []Provider{LRCLIB}})
		if err != nil || result.Plain != "hi" || result.Synced[0].Text != "hi " || len(result.Synced[0].Words) != 1 || result.Voices["v1"].Name != "Lead" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
	if words[0].Text != "<b>hi</b> " {
		t.Fatalf("source words mutated: %+v", words)
	}
}

func TestSpaceAfterComma(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"in your city,baby", "in your city, baby"},
		{"天黑了，我想你了", "天黑了， 我想你了"},
		{"one, two", "one, two"},
		{"wait,...", "wait,..."},
		{"50,000 people", "50,000 people"},
		{"1,2,3,go", "1,2,3, go"},
		{"10,baby", "10, baby"},
	} {
		if got := spaceAfterComma(tc.in); got != tc.want {
			t.Errorf("spaceAfterComma(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSpaceLineCopiesWords(t *testing.T) {
	words := []Word{{Start: 1, Text: "city,"}, {Start: 2, Text: "baby "}, {Start: 3, Text: "50,"}, {Start: 4, Text: "000"}}
	got := spaceLine(Line{Text: joinWords(words), Words: words})
	if got.Text != "city, baby 50,000" || joinWords(got.Words) != got.Text {
		t.Fatalf("got %+v", got)
	}
	if words[0].Text != "city," {
		t.Fatalf("source words mutated: %+v", words)
	}
}

package lyrics

import (
	"regexp"
	"strings"
)

// Per the WHATWG tag open state, "<" starts markup only before an ASCII letter, "/", "!" or "?".
var (
	markupTag  = regexp.MustCompile(`<[A-Za-z/!?][^<>]*>`)
	markupOpen = regexp.MustCompile(`<[A-Za-z/!?]`)
)

func (r *Result) withoutMarkup() *Result {
	clean := *r
	changed := false
	strip := func(text string) string {
		stripped := stripMarkup(text)
		changed = changed || stripped != text
		return stripped
	}
	clean.Attribution.Name = strip(r.Attribution.Name)
	clean.Attribution.Text = strip(r.Attribution.Text)
	clean.Plain = spaceAfterComma(strip(r.Plain))
	clean.Title = strip(r.Title)
	clean.Artist = strip(r.Artist)
	clean.Album = strip(r.Album)
	if r.Synced != nil {
		clean.Synced = make([]Line, len(r.Synced))
		for i, line := range r.Synced {
			clean.Synced[i] = spaceLine(stripLine(line))
			changed = changed || clean.Synced[i].Text != line.Text
		}
	}
	if r.Voices != nil {
		clean.Voices = make(map[VoiceID]Voice, len(r.Voices))
		for id, voice := range r.Voices {
			voice.Name = strip(voice.Name)
			clean.Voices[id] = voice
		}
	}
	if changed {
		clean.raw = nil
	}
	return &clean
}

// commaSpace matches a comma glued to the following word. Providers leave that space out for both
// forms: KuGou returns "I'm in your city,b***h" for English and "天黑了，我想你了" for Chinese.
// Periods are deliberately excluded: they occur inside abbreviations, filenames and versions.
var commaSpace = regexp.MustCompile(`([，,])([^\s\p{P}])`)

func spaceAfterComma(text string) string {
	return commaSpace.ReplaceAllString(text, "$1 $2")
}

// spaceLine adds the comma spacing to a line and its words. A comma often ends one word while the
// next word opens a new clause, so the space goes at the end of the word carrying the mark; the line
// text is then rebuilt from the words to keep the documented Line.Text == joinWords(Words).
func spaceLine(line Line) Line {
	if len(line.Words) == 0 {
		line.Text = spaceAfterComma(line.Text)
		return line
	}
	for i := range line.Words {
		line.Words[i].Text = spaceAfterComma(line.Words[i].Text)
		if i+1 < len(line.Words) && gluedComma(line.Words[i].Text) && !strings.HasPrefix(line.Words[i+1].Text, " ") {
			line.Words[i].Text += " "
		}
	}
	line.Text = joinWords(line.Words)
	return line
}

func gluedComma(text string) bool {
	return strings.HasSuffix(text, "，") || strings.HasSuffix(text, ",")
}

func stripMarkup(text string) string {
	if !markupOpen.MatchString(text) {
		return text
	}
	stripped, _ := cutMarkup([]byte(text), nil)
	return string(stripped)
}

// stripLine cuts the joined words so a tag split across words goes too.
func stripLine(line Line) Line {
	if len(line.Words) == 0 {
		line.Text = stripMarkup(line.Text)
		return line
	}
	joined := joinWords(line.Words)
	if !markupOpen.MatchString(joined) {
		return line
	}
	owners := make([]int, 0, len(joined))
	for i, word := range line.Words {
		for range len(word.Text) {
			owners = append(owners, i)
		}
	}
	text, owners := cutMarkup([]byte(joined), owners)
	words := make([]Word, 0, len(line.Words))
	for start := 0; start < len(text); {
		end := start
		for end < len(text) && owners[end] == owners[start] {
			end++
		}
		word := line.Words[owners[start]]
		word.Text = string(text[start:end])
		words = append(words, word)
		start = end
	}
	line.Words = words
	line.Text = joinWords(words)
	return line
}

// cutMarkup repeats until no cut exposes a new tag opener. owners, if set, moves with text.
func cutMarkup(text []byte, owners []int) ([]byte, []int) {
	for {
		cuts := markupTag.FindAllIndex(text, -1)
		if cuts == nil {
			cuts = markupOpen.FindAllIndex(text, -1)
			for _, cut := range cuts {
				cut[1] = cut[0] + 1
			}
		}
		if cuts == nil {
			return text, owners
		}
		text, owners = cutRanges(text, owners, cuts)
	}
}

func cutRanges(text []byte, owners []int, cuts [][]int) ([]byte, []int) {
	keptText := make([]byte, 0, len(text))
	var keptOwners []int
	from := 0
	for _, cut := range append(cuts, []int{len(text), len(text)}) {
		keptText = append(keptText, text[from:cut[0]]...)
		if owners != nil {
			keptOwners = append(keptOwners, owners[from:cut[0]]...)
		}
		from = cut[1]
	}
	return keptText, keptOwners
}

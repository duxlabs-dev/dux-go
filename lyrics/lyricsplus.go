package lyrics

import (
	"context"
	"slices"
	"strings"
	"time"
)

type lyricsPlusResponse struct {
	Type     string           `json:"type"`
	Lyrics   []lyricsPlusLine `json:"lyrics"`
	Metadata struct {
		Agents map[string]struct {
			Name  string `json:"name"`
			Type  string `json:"type"`
			Alias string `json:"alias"`
		} `json:"agents"`
		TotalDuration string `json:"totalDuration"`
	} `json:"metadata"`
}

type lyricsPlusLine struct {
	Time     *int64           `json:"time"`
	Duration int64            `json:"duration"`
	Text     string           `json:"text"`
	Words    []lyricsPlusWord `json:"syllabus"`
	Element  struct {
		Singer string `json:"singer"`
	} `json:"element"`
}

type lyricsPlusWord struct {
	Time       *int64 `json:"time"`
	Duration   int64  `json:"duration"`
	Text       string `json:"text"`
	Background bool   `json:"isBackground"`
}

func (c *Client) fetchLyricsPlus(ctx context.Context, req Request) (*Lyrics, error) {
	query := req.query("title", "artist", "album", "duration")
	var payload lyricsPlusResponse
	if err := c.getJSON(ctx, "https://lyricsplus.binimum.org/v2/lyrics/get", query, 4*time.Second, &payload); err != nil {
		return nil, err
	}
	return decodeLyricsPlus(payload)
}

func decodeLyricsPlus(payload lyricsPlusResponse) (*Lyrics, error) {
	if len(payload.Lyrics) > MaxLines || len(payload.Metadata.Agents) > MaxLines {
		return nil, ErrTooLarge
	}
	timed := payload.Type == "Word" || payload.Type == "Line"
	result := &Lyrics{Voices: payload.voices()}
	plain := make([]string, 0, len(payload.Lyrics))
	wordCount := 0
	for index, source := range payload.Lyrics {
		plain = append(plain, source.Text)
		wordCount += len(source.Words)
		if wordCount > MaxWords {
			return nil, ErrTooLarge
		}
		if !timed || source.Time == nil || *source.Time < 0 {
			continue
		}
		line := Line{Start: milliseconds(*source.Time), Text: source.Text, Voice: boundedVoiceID(source.Element.Singer)}
		if source.Duration > 0 {
			line.End = line.Start + milliseconds(source.Duration)
		}
		if payload.Type == "Word" {
			line.Words = source.words()
		}
		if line.Words == nil {
			result.Synced = append(result.Synced, line)
			continue
		}
		parts := splitBackground(line, source.Words)
		for _, part := range parts {
			if len(parts) > 1 {
				part.Group = index + 1
			}
			result.Synced = append(result.Synced, part)
		}
	}
	if len(result.Synced) > MaxLines {
		return nil, ErrTooLarge
	}
	if len(result.Synced) > 0 && !result.timed() {
		return nil, errUntimedSync
	}
	sortLines(result.Synced)
	result.Plain = strings.Join(plain, "\n")
	if length, err := ttmlSeconds(payload.Metadata.TotalDuration); err == nil {
		result.DurationMS = length.ms()
	}
	return result, nil
}

func (p lyricsPlusResponse) voices() map[VoiceID]Voice {
	voices := make(map[VoiceID]Voice, len(p.Metadata.Agents))
	for name, agent := range p.Metadata.Agents {
		if agent.Alias != "" {
			name = agent.Alias
		}
		if id := boundedVoiceID(name); id != "" {
			voices[id] = voiceMetadata(agent.Name, agent.Type)
		}
	}
	if len(voices) == 0 {
		return nil
	}
	return voices
}

// words is nil unless every syllable is timed inside the line and they spell out its text.
func (l lyricsPlusLine) words() []Word {
	words := make([]Word, 0, len(l.Words))
	for _, source := range l.Words {
		if source.Time == nil || *source.Time < *l.Time {
			return nil
		}
		word := Word{Start: milliseconds(*source.Time), Text: source.Text}
		if source.Duration > 0 {
			word.End = word.Start + milliseconds(source.Duration)
		}
		words = append(words, word)
	}
	if len(words) == 0 || joinWords(words) != l.Text {
		return nil
	}
	return words
}

func splitBackground(line Line, source []lyricsPlusWord) []Line {
	if !slices.ContainsFunc(source, func(word lyricsPlusWord) bool { return word.Background }) {
		return []Line{line}
	}
	var parts []Line
	for _, background := range []bool{false, true} {
		part := Line{Voice: line.Voice, Background: background}
		for i, word := range line.Words {
			if source[i].Background == background {
				part.Words = append(part.Words, word)
			}
		}
		part.Words = trimWords(part.Words)
		if len(part.Words) == 0 {
			continue
		}
		part.Start = part.Words[0].Start
		for _, word := range part.Words {
			part.Start = min(part.Start, word.Start)
			part.End = max(part.End, word.End)
		}
		part.Text = joinWords(part.Words)
		parts = append(parts, part)
	}
	return parts
}

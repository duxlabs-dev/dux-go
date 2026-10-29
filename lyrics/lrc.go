package lyrics

import (
	"bufio"
	"bytes"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	MaxSourceBytes  = 512 << 10
	MaxSidecarBytes = 256 << 10
	MaxLines        = 2000
	MaxWords        = 16000
)

var (
	ErrTooLarge = errors.New("lyrics too large")
	ErrEncoding = errors.New("lyrics are not valid utf-8")

	enhancedTag = regexp.MustCompile(`<\d+:\d+(?::\d+)?(?:[.,]\d+)?>`)
	utf8BOM     = []byte{0xEF, 0xBB, 0xBF}
)

// ParseLRC handles plain, synced, and enhanced (word-synced) LRC.
func ParseLRC(data []byte) (*Lyrics, error) {
	text, err := decode(data)
	if err != nil {
		return nil, err
	}

	var (
		lines         []Line
		plain         []string
		offset        Seconds
		length        Seconds
		expandedBytes int
		expandedWords int
	)

	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 0, 64<<10), MaxSourceBytes)
	for scanner.Scan() {
		raw := strings.TrimRight(scanner.Text(), "\r")
		stamps, rest := splitTimestamps(raw)

		if len(stamps) == 0 {
			if name, value, ok := metaTag(raw); ok {
				switch name {
				case "offset":
					offset = parseOffset(value)
				case "length":
					length, _ = parseStamp(value)
				}
				continue
			}
			if body := strings.TrimSpace(stripEnhanced(raw)); body != "" {
				plain = append(plain, body)
			}
			if len(plain) > MaxLines {
				return nil, ErrTooLarge
			}
			continue
		}

		body, words := splitWords(rest, stamps[0])
		if body != "" {
			plain = append(plain, body)
		}
		expandedBytes += len(body) * len(stamps)
		expandedWords += len(words) * len(stamps)
		if len(lines)+len(stamps) > MaxLines || len(plain) > MaxLines || expandedBytes > MaxSourceBytes || expandedWords > MaxWords {
			return nil, ErrTooLarge
		}
		for _, at := range stamps {
			line := Line{Start: at, Text: body}
			for _, word := range words {
				shifted := Word{Start: at + (word.Start - stamps[0]), Text: word.Text}
				if word.End > word.Start {
					shifted.End = at + (word.End - stamps[0])
				}
				if !shifted.Start.finite() || !shifted.End.finite() {
					return nil, errors.New("lyric timestamp overflow")
				}
				line.Words = append(line.Words, shifted)
			}
			lines = append(lines, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	for i := range lines {
		lines[i].Start = shift(lines[i].Start, offset)
		for j := range lines[i].Words {
			lines[i].Words[j].Start = shift(lines[i].Words[j].Start, offset)
			if lines[i].Words[j].End > 0 {
				lines[i].Words[j].End = shift(lines[i].Words[j].End, offset)
			}
		}
	}
	sortLines(lines)

	return &Lyrics{Synced: lines, Plain: strings.Join(plain, "\n"), DurationMS: length.ms(), Source: SourceLRC}, nil
}

func shift(value, offset Seconds) Seconds {
	shifted := value - offset
	if !shifted.finite() {
		return value
	}
	return max(0, shifted)
}

func decode(data []byte) (string, error) {
	if len(data) > MaxSourceBytes {
		return "", ErrTooLarge
	}
	data = bytes.TrimPrefix(data, utf8BOM)
	if !utf8.Valid(data) {
		return "", ErrEncoding
	}
	return string(data), nil
}

func splitTimestamps(line string) ([]Seconds, string) {
	var stamps []Seconds
	rest := line

	for {
		trimmed := strings.TrimLeft(rest, " \t")
		if !strings.HasPrefix(trimmed, "[") {
			break
		}
		end := strings.Index(trimmed, "]")
		if end < 0 {
			break
		}
		at, ok := parseStamp(trimmed[1:end])
		if !ok {
			break
		}
		stamps = append(stamps, at)
		rest = trimmed[end+1:]
	}

	if len(stamps) == 0 {
		return nil, line
	}
	return stamps, rest
}

func parseStamp(s string) (Seconds, bool) {
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}

	total := Seconds(0)
	for i, part := range parts {
		if i == len(parts)-1 {
			part = strings.Replace(part, ",", ".", 1)
		} else if strings.ContainsAny(part, ".,") {
			return 0, false
		}
		value, err := strconv.ParseFloat(part, 64)
		if err != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, false
		}
		total = total*60 + Seconds(value)
	}
	return total, total.finite()
}

func metaTag(line string) (string, string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") || !strings.HasSuffix(trimmed, "]") {
		return "", "", false
	}
	name, value, found := strings.Cut(trimmed[1:len(trimmed)-1], ":")
	if !found {
		return "", "", false
	}
	return strings.ToLower(strings.TrimSpace(name)), strings.TrimSpace(value), true
}

func parseOffset(value string) Seconds {
	ms, err := strconv.ParseFloat(strings.TrimPrefix(strings.TrimSpace(value), "+"), 64)
	if err != nil || math.IsNaN(ms) || math.IsInf(ms, 0) {
		return 0
	}
	return Seconds(ms / 1000)
}

func stripEnhanced(s string) string {
	if !strings.Contains(s, "<") {
		return s
	}
	return enhancedTag.ReplaceAllString(s, "")
}

func splitWords(text string, start Seconds) (string, []Word) {
	tags := enhancedTag.FindAllStringIndex(text, -1)
	if len(tags) == 0 {
		return strings.TrimSpace(text), nil
	}
	body := strings.TrimSpace(stripEnhanced(text))
	var words []Word
	previous := 0
	at := start
	for _, tag := range tags {
		next, valid := parseStamp(text[tag[0]+1 : tag[1]-1])
		if !valid || next < at {
			return body, nil
		}
		if tag[0] > previous {
			words = append(words, Word{Start: at, End: next, Text: text[previous:tag[0]]})
		}
		at = next
		previous = tag[1]
	}
	if previous < len(text) {
		words = append(words, Word{Start: at, Text: text[previous:]})
	}
	return body, trimWords(words)
}

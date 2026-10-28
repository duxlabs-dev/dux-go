package lyrics

import (
	"encoding/xml"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode"
)

const ttmlMaxDepth = 32

var errTTML = errors.New("invalid timed lyrics document")

// ttmlScope is what an element hands down to its children.
type ttmlScope struct {
	start      Seconds
	end        Seconds
	timed      bool
	wordTimed  bool
	skipped    bool
	background bool
	agentName  bool
	voice      VoiceID
}

// ttmlLine is one voice within a <p>. A lead and its x-bg backing vocal are two.
type ttmlLine struct {
	start      Seconds
	end        Seconds
	voice      VoiceID
	background bool
	wordTimed  bool
	words      []*ttmlWord
}

type ttmlWord struct {
	start Seconds
	end   Seconds
	text  strings.Builder
}

type ttmlParser struct {
	scopes      []ttmlScope
	rootSeen    bool
	inBody      bool
	length      Seconds
	inParagraph bool
	parts       []*ttmlLine
	paragraphs  int
	words       int

	agent     VoiceID
	agentName strings.Builder

	plain  []string
	synced []Line
	voices map[VoiceID]Voice
}

// ParseTTML understands ttm:agent voices and the x-bg role for backing vocals.
func ParseTTML(data []byte) (*Lyrics, error) {
	text, err := decode(data)
	if err != nil {
		return nil, err
	}
	decoder := xml.NewDecoder(strings.NewReader(text))
	parser := ttmlParser{voices: map[VoiceID]Voice{}}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch token := token.(type) {
		case xml.StartElement:
			err = parser.open(token)
		case xml.CharData:
			err = parser.text(string(token))
		case xml.EndElement:
			err = parser.close(token.Name.Local)
		case xml.Directive:
			err = errTTML
		}
		if err != nil {
			return nil, err
		}
	}
	if !parser.rootSeen {
		return nil, errTTML
	}

	sortLines(parser.synced)
	result := &Lyrics{Synced: parser.synced, Plain: strings.Join(parser.plain, "\n"), DurationMS: parser.length.ms(), Source: SourceTTML}
	if len(parser.voices) > 0 {
		result.Voices = parser.voices
	}
	return result, nil
}

func (p *ttmlParser) open(element xml.StartElement) error {
	if len(p.scopes) >= ttmlMaxDepth {
		return ErrTooLarge
	}
	name := element.Name.Local
	scope := ttmlScope{}
	switch {
	case len(p.scopes) > 0:
		scope = p.scopes[len(p.scopes)-1]
	case p.rootSeen || name != "tt":
		return errTTML
	}
	p.rootSeen = true

	var id VoiceID
	var kind string
	var duration Seconds
	for _, attr := range element.Attr {
		switch attr.Name.Local {
		case "begin", "end", "dur":
			at, err := ttmlSeconds(attr.Value)
			if err != nil {
				return err
			}
			switch attr.Name.Local {
			case "begin":
				scope.start = at
				scope.timed = true
				scope.wordTimed = name == "span"
			case "end":
				scope.end = at
			case "dur":
				duration = at
			}
		case "role":
			switch attr.Value {
			case "x-translation", "x-transliteration":
				scope.skipped = true
			case "x-bg":
				scope.background = true
			}
		case "agent":
			scope.voice = boundedVoiceID(attr.Value)
		case "id":
			id = boundedVoiceID(attr.Value)
		case "type":
			kind = attr.Value
		}
	}
	if duration > 0 {
		scope.end = scope.start + duration
	}
	if !scope.end.finite() {
		return errTTML
	}

	if !p.inBody {
		switch {
		case name == "agent" && id != "":
			if len(p.voices) >= MaxLines {
				return ErrTooLarge
			}
			p.agent = id
			p.voices[id] = voiceMetadata("", kind)
		case name == "name" && p.agent != "" && (kind == "full" || p.voices[p.agent].Name == ""):
			scope.agentName = true
			p.agentName.Reset()
		}
	}

	p.scopes = append(p.scopes, scope)
	if name == "body" {
		p.inBody = true
		p.length = scope.end
	}
	if !p.inBody || scope.skipped {
		return nil
	}
	switch name {
	case "p":
		if p.inParagraph {
			return errTTML
		}
		if p.paragraphs >= MaxLines {
			return ErrTooLarge
		}
		p.paragraphs++
		p.inParagraph = true
	case "br":
		return p.text("\n")
	}
	return nil
}

func (p *ttmlParser) text(text string) error {
	if len(p.scopes) == 0 || text == "" {
		return nil
	}
	scope := p.scopes[len(p.scopes)-1]
	if scope.agentName {
		p.agentName.WriteString(text)
	}
	if !p.inParagraph || scope.skipped {
		return nil
	}
	if text != "\n" {
		text = strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return ' '
			}
			return r
		}, text)
	}
	blank := strings.TrimSpace(text) == ""

	line := p.lineFor(scope)
	if line == nil {
		if blank {
			return nil
		}
		if len(p.parts) >= MaxLines {
			return ErrTooLarge
		}
		line = &ttmlLine{start: -1, voice: scope.voice, background: scope.background}
		p.parts = append(p.parts, line)
	}
	line.wordTimed = line.wordTimed || scope.wordTimed

	at := Seconds(-1)
	if scope.timed {
		at = scope.start
	}
	if scope.timed && !blank {
		if line.start < 0 || at < line.start {
			line.start = at
		}
		if scope.end > at {
			line.end = max(line.end, scope.end)
		}
	}

	if last := len(line.words) - 1; last >= 0 && (!scope.wordTimed || line.words[last].start == at) {
		line.words[last].text.WriteString(text)
		return nil
	}
	p.words++
	if p.words > MaxWords {
		return ErrTooLarge
	}
	word := &ttmlWord{start: at, end: scope.end}
	word.text.WriteString(text)
	line.words = append(line.words, word)
	return nil
}

func (p *ttmlParser) lineFor(scope ttmlScope) *ttmlLine {
	for _, line := range p.parts {
		if line.voice == scope.voice && line.background == scope.background {
			return line
		}
	}
	return nil
}

func (p *ttmlParser) close(name string) error {
	scope := p.scopes[len(p.scopes)-1]
	p.scopes = p.scopes[:len(p.scopes)-1]
	switch name {
	case "name":
		if scope.agentName && p.agent != "" {
			p.voices[p.agent] = voiceMetadata(strings.TrimSpace(p.agentName.String()), string(p.voices[p.agent].Type))
		}
	case "agent":
		p.agent = ""
	case "body":
		p.inBody = false
	case "p":
		if p.inParagraph && !scope.skipped {
			return p.closeParagraph()
		}
	}
	return nil
}

func (p *ttmlParser) closeParagraph() error {
	for _, part := range p.parts {
		line := part.finish()
		if line.Text == "" {
			continue
		}
		p.plain = append(p.plain, line.Text)
		if len(p.plain) > MaxLines {
			return ErrTooLarge
		}
		if len(p.parts) > 1 {
			line.Group = p.paragraphs
		}
		if line.Start >= 0 {
			p.synced = append(p.synced, line)
		}
	}
	p.parts = nil
	p.inParagraph = false
	return nil
}

func (l *ttmlLine) finish() Line {
	line := Line{Start: l.start, End: l.end, Voice: l.voice, Background: l.background}
	words := make([]Word, 0, len(l.words))
	for _, source := range l.words {
		word := Word{Start: max(source.start, l.start), Text: source.text.String()}
		if source.end > word.Start {
			word.End = source.end
		}
		words = append(words, word)
	}
	words = trimWords(words)
	line.Text = joinWords(words)
	if l.wordTimed {
		line.Words = words
	}
	return line
}

func ttmlSeconds(value string) (Seconds, error) {
	scale := 1.0
	for _, unit := range []struct {
		suffix string
		scale  float64
	}{{"ms", 0.001}, {"s", 1}, {"m", 60}, {"h", 3600}} {
		if strings.HasSuffix(value, unit.suffix) {
			value = strings.TrimSuffix(value, unit.suffix)
			scale = unit.scale
			break
		}
	}
	parts := strings.Split(value, ":")
	if len(parts) > 3 {
		return 0, errTTML
	}
	seconds := 0.0
	for _, part := range parts {
		number, err := strconv.ParseFloat(part, 64)
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
			return 0, errTTML
		}
		seconds = seconds*60 + number
	}
	seconds *= scale
	if math.IsInf(seconds, 0) {
		return 0, errTTML
	}
	return Seconds(seconds), nil
}

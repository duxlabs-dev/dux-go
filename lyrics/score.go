package lyrics

import (
	"math"
	"slices"
	"sort"
	"strings"
)

const (
	endSlack      Seconds = 3
	minAgreeLines         = 3
	// Catalogs round lengths to whole seconds, so short tracks need the floor.
	lengthShare            = 0.01
	minLengthSlack Seconds = 2
	// Gaps within one band count as agreement, so provider order decides between them.
	agreeBandShare         = 0.001
	minAgreeBand   Seconds = 0.25
	// Bounds the LCS table, which is quadratic in line count, against oversized provider responses.
	maxAlignCells = 250_000
)

type candidate struct {
	priority  int
	result    *Result
	cues      []Line
	plausible bool
	verified  bool
	timed     bool
	nearest   Seconds
}

// pick prefers lyrics whose reported length matches the track, then the ones whose
// timing sits closest to another provider's. Provider order decides among those that agree.
func pick(req Request, responses []providerResponse) *Result {
	candidates := make([]*candidate, 0, len(responses))
	for i, response := range responses {
		if response.result == nil || !response.result.Found {
			continue
		}
		lyrics := &response.result.Lyrics
		lines := cues(lyrics)
		candidates = append(candidates, &candidate{
			priority:  i,
			result:    response.result,
			cues:      lines,
			plausible: plausible(req, lyrics),
			verified:  req.seconds() > 0 && lyrics.DurationMS > 0,
			timed:     len(lines) > 0,
			nearest:   Seconds(math.Inf(1)),
		})
	}
	if len(candidates) == 0 {
		return nil
	}
	for i, a := range candidates {
		for _, b := range candidates[i+1:] {
			if !a.plausible || !b.plausible {
				continue
			}
			gap, ok := medianGap(a.cues, b.cues)
			if !ok {
				continue
			}
			a.nearest = min(a.nearest, gap)
			b.nearest = min(b.nearest, gap)
		}
	}
	band := max(minAgreeBand, Seconds(req.seconds())*agreeBandShare)
	best := slices.MinFunc(candidates, func(a, b *candidate) int {
		switch {
		case a.plausible != b.plausible:
			return boolOrder(a.plausible)
		case a.verified != b.verified:
			return boolOrder(a.verified)
		case a.timed != b.timed:
			return boolOrder(a.timed)
		}
		if bandA, bandB := math.Floor(float64(a.nearest/band)), math.Floor(float64(b.nearest/band)); bandA != bandB {
			return boolOrder(bandA < bandB)
		}
		return a.priority - b.priority
	})
	return best.result
}

func boolOrder(first bool) int {
	if first {
		return -1
	}
	return 1
}

func cues(lyrics *Lyrics) []Line {
	var lines []Line
	for _, line := range lyrics.Synced {
		if line.Background || strings.TrimSpace(line.Text) == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// plausible rejects lyrics timed for another cut, such as a music video with intro footage.
func plausible(req Request, lyrics *Lyrics) bool {
	want := Seconds(req.seconds())
	if want == 0 {
		return true
	}
	if reported := milliseconds(lyrics.DurationMS); lyrics.DurationMS > 0 && max(reported-want, want-reported) > max(minLengthSlack, want*lengthShare) {
		return false
	}
	lines := cues(lyrics)
	return len(lines) == 0 || lines[len(lines)-1].Start <= want+endSlack
}

// medianGap aligns lines by text and compares their starts. A constant offset or
// a drift both raise the median.
func medianGap(a, b []Line) (Seconds, bool) {
	gaps := alignedGaps(a, b)
	if len(gaps) < minAgreeLines {
		return 0, false
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i] < gaps[j] })
	return gaps[len(gaps)/2], true
}

func alignedGaps(a, b []Line) []Seconds {
	if len(a)*len(b) > maxAlignCells {
		return nil
	}
	textA, textB := lineTexts(a), lineTexts(b)
	// Longest common subsequence keeps repeated choruses paired in order.
	table := make([][]int, len(a)+1)
	for i := range table {
		table[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			switch {
			case textA[i] != "" && textA[i] == textB[j]:
				table[i][j] = table[i+1][j+1] + 1
			case table[i+1][j] >= table[i][j+1]:
				table[i][j] = table[i+1][j]
			default:
				table[i][j] = table[i][j+1]
			}
		}
	}
	var gaps []Seconds
	for i, j := 0, 0; i < len(a) && j < len(b); {
		switch {
		case textA[i] != "" && textA[i] == textB[j]:
			gaps = append(gaps, max(a[i].Start-b[j].Start, b[j].Start-a[i].Start))
			i++
			j++
		case table[i+1][j] >= table[i][j+1]:
			i++
		default:
			j++
		}
	}
	return gaps
}

func lineTexts(lines []Line) []string {
	texts := make([]string, len(lines))
	for i, line := range lines {
		texts[i] = searchable(line.Text)
	}
	return texts
}

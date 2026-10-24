package desktop

import (
	"bufio"
	"bytes"
	"os"
	"strings"
)

type group struct {
	keys map[string]string
}

func parseGroups(data []byte) map[string]*group {
	groups := make(map[string]*group)
	var current *group

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' {
			continue
		}

		if line[0] == '[' && strings.HasSuffix(line, "]") {
			name := line[1 : len(line)-1]
			g, ok := groups[name]
			if !ok {
				g = &group{keys: make(map[string]string)}
				groups[name] = g
			}
			current = g
			continue
		}

		if current == nil {
			continue
		}

		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}

		key := strings.TrimSpace(line[:eq])
		if _, ok := current.keys[key]; ok {
			continue
		}
		current.keys[key] = strings.TrimSpace(line[eq+1:])
	}

	return groups
}

// Spec lookup order: lang_COUNTRY@MODIFIER, lang_COUNTRY, lang@MODIFIER, lang, then the plain key.
func (g *group) localized(key string) string {
	for _, suffix := range localeSuffixes() {
		if v, ok := g.keys[key+"["+suffix+"]"]; ok {
			return v
		}
	}
	return g.keys[key]
}

func localeSuffixes() []string {
	locale := ""
	for _, env := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(env); v != "" {
			locale = v
			break
		}
	}
	if locale == "" || locale == "C" || locale == "POSIX" {
		return nil
	}
	locale, modifier, _ := strings.Cut(locale, "@")
	locale, _, _ = strings.Cut(locale, ".")
	lang, country, _ := strings.Cut(locale, "_")
	if lang == "" {
		return nil
	}

	var out []string
	if country != "" && modifier != "" {
		out = append(out, lang+"_"+country+"@"+modifier)
	}
	if country != "" {
		out = append(out, lang+"_"+country)
	}
	if modifier != "" {
		out = append(out, lang+"@"+modifier)
	}
	return append(out, lang)
}

func splitList(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func parseBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes":
		return true
	}
	return false
}

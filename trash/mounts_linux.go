package trash

import (
	"os"
	"strings"
)

// readMountPoints returns user-visible mount points from /proc/self/mountinfo,
// skipping pseudo and system filesystems.
func readMountPoints() []string {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil
	}
	return parseMountInfo(string(data))
}

func parseMountInfo(data string) []string {
	var out []string
	seen := map[string]bool{}
	for line := range strings.SplitSeq(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		mp := unescapeMountPath(fields[4])
		if skipMountPoint(mp, seen) || skipFsType(mountInfoFsType(fields)) {
			continue
		}
		seen[mp] = true
		out = append(out, mp)
	}
	return out
}

func mountInfoFsType(fields []string) string {
	for i := 6; i+1 < len(fields); i++ {
		if fields[i] == "-" {
			return fields[i+1]
		}
	}
	return ""
}

func unescapeMountPath(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '\\' && i+3 < len(s) &&
			isOctal(s[i+1]) && isOctal(s[i+2]) && isOctal(s[i+3]) {
			val := (s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0')
			b.WriteByte(val)
			i += 4
		} else {
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

func isOctal(c byte) bool {
	return c >= '0' && c <= '7'
}

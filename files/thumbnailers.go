package files

import (
	"cmp"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/duxlabs-dev/dux-go/paths"
)

const thumbnailerGroup = "[Thumbnailer Entry]"

type thumbnailer struct {
	argv          []string
	outputPrefix  bool // the tool appends .png to %o itself
	userInstalled bool
}

type thumbJob struct {
	input  string
	output string
	size   int
	mime   string
}

type thumbnailers struct {
	byMime map[string]thumbnailer
}

func loadThumbnailers() *thumbnailers {
	set := &thumbnailers{byMime: map[string]thumbnailer{}}
	set.loadDirs()
	set.addBuiltins()
	return set
}

func (t *thumbnailers) loadDirs() {
	t.loadDir(filepath.Join(paths.XDGDataHome(), "thumbnailers"), true)
	for _, dir := range systemThumbnailerDirs() {
		t.loadDir(dir, false)
	}
}

func systemThumbnailerDirs() []string {
	var dirs []string
	for dir := range strings.SplitSeq(cmp.Or(os.Getenv("XDG_DATA_DIRS"), "/usr/local/share:/usr/share"), ":") {
		if dir = strings.TrimSpace(dir); dir != "" {
			dirs = append(dirs, filepath.Join(dir, "thumbnailers"))
		}
	}
	return dirs
}

func (t *thumbnailers) loadDir(dir string, userInstalled bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".thumbnailer") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		t.addEntry(parseThumbnailerEntry(data), userInstalled)
	}
}

func (t *thumbnailers) addEntry(keys map[string]string, userInstalled bool) {
	if tryExec := keys["TryExec"]; tryExec != "" && resolveExecutable(tryExec) == "" {
		return
	}
	argv, err := splitShellWords(keys["Exec"])
	if err != nil || len(argv) == 0 {
		return
	}
	argv[0] = resolveExecutable(argv[0])
	if argv[0] == "" {
		return
	}
	for mime := range strings.SplitSeq(keys["MimeType"], ";") {
		if mime = strings.TrimSpace(mime); mime != "" {
			t.add(mime, thumbnailer{argv: argv, userInstalled: userInstalled})
		}
	}
}

func (t *thumbnailers) add(mime string, tool thumbnailer) {
	if _, taken := t.byMime[mime]; taken {
		return
	}
	t.byMime[mime] = tool
}

func (t *thumbnailers) addBuiltins() {
	if video := resolveExecutable("ffmpegthumbnailer"); video != "" {
		t.add("video/*", thumbnailer{argv: []string{video, "-i", "%i", "-o", "%o", "-s", "%s"}})
	}
	if pdf := resolveExecutable("pdftoppm"); pdf != "" {
		t.add("application/pdf", thumbnailer{argv: []string{pdf, "-png", "-f", "1", "-l", "1", "-singlefile", "-scale-to", "%s", "%i", "%o"}, outputPrefix: true})
	}
}

func (t *thumbnailers) lookup(mime string) (thumbnailer, bool) {
	if tool, ok := t.byMime[mime]; ok {
		return tool, true
	}
	tool, ok := t.byMime[mediaType(mime)+"/*"]
	return tool, ok
}

func (t *thumbnailers) mediaKinds() []string {
	kinds := map[string]bool{}
	for mime := range t.byMime {
		switch {
		case mediaType(mime) == "video":
			kinds["video"] = true
		case mime == "application/pdf":
			kinds["pdf"] = true
		case mediaType(mime) == "application" || mediaType(mime) == "text":
			kinds["document"] = true
		}
	}
	out := make([]string, 0, len(kinds))
	for kind := range kinds {
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}

func (tool thumbnailer) command(job thumbJob) []string {
	output := job.output
	if tool.outputPrefix {
		output = strings.TrimSuffix(output, ".png")
	}
	argv := make([]string, len(tool.argv))
	for i, arg := range tool.argv {
		argv[i] = expandThumbnailerArg(arg, job, output)
	}
	return argv
}

func expandThumbnailerArg(arg string, job thumbJob, output string) string {
	var out strings.Builder
	for i := 0; i < len(arg); i++ {
		if arg[i] != '%' || i+1 == len(arg) {
			out.WriteByte(arg[i])
			continue
		}
		i++
		switch arg[i] {
		case 'i':
			out.WriteString(job.input)
		case 'u':
			out.WriteString(fileURI(job.input))
		case 'o':
			out.WriteString(output)
		case 's':
			out.WriteString(strconv.Itoa(job.size))
		case 'm':
			out.WriteString(job.mime)
		case '%':
			out.WriteByte('%')
		}
	}
	return out.String()
}

func parseThumbnailerEntry(data []byte) map[string]string {
	keys := map[string]string{}
	inGroup := false
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || line[0] == '#':
			continue
		case line[0] == '[':
			inGroup = line == thumbnailerGroup
			continue
		case !inGroup:
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if _, seen := keys[key]; !seen {
			keys[key] = strings.TrimSpace(value)
		}
	}
	return keys
}

func resolveExecutable(name string) string {
	if !filepath.IsAbs(name) {
		path, err := exec.LookPath(name)
		if err != nil {
			return ""
		}
		return path
	}
	info, err := os.Stat(name)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return ""
	}
	return name
}

func splitShellWords(line string) ([]string, error) {
	var words []string
	var word strings.Builder
	inWord := false
	quote := byte(0)
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
				continue
			}
			word.WriteByte(c)
		case quote == '"':
			switch {
			case c == '"':
				quote = 0
			case c == '\\' && i+1 < len(line) && strings.IndexByte("\"\\$`\n", line[i+1]) >= 0:
				i++
				word.WriteByte(line[i])
			default:
				word.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote = c
			inWord = true
		case c == '\\':
			if i+1 == len(line) {
				return nil, errors.New("trailing backslash")
			}
			i++
			word.WriteByte(line[i])
			inWord = true
		case c == ' ' || c == '\t':
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		default:
			word.WriteByte(c)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote")
	}
	if inWord {
		words = append(words, word.String())
	}
	return words, nil
}

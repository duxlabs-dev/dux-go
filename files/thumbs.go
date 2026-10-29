package files

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	xdraw "golang.org/x/image/draw"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

const (
	imageSizeCap  = 64 << 20
	pixelCap      = 100 << 20
	externalLimit = 20 * time.Second
	stderrTail    = 512
	failDir       = "fail/dankfiles"
)

var thumbSizes = map[string]int{
	"normal":   128,
	"large":    256,
	"x-large":  512,
	"xx-large": 1024,
}

// must match the blank decoder imports above
var nativeImages = map[string]bool{
	"image/png":      true,
	"image/jpeg":     true,
	"image/gif":      true,
	"image/bmp":      true,
	"image/x-ms-bmp": true,
	"image/tiff":     true,
	"image/webp":     true,
}

func ParseThumbSize(raw string) string {
	name := strings.ToLower(strings.TrimSpace(raw))
	if _, ok := thumbSizes[name]; ok {
		return name
	}
	return "normal"
}

type Thumbs struct {
	root      string
	cacheHome string
	tools     *thumbnailers
	bwrap     string
	limit     time.Duration
}

func NewThumbs(cacheHome string) *Thumbs {
	return &Thumbs{
		root:      filepath.Join(cacheHome, "thumbnails"),
		cacheHome: cacheHome,
		tools:     loadThumbnailers(),
		bwrap:     detectBwrap(),
		limit:     externalLimit,
	}
}

func (t *Thumbs) Capabilities() []string {
	caps := []string{"thumbnail.image"}
	for _, kind := range t.tools.mediaKinds() {
		caps = append(caps, "thumbnail."+kind)
	}
	if t.bwrap != "" {
		caps = append(caps, "thumbnail.sandboxed")
	}
	return caps
}

func (t *Thumbs) Supports(mimeType string) bool {
	switch {
	case mimeType == "image/svg+xml" || mimeType == "image/svg":
		return false
	case nativeImages[mimeType]:
		return true
	default:
		_, ok := t.tools.lookup(mimeType)
		return ok
	}
}

func (t *Thumbs) Lookup(path string, mtimeMs int64, size string) (string, bool) {
	cached := t.cachePath(path, size)
	if t.valid(cached, path, mtimeMs) {
		return cached, true
	}
	return "", false
}

func (t *Thumbs) Failed(path string, mtimeMs int64) bool {
	return t.valid(t.failPath(path), path, mtimeMs)
}

func (t *Thumbs) Generate(e Entry, size string) (string, error) {
	if !t.Supports(e.Mime) {
		return "", errors.New("no thumbnailer for " + e.Mime)
	}
	if cached, ok := t.Lookup(e.Path, e.MtimeMs, size); ok {
		return cached, nil
	}

	img, err := t.render(e, thumbSizes[size])
	if err != nil {
		t.markFailed(e)
		return "", err
	}

	dst := t.cachePath(e.Path, size)
	if err := writePNGWithText(dst, img, t.metadata(e)); err != nil {
		return "", err
	}
	return dst, nil
}

func (t *Thumbs) render(e Entry, bound int) (image.Image, error) {
	tool, ok := t.tools.lookup(e.Mime)
	switch {
	case ok && (tool.userInstalled || !nativeImages[e.Mime]):
		return t.external(e, tool, bound)
	case !nativeImages[e.Mime]:
		return nil, errors.New("no thumbnailer for " + e.Mime)
	case e.Size > imageSizeCap:
		return nil, errors.New("image over the thumbnail size cap")
	default:
		return decodeScaled(e.Path, bound)
	}
}

func (t *Thumbs) external(e Entry, tool thumbnailer, bound int) (image.Image, error) {
	tmp, err := os.MkdirTemp("", "dfiles-thumb-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	hostOutput := filepath.Join(tmp, filepath.Base(sandboxOutput))
	argv := t.argv(e, tool, tmp, hostOutput, bound)

	ctx, cancel := context.WithTimeout(context.Background(), t.limit)
	defer cancel()

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stderr = &stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", filepath.Base(tool.argv[0]), err, tail(stderr.Bytes()))
	}
	return decodeScaled(hostOutput, bound)
}

func (t *Thumbs) argv(e Entry, tool thumbnailer, outDir, hostOutput string, bound int) []string {
	job := thumbJob{input: e.Path, output: hostOutput, size: bound, mime: e.Mime}
	if t.bwrap == "" {
		return tool.command(job)
	}
	job.input = sandboxInputPath(e.Path)
	job.output = sandboxOutput
	return append(bwrapArgs(t.bwrap, e.Path, outDir, t.cacheHome, filepath.Dir(tool.argv[0])), tool.command(job)...)
}

func tail(out []byte) string {
	if len(out) > stderrTail {
		out = out[len(out)-stderrTail:]
	}
	return strings.TrimSpace(string(out))
}

func decodeScaled(path string, bound int) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	config, _, err := image.DecodeConfig(f)
	if err != nil {
		return nil, err
	}
	if config.Width*config.Height > pixelCap {
		return nil, errors.New("image over the pixel cap")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	src, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	return scaleToBound(src, bound), nil
}

func scaleToBound(src image.Image, bound int) image.Image {
	size := src.Bounds().Size()
	if size.X <= bound && size.Y <= bound {
		return src
	}
	width, height := size.X, size.Y
	switch {
	case width >= height:
		height = max(1, height*bound/width)
		width = bound
	default:
		width = max(1, width*bound/height)
		height = bound
	}
	dst := image.NewNRGBA(image.Rect(0, 0, width, height))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	return dst
}

func (t *Thumbs) metadata(e Entry) map[string]string {
	return map[string]string{
		"Thumb::URI":      fileURI(e.Path),
		"Thumb::MTime":    strconv.FormatInt(e.MtimeMs/1000, 10),
		"Thumb::Size":     strconv.FormatInt(max(e.Size, 0), 10),
		"Thumb::Mimetype": e.Mime,
	}
}

func (t *Thumbs) markFailed(e Entry) {
	marker := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	_ = writePNGWithText(t.failPath(e.Path), marker, t.metadata(e))
}

func (t *Thumbs) valid(cached, path string, mtimeMs int64) bool {
	text, err := readPNGText(cached)
	if err != nil {
		return false
	}
	if text["Thumb::URI"] != fileURI(path) {
		return false
	}
	return text["Thumb::MTime"] == strconv.FormatInt(mtimeMs/1000, 10)
}

func (t *Thumbs) cachePath(path, size string) string {
	return filepath.Join(t.root, size, uriDigest(path)+".png")
}

func (t *Thumbs) failPath(path string) string {
	return filepath.Join(t.root, failDir, uriDigest(path)+".png")
}

func fileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: path}).String()
}

func uriDigest(path string) string {
	sum := md5.Sum([]byte(fileURI(path)))
	return hex.EncodeToString(sum[:])
}

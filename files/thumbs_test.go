package files

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/duxlabs-dev/dux-go/ipc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeTestPNG(t *testing.T, path string, width, height int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := range width {
		for y := range height {
			img.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 40, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
}

func TestThumbnailWriteAndRead(t *testing.T) {
	cache := t.TempDir()
	svc := NewService(&fakeBus{}, cache, nil)
	t.Cleanup(svc.Close)

	source := filepath.Join(t.TempDir(), "photo.png")
	writeTestPNG(t, source, 400, 200)
	entry, err := svc.Stat(source)
	require.NoError(t, err)
	require.True(t, entry.Thumbnailable)

	dst, err := svc.thumbs.Generate(entry, "normal")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(cache, "thumbnails", "normal", uriDigest(source)+".png"), dst)

	info, err := os.Stat(dst)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	dirInfo, err := os.Stat(filepath.Dir(dst))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())

	text, err := readPNGText(dst)
	require.NoError(t, err)
	assert.Equal(t, fileURI(source), text["Thumb::URI"])
	assert.Equal(t, strconv.FormatInt(entry.MtimeMs/1000, 10), text["Thumb::MTime"])
	assert.Equal(t, strconv.FormatInt(entry.Size, 10), text["Thumb::Size"])
	assert.Equal(t, "image/png", text["Thumb::Mimetype"])

	file, err := os.Open(dst)
	require.NoError(t, err)
	defer file.Close()
	config, err := png.DecodeConfig(file)
	require.NoError(t, err)
	assert.Equal(t, 128, config.Width)
	assert.Equal(t, 64, config.Height)

	cached, ok := svc.thumbs.Lookup(source, entry.MtimeMs, "normal")
	assert.True(t, ok)
	assert.Equal(t, dst, cached)

	_, stale := svc.thumbs.Lookup(source, entry.MtimeMs+5000, "normal")
	assert.False(t, stale, "a changed mtime invalidates the cached thumbnail")
}

func TestThumbnailSmallerThanBoundIsNotUpscaled(t *testing.T) {
	svc := NewService(&fakeBus{}, t.TempDir(), nil)
	t.Cleanup(svc.Close)

	source := filepath.Join(t.TempDir(), "tiny.png")
	writeTestPNG(t, source, 20, 10)
	entry, err := svc.Stat(source)
	require.NoError(t, err)

	dst, err := svc.thumbs.Generate(entry, "large")
	require.NoError(t, err)

	file, err := os.Open(dst)
	require.NoError(t, err)
	defer file.Close()
	config, err := png.DecodeConfig(file)
	require.NoError(t, err)
	assert.Equal(t, 20, config.Width)
}

func TestThumbnailFailureLeavesMarker(t *testing.T) {
	svc := NewService(&fakeBus{}, t.TempDir(), nil)
	t.Cleanup(svc.Close)

	source := filepath.Join(t.TempDir(), "broken.png")
	require.NoError(t, os.WriteFile(source, []byte("not a png"), 0o644))
	entry, err := svc.Stat(source)
	require.NoError(t, err)
	entry.Mime = "image/png"

	_, err = svc.thumbs.Generate(entry, "normal")
	require.Error(t, err)
	assert.True(t, svc.thumbs.Failed(source, entry.MtimeMs))

	marker := filepath.Join(svc.thumbs.root, failDir, uriDigest(source)+".png")
	text, err := readPNGText(marker)
	require.NoError(t, err)
	assert.Equal(t, fileURI(source), text["Thumb::URI"])
}

func TestThumbnailRejectsOversizeImages(t *testing.T) {
	svc := NewService(&fakeBus{}, t.TempDir(), nil)
	t.Cleanup(svc.Close)

	source := filepath.Join(t.TempDir(), "huge.png")
	writeTestPNG(t, source, 8, 8)
	entry, err := svc.Stat(source)
	require.NoError(t, err)
	entry.Size = imageSizeCap + 1

	_, err = svc.thumbs.Generate(entry, "normal")
	require.Error(t, err)
}

func TestThumbnailSupportFollowsHostTools(t *testing.T) {
	svc := NewService(&fakeBus{}, t.TempDir(), nil)
	t.Cleanup(svc.Close)

	assert.True(t, svc.thumbs.Supports("image/jpeg"))
	assert.False(t, svc.thumbs.Supports("text/plain"))
	assert.False(t, svc.thumbs.Supports("image/svg+xml"), "Qt renders svg on the UI side")
	_, video := svc.thumbs.tools.lookup("video/mp4")
	assert.Equal(t, video, svc.thumbs.Supports("video/mp4"))
	_, pdf := svc.thumbs.tools.lookup("application/pdf")
	assert.Equal(t, pdf, svc.thumbs.Supports("application/pdf"))
	assert.Contains(t, svc.Capabilities(), "thumbnail.image")
}

func TestSnapshotMarksThumbnailableFromExtension(t *testing.T) {
	svc := NewService(&fakeBus{}, t.TempDir(), nil)
	t.Cleanup(svc.Close)

	root := t.TempDir()
	writeTestPNG(t, filepath.Join(root, "photo.png"), 8, 8)
	require.NoError(t, os.WriteFile(filepath.Join(root, "note.txt"), []byte("hi"), 0o644))

	page, err := svc.list(root, ListOptions{Sort: nameSort()})
	require.NoError(t, err)
	assert.True(t, byName(t, page.Entries, "photo.png").Thumbnailable)
	assert.False(t, byName(t, page.Entries, "note.txt").Thumbnailable)
}

func TestHandleThumbnailServesCacheAndStreamsLateResults(t *testing.T) {
	bus := &fakeBus{}
	svc := NewService(bus, t.TempDir(), nil)
	t.Cleanup(svc.Close)

	root := t.TempDir()
	source := filepath.Join(root, "photo.png")
	writeTestPNG(t, source, 64, 64)
	text := filepath.Join(root, "note.txt")
	require.NoError(t, os.WriteFile(text, []byte("hi"), 0o644))

	w, err := svc.Watch(context.Background(), root, nameSort(), false)
	require.NoError(t, err)
	svc.Attach([]string{w.topic})

	result := resultOf(t, call(t, svc, ipc.Request{ID: 1, Method: "files.thumbnail", Params: map[string]any{
		"paths":   []any{source, text},
		"watchId": w.id,
	}}))
	assert.Equal(t, "normal", result["size"])

	results := result["results"].([]any)
	require.Len(t, results, 2)
	assert.Equal(t, true, results[0].(map[string]any)["pending"])
	assert.Equal(t, true, results[1].(map[string]any)["failed"], "a text file has no thumbnailer")

	event := bus.await(t, func(b batch) bool { return b.Kind == "thumbnails" })
	require.Len(t, event.Thumbnails, 1)
	assert.Equal(t, "photo.png", event.Thumbnails[0].Name)
	assert.NotEmpty(t, event.Thumbnails[0].Thumbnail)

	again := resultOf(t, call(t, svc, ipc.Request{ID: 2, Method: "files.thumbnail", Params: map[string]any{"paths": []any{source}, "watchId": w.id}}))
	assert.NotEmpty(t, again["results"].([]any)[0].(map[string]any)["thumbnail"])
}

func TestHandleThumbnailWithoutWatchGeneratesInline(t *testing.T) {
	svc := NewService(&fakeBus{}, t.TempDir(), nil)
	t.Cleanup(svc.Close)

	source := filepath.Join(t.TempDir(), "photo.png")
	writeTestPNG(t, source, 32, 32)

	result := resultOf(t, call(t, svc, ipc.Request{ID: 1, Method: "files.thumbnail", Params: map[string]any{"path": source, "size": "large"}}))
	entry := result["results"].([]any)[0].(map[string]any)
	assert.NotEmpty(t, entry["thumbnail"])
	assert.Nil(t, entry["pending"])
}

func TestWatchListsTheThumbnailSizeItWasAskedFor(t *testing.T) {
	svc := NewService(&fakeBus{}, t.TempDir(), nil)
	t.Cleanup(svc.Close)

	root := t.TempDir()
	source := filepath.Join(root, "photo.png")
	writeTestPNG(t, source, 300, 300)
	generated := resultOf(t, call(t, svc, ipc.Request{ID: 1, Method: "files.thumbnail", Params: map[string]any{"path": source, "size": "large"}}))
	large := generated["results"].([]any)[0].(map[string]any)["thumbnail"]
	require.NotEmpty(t, large)

	listedThumbnail := func(params map[string]any) any {
		opened := resultOf(t, call(t, svc, ipc.Request{ID: 2, Method: "files.watch", Params: params}))
		return opened["entries"].([]any)[0].(map[string]any)["thumbnail"]
	}
	assert.Equal(t, large, listedThumbnail(map[string]any{"path": root, "thumbnailSize": "large"}))
	assert.Empty(t, listedThumbnail(map[string]any{"path": root}), "only the large size is cached")
}

package files

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/duxlabs-dev/dux-go/ipc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func call(t *testing.T, svc *Service, req ipc.Request) map[string]any {
	t.Helper()
	client, srv := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = srv.Close()
	})

	go svc.Handle(context.Background(), ipc.NewConnWriter(srv), req)

	line, err := bufio.NewReader(client).ReadBytes('\n')
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(line, &out))
	return out
}

func resultOf(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	require.NotContains(t, out, "error", "unexpected error response")
	result, ok := out["result"].(map[string]any)
	require.True(t, ok)
	return result
}

func TestHandleListReturnsEntries(t *testing.T) {
	svc := newTestService(t)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("hi"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".secret"), nil, 0o644))

	out := call(t, svc, ipc.Request{ID: 7, Method: "files.list", Params: map[string]any{"path": root}})
	assert.Equal(t, float64(7), out["id"])

	result := resultOf(t, out)
	assert.Equal(t, root, result["path"])
	assert.Equal(t, float64(1), result["total"])
	entries := result["entries"].([]any)
	require.Len(t, entries, 1)

	entry := entries[0].(map[string]any)
	assert.Equal(t, "a.txt", entry["name"])
	assert.Equal(t, float64(2), entry["size"])
	assert.Equal(t, false, entry["isDir"])
	assert.Equal(t, "text/plain", entry["mime"])
	assert.Contains(t, entry, "iconName")
	assert.Contains(t, entry, "mtimeMs")
}

func TestHandleListPagesAndSorts(t *testing.T) {
	svc := newTestService(t)
	root := t.TempDir()
	for _, name := range []string{"b", "a", "c"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(name), 0o644))
	}

	first := resultOf(t, call(t, svc, ipc.Request{ID: 1, Method: "files.list", Params: map[string]any{"path": root, "limit": "2"}}))
	assert.Len(t, first["entries"], 2)
	require.NotEmpty(t, first["cursor"])

	second := resultOf(t, call(t, svc, ipc.Request{ID: 2, Method: "files.list", Params: map[string]any{"path": root, "limit": "2", "cursor": first["cursor"]}}))
	entries := second["entries"].([]any)
	require.Len(t, entries, 1)
	assert.Equal(t, "c", entries[0].(map[string]any)["name"])
	assert.Empty(t, second["cursor"])

	desc := resultOf(t, call(t, svc, ipc.Request{ID: 3, Method: "files.list", Params: map[string]any{"path": root, "sort": "name", "desc": "true"}}))
	assert.Equal(t, "c", desc["entries"].([]any)[0].(map[string]any)["name"])
}

func TestHandleListIncludeHidden(t *testing.T) {
	svc := newTestService(t)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".secret"), nil, 0o644))

	out := call(t, svc, ipc.Request{ID: 1, Method: "files.list", Params: map[string]any{"path": root, "includeHidden": "true"}})
	assert.Len(t, resultOf(t, out)["entries"], 1)
}

func TestHandleListHiddenLast(t *testing.T) {
	svc := newTestService(t)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".secret"), nil, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "z.txt"), nil, 0o644))

	names := func(params map[string]any) []string {
		params["path"] = root
		params["includeHidden"] = "true"
		var out []string
		for _, e := range resultOf(t, call(t, svc, ipc.Request{ID: 1, Method: "files.list", Params: params}))["entries"].([]any) {
			out = append(out, e.(map[string]any)["name"].(string))
		}
		return out
	}
	assert.Equal(t, []string{".secret", "z.txt"}, names(map[string]any{}))
	assert.Equal(t, []string{"z.txt", ".secret"}, names(map[string]any{"hiddenLast": "true"}))
}

func TestHandleErrorsCarryCode(t *testing.T) {
	svc := newTestService(t)

	out := call(t, svc, ipc.Request{ID: 2, Method: "files.list", Params: map[string]any{"path": filepath.Join(t.TempDir(), "gone")}})
	assert.Equal(t, "ENOENT", out["code"])
	assert.Nil(t, out["result"])
	assert.NotEmpty(t, out["error"])

	assert.Equal(t, "EINVAL", call(t, svc, ipc.Request{ID: 3, Method: "files.list"})["code"])
	assert.Equal(t, "NOTSUPPORTED", call(t, svc, ipc.Request{ID: 4, Method: "files.nothing"})["code"])
	assert.Equal(t, "EINVAL", call(t, svc, ipc.Request{ID: 5, Method: "files.list", Params: map[string]any{"watchId": "w404"}})["code"])
}

func TestHandleWatchListsAndPages(t *testing.T) {
	svc := newTestService(t)
	root := t.TempDir()
	for _, name := range []string{"a", "b"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), nil, 0o644))
	}

	result := resultOf(t, call(t, svc, ipc.Request{ID: 1, Method: "files.watch", Params: map[string]any{"path": root, "limit": "1"}}))
	watchID := result["watchId"].(string)
	assert.Equal(t, "files:"+watchID, result["topic"])
	assert.Equal(t, true, result["watching"])
	assert.Equal(t, false, result["pollOnFocus"])
	assert.Len(t, result["entries"], 1)

	page := resultOf(t, call(t, svc, ipc.Request{ID: 2, Method: "files.list", Params: map[string]any{"watchId": watchID, "cursor": result["cursor"], "limit": "5"}}))
	assert.Equal(t, "b", page["entries"].([]any)[0].(map[string]any)["name"])

	closed := resultOf(t, call(t, svc, ipc.Request{ID: 3, Method: "files.unwatch", Params: map[string]any{"watchId": watchID}}))
	assert.Equal(t, true, closed["closed"])
	assert.Equal(t, "EINVAL", call(t, svc, ipc.Request{ID: 4, Method: "files.unwatch", Params: map[string]any{"watchId": watchID}})["code"])
}

func TestHandleStatAndCount(t *testing.T) {
	svc := newTestService(t)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "note.md"), []byte("# hi"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(root, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "sub", ".dot"), nil, 0o644))

	entry := resultOf(t, call(t, svc, ipc.Request{ID: 1, Method: "files.stat", Params: map[string]any{"path": filepath.Join(root, "note.md")}}))["entry"].(map[string]any)
	assert.Equal(t, "note.md", entry["name"])
	assert.Equal(t, "text/markdown", entry["mime"])

	counts := resultOf(t, call(t, svc, ipc.Request{ID: 2, Method: "files.count", Params: map[string]any{"paths": []any{root, filepath.Join(root, "sub"), filepath.Join(root, "note.md")}}}))["counts"].(map[string]any)
	assert.Equal(t, float64(2), counts[root].(map[string]any)["count"])
	assert.Equal(t, float64(0), counts[filepath.Join(root, "sub")].(map[string]any)["count"])
	assert.Equal(t, "ENOTDIR", counts[filepath.Join(root, "note.md")].(map[string]any)["code"])
}

func TestHandleIconLadders(t *testing.T) {
	svc := newTestService(t)
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "sub"), 0o755))

	icons := resultOf(t, call(t, svc, ipc.Request{ID: 1, Method: "files.icon", Params: map[string]any{"mimes": "image/png"}}))["icons"].(map[string]any)
	assert.Equal(t, []any{"image-png", "image-x-generic", "text-x-generic"}, icons["image/png"])

	icons = resultOf(t, call(t, svc, ipc.Request{ID: 2, Method: "files.icon", Params: map[string]any{"paths": []any{filepath.Join(root, "sub")}}}))["icons"].(map[string]any)
	assert.Equal(t, []any{"folder", "inode-directory"}, icons[filepath.Join(root, "sub")])
}

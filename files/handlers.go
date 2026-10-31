package files

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/duxlabs-dev/dux-go/ipc"
	"github.com/duxlabs-dev/dux-go/ipc/params"
)

type errorResponse struct {
	ID    int    `json:"id,omitempty"`
	Error string `json:"error"`
	Code  Code   `json:"code"`
}

type listResult struct {
	Path    string  `json:"path"`
	WatchID string  `json:"watchId,omitempty"`
	Seq     int     `json:"seq"`
	Entries []Entry `json:"entries"`
	Total   int     `json:"total"`
	Hidden  int     `json:"hidden"`
	Cursor  string  `json:"cursor,omitempty"`
}

type watchResult struct {
	listResult
	Topic       string `json:"topic"`
	Watching    bool   `json:"watching"`
	PollOnFocus bool   `json:"pollOnFocus"`
}

func (s *Service) Handle(ctx context.Context, w *ipc.ConnWriter, req ipc.Request) {
	switch req.Method {
	case "files.list":
		s.handleList(w, req)
	case "files.watch":
		s.handleWatch(ctx, w, req)
	case "files.unwatch":
		s.handleUnwatch(w, req)
	case "files.stat":
		s.handleStat(w, req)
	case "files.count":
		s.handleCount(w, req)
	case "files.thumbnail":
		s.handleThumbnail(w, req)
	case "files.icon":
		s.handleIcon(w, req)
	case "files.size":
		s.handleSize(ctx, w, req)
	case "files.sizeCancel":
		s.handleSizeCancel(w, req)
	case "files.userDirs":
		w.Respond(req.ID, map[string]any{"dirs": listUserDirs()})
	case "files.mkdir":
		s.handleMkdir(w, req)
	case "files.rename":
		s.handleRename(w, req)
	case "files.trash":
		s.handleTrash(w, req)
	default:
		respondCode(w, req.ID, CodeNotSupported, "unknown files method: "+req.Method)
	}
}

func (s *Service) handleList(w *ipc.ConnWriter, req ipc.Request) {
	opts, err := listOptions(req)
	if err != nil {
		respondErr(w, req.ID, err)
		return
	}

	if id := params.StringOpt(req.Params, "watchId", ""); id != "" {
		watcher, ok := s.watchByID(id)
		if !ok {
			respondCode(w, req.ID, CodeInvalid, "unknown watch: "+id)
			return
		}
		page, seq := watcher.page(opts)
		w.Respond(req.ID, listResult{Path: watcher.path, WatchID: id, Seq: seq, Entries: page.Entries, Total: page.Total, Hidden: page.Hidden, Cursor: page.Cursor})
		return
	}

	path, err := pathParam(req)
	if err != nil {
		respondErr(w, req.ID, err)
		return
	}
	page, err := s.list(path, opts)
	if err != nil {
		respondErr(w, req.ID, err)
		return
	}
	w.Respond(req.ID, listResult{Path: path, Entries: page.Entries, Total: page.Total, Hidden: page.Hidden, Cursor: page.Cursor})
}

func (s *Service) handleWatch(ctx context.Context, w *ipc.ConnWriter, req ipc.Request) {
	path, err := pathParam(req)
	if err != nil {
		respondErr(w, req.ID, err)
		return
	}

	opts, err := listOptions(req)
	if err != nil {
		respondErr(w, req.ID, err)
		return
	}
	watcher, err := s.openWatch(ctx, path, opts)
	if err != nil {
		respondErr(w, req.ID, err)
		return
	}

	page, seq := watcher.page(opts)
	w.Respond(req.ID, watchResult{
		listResult:  listResult{Path: path, WatchID: watcher.id, Seq: seq, Entries: page.Entries, Total: page.Total, Hidden: page.Hidden, Cursor: page.Cursor},
		Topic:       watcher.topic,
		Watching:    watcher.watching,
		PollOnFocus: watcher.pollOnFocus,
	})
}

func (s *Service) handleUnwatch(w *ipc.ConnWriter, req ipc.Request) {
	id := params.StringOpt(req.Params, "watchId", "")
	watcher, ok := s.watchByID(id)
	if !ok {
		respondCode(w, req.ID, CodeInvalid, "unknown watch: "+id)
		return
	}
	watcher.close()
	w.Respond(req.ID, map[string]any{"watchId": id, "closed": true})
}

func (s *Service) handleStat(w *ipc.ConnWriter, req ipc.Request) {
	path, err := pathParam(req)
	if err != nil {
		respondErr(w, req.ID, err)
		return
	}
	entry, err := s.Stat(path)
	if err != nil {
		respondErr(w, req.ID, err)
		return
	}
	w.Respond(req.ID, map[string]any{"entry": entry})
}

func (s *Service) handleCount(w *ipc.ConnWriter, req ipc.Request) {
	paths := pathsParam(req)
	if len(paths) == 0 {
		respondCode(w, req.ID, CodeInvalid, "files.count requires paths")
		return
	}

	includeHidden := boolParam(req.Params, "includeHidden", false)
	counts := make(map[string]Count, len(paths))
	for _, path := range paths {
		counts[path] = s.count(path, includeHidden)
	}
	w.Respond(req.ID, map[string]any{"counts": counts})
}

func (s *Service) handleSize(ctx context.Context, w *ipc.ConnWriter, req ipc.Request) {
	paths := pathsParam(req)
	if len(paths) == 0 {
		respondCode(w, req.ID, CodeInvalid, "files.size requires paths")
		return
	}
	sc := s.startSize(ctx, paths)
	w.Respond(req.ID, map[string]any{"scanId": sc.id, "topic": sizeTopic})
}

func (s *Service) handleSizeCancel(w *ipc.ConnWriter, req ipc.Request) {
	id := params.StringOpt(req.Params, "scanId", "")
	if id == "" {
		respondCode(w, req.ID, CodeInvalid, "files.sizeCancel requires scanId")
		return
	}
	w.Respond(req.ID, map[string]any{"cancelled": s.cancelScan(id)})
}

func (s *Service) handleThumbnail(w *ipc.ConnWriter, req ipc.Request) {
	paths := pathsParam(req)
	if len(paths) == 0 {
		respondCode(w, req.ID, CodeInvalid, "files.thumbnail requires paths")
		return
	}

	size := ParseThumbSize(params.StringOpt(req.Params, "size", "normal"))
	var watcher *watch
	if id := params.StringOpt(req.Params, "watchId", ""); id != "" {
		found, ok := s.watchByID(id)
		if !ok {
			respondCode(w, req.ID, CodeInvalid, "unknown watch: "+id)
			return
		}
		watcher = found
	}

	w.Respond(req.ID, map[string]any{"size": size, "results": s.thumbnails(paths, size, watcher)})
}

func (s *Service) handleMkdir(w *ipc.ConnWriter, req ipc.Request) {
	path, err := pathParam(req)
	if err != nil {
		respondErr(w, req.ID, err)
		return
	}
	entry, err := s.makeDir(path)
	if err != nil {
		respondErr(w, req.ID, err)
		return
	}
	w.Respond(req.ID, map[string]any{"path": entry.Path, "entry": entry})
}

func (s *Service) handleRename(w *ipc.ConnWriter, req ipc.Request) {
	path, err := pathParam(req)
	if err != nil {
		respondErr(w, req.ID, err)
		return
	}
	renamed, err := s.rename(path, params.StringOpt(req.Params, "name", ""))
	if err != nil {
		respondErr(w, req.ID, err)
		return
	}
	w.Respond(req.ID, map[string]any{"path": renamed})
}

func (s *Service) handleTrash(w *ipc.ConnWriter, req ipc.Request) {
	paths := exactPathsParam(req)
	if len(paths) == 0 {
		respondCode(w, req.ID, CodeInvalid, "files.trash requires paths")
		return
	}
	if relative := slices.IndexFunc(paths, func(p string) bool { return !filepath.IsAbs(p) }); relative >= 0 {
		respondCode(w, req.ID, CodeInvalid, "path must be absolute: "+paths[relative])
		return
	}
	w.Respond(req.ID, s.trashPaths(paths))
}

func (s *Service) handleIcon(w *ipc.ConnWriter, req ipc.Request) {
	keys := pathsParam(req)
	if len(keys) == 0 {
		keys = stringsParam(req.Params, "mimes")
	}
	if len(keys) == 0 {
		respondCode(w, req.ID, CodeInvalid, "files.icon requires paths or mimes")
		return
	}
	w.Respond(req.ID, map[string]any{"icons": s.icons(keys)})
}

func listOptions(req ipc.Request) (ListOptions, error) {
	filters, err := parseFilters(stringsParam(req.Params, "filters"))
	if err != nil {
		return ListOptions{}, err
	}
	return ListOptions{
		IncludeHidden: boolParam(req.Params, "includeHidden", false),
		Filters:       filters,
		Sort: SortSpec{
			Key:        ParseSortKey(params.StringOpt(req.Params, "sort", "")),
			Desc:       boolParam(req.Params, "desc", false),
			DirsFirst:  boolParam(req.Params, "dirsFirst", true),
			HiddenLast: boolParam(req.Params, "hiddenLast", false),
		},
		Limit:  intParam(req.Params, "limit", DefaultPageSize),
		Cursor: params.StringOpt(req.Params, "cursor", ""),
	}, nil
}

func pathParam(req ipc.Request) (string, error) {
	path := params.StringOpt(req.Params, "path", "")
	if path == "" {
		return "", &Error{Code: CodeInvalid, Err: errors.New(req.Method + " requires a path")}
	}
	return path, nil
}

func pathsParam(req ipc.Request) []string {
	if paths := stringsParam(req.Params, "paths"); len(paths) > 0 {
		return paths
	}
	if path := params.StringOpt(req.Params, "path", ""); path != "" {
		return []string{path}
	}
	return nil
}

func exactPathsParam(req ipc.Request) []string {
	if paths := params.StringSlice(req.Params, "paths"); len(paths) > 0 {
		return paths
	}
	for _, key := range []string{"paths", "path"} {
		if path := params.StringOpt(req.Params, key, ""); path != "" {
			return []string{path}
		}
	}
	return nil
}

func stringsParam(p map[string]any, key string) []string {
	if values := params.StringSlice(p, key); len(values) > 0 {
		return values
	}
	raw := params.StringOpt(p, key, "")
	if raw == "" {
		return nil
	}
	return strings.Split(raw, ",")
}

func boolParam(p map[string]any, key string, def bool) bool {
	switch v := p[key].(type) {
	case bool:
		return v
	case string:
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			return def
		}
		return parsed
	}
	return def
}

func intParam(p map[string]any, key string, def int) int {
	switch v := p[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		parsed, err := strconv.Atoi(v)
		if err != nil {
			return def
		}
		return parsed
	}
	return def
}

func respondErr(w *ipc.ConnWriter, id int, err error) {
	respondCode(w, id, CodeOf(err), err.Error())
}

func respondCode(w *ipc.ConnWriter, id int, code Code, msg string) {
	_ = w.WriteResponse(errorResponse{ID: id, Error: msg, Code: code})
}

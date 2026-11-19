package files

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	pendingCap      = 64
	coalesceWindow  = 100 * time.Millisecond
	enrichChunk     = 64
	thumbChunk      = 4
	firstPageBudget = 40 * time.Millisecond
)

type notifier interface {
	Add(path string) error
	Events() <-chan fsnotify.Event
	Errors() <-chan error
	Close() error
}

type fsnotifyWatcher struct{ inner *fsnotify.Watcher }

func newFsnotifyWatcher() (notifier, error) {
	inner, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &fsnotifyWatcher{inner: inner}, nil
}

func (w *fsnotifyWatcher) Add(path string) error         { return w.inner.Add(path) }
func (w *fsnotifyWatcher) Events() <-chan fsnotify.Event { return w.inner.Events }
func (w *fsnotifyWatcher) Errors() <-chan error          { return w.inner.Errors }
func (w *fsnotifyWatcher) Close() error                  { return w.inner.Close() }

type batch struct {
	WatchID    string   `json:"watchId"`
	Kind       string   `json:"kind"`
	Seq        int      `json:"seq"`
	Added      []Entry  `json:"added,omitempty"`
	AddedAt    []int    `json:"addedAt,omitempty"`
	Changed    []Entry  `json:"changed,omitempty"`
	Removed    []string `json:"removed,omitempty"`
	Thumbnails []Entry  `json:"thumbnails,omitempty"`
	Hidden     *int     `json:"hidden,omitempty"`
}

type watch struct {
	id          string
	topic       string
	path        string
	svc         *Service
	notifier    notifier
	watching    bool
	pollOnFocus bool
	done        chan struct{}
	closeOnce   sync.Once

	mu            sync.Mutex
	stopFromCtx   func() bool
	entries       []Entry
	spec          SortSpec
	includeHidden bool
	filters       nameFilter
	thumbSize     string
	seq           int
	hidden        int
	misplaced     bool
	subscribed    bool
	pending       []batch
	dropped       bool
}

func (s *Service) openWatch(ctx context.Context, path string, opts ListOptions) (*watch, error) {
	notify := s.armNotifier(path)
	entries, err := s.builder.readDir(path)
	if err != nil {
		if notify != nil {
			_ = notify.Close()
		}
		return nil, err
	}
	sortEntries(entries, opts.Sort)
	pending := unenriched(entries)

	w := &watch{
		path:          path,
		svc:           s,
		notifier:      notify,
		watching:      notify != nil,
		done:          make(chan struct{}),
		entries:       entries,
		spec:          opts.Sort,
		includeHidden: opts.IncludeHidden,
		filters:       opts.Filters,
		thumbSize:     ParseThumbSize(opts.ThumbSize),
		subscribed:    s.attachOnOpen.Load(),
		pollOnFocus:   needsFocusRefresh(path),
	}
	s.register(w)
	if w.watching {
		go w.run()
	}
	w.closeWith(ctx)
	w.awaitEnrich(pending)
	return w, nil
}

// A small directory's sniffed mimes land in the first page instead of a second paint.
func (w *watch) awaitEnrich(pending []Entry) {
	done := w.enrich(pending)
	budget := time.NewTimer(firstPageBudget)
	defer budget.Stop()
	select {
	case <-done:
	case <-budget.C:
	}
}

func (s *Service) armNotifier(path string) notifier {
	notify, err := s.newNotifier()
	if err != nil {
		s.log("watch %s: notifications unavailable: %v", path, err)
		return nil
	}
	if err := notify.Add(path); err != nil {
		s.log("watch %s: %v", path, err)
		_ = notify.Close()
		return nil
	}
	return notify
}

func (w *watch) closeWith(ctx context.Context) {
	stop := context.AfterFunc(ctx, w.close)
	w.mu.Lock()
	w.stopFromCtx = stop
	w.mu.Unlock()
	if w.closed() {
		stop()
	}
}

func (w *watch) run() {
	pending := map[string]bool{}
	flush := time.NewTimer(coalesceWindow)
	if !flush.Stop() {
		<-flush.C
	}
	armed := false

	for {
		select {
		case <-w.done:
			return
		case event, ok := <-w.notifier.Events():
			if !ok {
				return
			}
			if w.isSelf(event) {
				w.publishGone()
				return
			}
			pending[filepath.Base(event.Name)] = true
			if !armed {
				flush.Reset(coalesceWindow)
				armed = true
			}
		case err, ok := <-w.notifier.Errors():
			if !ok {
				return
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				w.resync()
				clear(pending)
				armed = false
			}
		case <-flush.C:
			armed = false
			names := slices.Collect(maps.Keys(pending))
			clear(pending)
			w.flush(names)
		}
	}
}

func (w *watch) isSelf(event fsnotify.Event) bool {
	if event.Name != w.path {
		return false
	}
	return event.Op&(fsnotify.Remove|fsnotify.Rename) != 0
}

func (w *watch) flush(names []string) {
	if _, err := os.Stat(w.path); err != nil {
		w.publishGone()
		return
	}

	hidden := hiddenNames(w.path)
	fresh := make([]Entry, 0, len(names))

	w.mu.Lock()
	before := namesOf(w.view())
	touched := map[string]bool{}
	for _, name := range names {
		touched[name] = true
		entry, err := w.svc.builder.entry(w.path, name, hidden[name])
		index := w.indexOf(name)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if index >= 0 {
				w.entries = slices.Delete(w.entries, index, index+1)
			}
		case err != nil:
			continue
		case index >= 0:
			entry.Thumbnail = carryThumbnail(w.entries[index], entry)
			w.entries[index] = entry
			w.entries = w.resortAround(index)
			fresh = append(fresh, entry)
		default:
			w.entries = insertSorted(w.entries, entry, w.spec)
			fresh = append(fresh, entry)
		}
	}
	changes := w.diff(before, touched)
	if hidden := hiddenCount(w.entries, w.includeHidden, w.filters); hidden != w.hidden {
		w.hidden = hidden
		changes.Hidden = &hidden
	}
	w.publishLocked(changes)
	w.mu.Unlock()

	w.enrich(unenriched(fresh))
}

func (w *watch) view() []Entry {
	return filterView(w.entries, w.includeHidden, w.filters)
}

func namesOf(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name
	}
	return out
}

// diff targets a client that removes by name first, then inserts at final indices in order.
func (w *watch) diff(before []string, touched map[string]bool) batch {
	view := w.view()
	at := make(map[string]int, len(view))
	for i, e := range view {
		at[e.Name] = i
	}

	changes := batch{Kind: "batch"}
	existed := make(map[string]bool, len(before))
	last, inOrder := -1, true
	for _, name := range before {
		index, kept := at[name]
		if !kept {
			changes.Removed = append(changes.Removed, name)
			continue
		}
		existed[name] = true
		inOrder = inOrder && index > last
		last = index
	}

	for index, entry := range view {
		switch {
		case !existed[entry.Name]:
			changes.Added = append(changes.Added, entry)
			changes.AddedAt = append(changes.AddedAt, index)
		case !touched[entry.Name]:
			continue
		case inOrder:
			changes.Changed = append(changes.Changed, entry)
		default:
			changes.Removed = append(changes.Removed, entry.Name)
			changes.Added = append(changes.Added, entry)
			changes.AddedAt = append(changes.AddedAt, index)
		}
	}
	return changes
}

func (w *watch) resortAround(index int) []Entry {
	entry := w.entries[index]
	rest := slices.Delete(slices.Clone(w.entries), index, index+1)
	return insertSorted(rest, entry, w.spec)
}

func (w *watch) indexOf(name string) int {
	return slices.IndexFunc(w.entries, func(e Entry) bool { return e.Name == name })
}

func carryThumbnail(old, fresh Entry) string {
	if old.MtimeMs == fresh.MtimeMs {
		return old.Thumbnail
	}
	return ""
}

func (w *watch) resync() {
	entries, err := w.svc.builder.readDir(w.path)
	if err != nil {
		w.publishGone()
		return
	}

	w.mu.Lock()
	sortEntries(entries, w.spec)
	w.entries = entries
	pending := unenriched(entries)
	w.publishLocked(batch{Kind: "resync"})
	w.mu.Unlock()

	w.enrich(pending)
}

func unenriched(entries []Entry) []Entry {
	pending := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if e.Unreadable || e.IsDir || e.SymlinkBroken {
			continue
		}
		if !needsSniff(e.Mime) && !e.Thumbnailable && !needsDesktop(e) {
			continue
		}
		pending = append(pending, e)
	}
	return pending
}

func (w *watch) enrich(pending []Entry) <-chan struct{} {
	done := make(chan struct{})
	chunks := slices.Collect(slices.Chunk(pending, enrichChunk))
	if len(chunks) == 0 {
		close(done)
		return done
	}
	var remaining atomic.Int32
	remaining.Store(int32(len(chunks)))
	for _, chunk := range chunks {
		w.svc.pool.submit(func() {
			w.enrichChunk(chunk)
			if remaining.Add(-1) == 0 {
				w.settleOrder()
				close(done)
			}
		})
	}
	return done
}

func (w *watch) enrichChunk(chunk []Entry) {
	thumbSize := w.thumbnailSize()
	changed := make([]Entry, 0, len(chunk))
	for _, e := range chunk {
		if w.closed() {
			return
		}
		updated := e
		if needsSniff(updated.Mime) {
			if sniffed := w.svc.builder.mime.Sniff(updated.Path); sniffed != "" {
				updated.Mime = sniffed
			}
		}
		updated.Thumbnailable = w.svc.thumbs.Supports(updated.Mime)
		if updated.Thumbnailable {
			updated.Thumbnail, _ = w.svc.thumbs.Lookup(updated.Path, updated.MtimeMs, thumbSize)
		}
		updated.IconName = w.svc.builder.icons.best(updated)
		w.svc.builder.display(&updated)
		if updated.Mime == e.Mime && updated.IconName == e.IconName && updated.Thumbnail == e.Thumbnail && updated.Thumbnailable == e.Thumbnailable && updated.DisplayName == e.DisplayName {
			continue
		}
		changed = append(changed, updated)
	}
	w.applyChanged(changed, "batch")
}

func (w *watch) applyChanged(changed []Entry, kind string) {
	if len(changed) == 0 {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	applied := make([]Entry, 0, len(changed))
	for _, e := range changed {
		index := w.indexOf(e.Name)
		if index < 0 || w.entries[index].MtimeMs != e.MtimeMs {
			continue
		}
		if kind == "thumbnails" {
			w.entries[index].Thumbnail = e.Thumbnail
			applied = append(applied, w.entries[index])
			continue
		}
		e.nameKey = w.entries[index].nameKey
		w.misplaced = w.misplaced || (w.spec.Key == SortType && w.entries[index].Mime != e.Mime)
		w.entries[index] = e
		applied = append(applied, e)
	}
	applied = filterView(applied, w.includeHidden, w.filters)
	if len(applied) == 0 {
		return
	}

	switch kind {
	case "thumbnails":
		w.publishLocked(batch{Kind: kind, Thumbnails: applied})
	default:
		w.publishLocked(batch{Kind: kind, Changed: applied})
	}
}

func (w *watch) settleOrder() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.misplaced || w.closed() {
		return
	}
	w.misplaced = false

	before := namesOf(w.view())
	sortEntries(w.entries, w.spec)
	after := w.view()
	moved := map[string]bool{}
	for i, e := range after {
		if i >= len(before) || before[i] != e.Name {
			moved[e.Name] = true
		}
	}
	if len(moved) == 0 {
		return
	}
	w.publishLocked(w.diff(before, moved))
}

// Closed first so a client told "gone" can no longer resolve the watch id.
func (w *watch) publishGone() {
	w.close()
	w.mu.Lock()
	w.publishLocked(batch{Kind: "gone"})
	w.mu.Unlock()
}

func (w *watch) publishLocked(payload batch) {
	if payload.Kind == "batch" && len(payload.Added)+len(payload.Changed)+len(payload.Removed) == 0 && payload.Hidden == nil {
		return
	}
	w.seq++
	payload.WatchID = w.id
	payload.Seq = w.seq

	if w.subscribed {
		w.svc.publish(w.topic, payload)
		return
	}
	if len(w.pending) >= pendingCap {
		w.pending = nil
		w.dropped = true
		return
	}
	w.pending = append(w.pending, payload)
}

// attach flushes events buffered before the subscribe; an overflowed buffer becomes one resync.
func (w *watch) attach() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.subscribed = true
	buffered := w.pending
	w.pending = nil

	if w.dropped {
		w.dropped = false
		w.seq++
		w.svc.publish(w.topic, batch{WatchID: w.id, Kind: "resync", Seq: w.seq})
		return
	}
	for _, payload := range buffered {
		w.svc.publish(w.topic, payload)
	}
}

func (w *watch) sequence() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seq
}

func (w *watch) page(opts ListOptions) (Page, int) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if opts.Sort != w.spec || w.misplaced {
		w.spec = opts.Sort
		w.misplaced = false
		sortEntries(w.entries, w.spec)
	}
	w.includeHidden = opts.IncludeHidden
	w.filters = opts.Filters
	w.thumbSize = ParseThumbSize(opts.ThumbSize)
	w.hidden = hiddenCount(w.entries, w.includeHidden, w.filters)
	page := pageOf(w.view(), opts.Cursor, opts.Limit)
	page.Entries = slices.Clone(page.Entries)
	page.Hidden = w.hidden
	return page, w.seq
}

func (w *watch) thumbnailSize() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.thumbSize
}

func (w *watch) entry(name string) (Entry, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	index := w.indexOf(name)
	if index < 0 {
		return Entry{}, false
	}
	return w.entries[index], true
}

func (w *watch) closed() bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}

func (w *watch) close() {
	w.closeOnce.Do(func() {
		close(w.done)
		w.mu.Lock()
		stop := w.stopFromCtx
		w.mu.Unlock()
		if stop != nil {
			stop()
		}
		if w.notifier != nil {
			_ = w.notifier.Close()
		}
		w.svc.unregister(w.id)
	})
}

func needsFocusRefresh(path string) bool {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime == "" {
		return false
	}
	for _, prefix := range []string{"gvfs", "doc"} {
		mount := filepath.Join(runtime, prefix)
		if path == mount || isUnder(path, mount) {
			return true
		}
	}
	return false
}

func isUnder(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !filepath.IsAbs(rel) && rel != "." && !hasParentEscape(rel)
}

func hasParentEscape(rel string) bool {
	return rel == ".." || len(rel) > 3 && rel[:3] == "../"
}

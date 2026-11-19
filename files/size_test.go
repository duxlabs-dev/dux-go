package files

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sizeBus struct {
	mu     sync.Mutex
	events []SizeEvent
}

func (b *sizeBus) Publish(topic string, data any) {
	event, ok := data.(SizeEvent)
	if !ok || topic != sizeTopic {
		return
	}
	b.mu.Lock()
	b.events = append(b.events, event)
	b.mu.Unlock()
}

func (b *sizeBus) final(t *testing.T) SizeEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		for _, event := range b.events {
			if event.Done {
				b.mu.Unlock()
				return event
			}
		}
		b.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no terminal size event")
	return SizeEvent{}
}

func sizeTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), make([]byte, 100), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sub", "deep"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "sub", "b.bin"), make([]byte, 250), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "sub", "deep", "c.bin"), make([]byte, 400), 0o644))
	return root
}

func TestSizeSumsTheWholeTree(t *testing.T) {
	bus := &sizeBus{}
	svc := NewService(bus, t.TempDir(), nil)
	t.Cleanup(svc.Close)

	svc.startSize(context.Background(), []string{sizeTree(t)})

	event := bus.final(t)
	assert.Equal(t, int64(750), event.Bytes)
	assert.Equal(t, 3, event.Files)
	assert.Equal(t, 2, event.Dirs)
	assert.False(t, event.Cancelled)
	assert.Empty(t, event.Code)
}

func TestSizeOfASingleFile(t *testing.T) {
	bus := &sizeBus{}
	svc := NewService(bus, t.TempDir(), nil)
	t.Cleanup(svc.Close)

	path := filepath.Join(t.TempDir(), "one.bin")
	require.NoError(t, os.WriteFile(path, make([]byte, 42), 0o644))
	svc.startSize(context.Background(), []string{path})

	event := bus.final(t)
	assert.Equal(t, int64(42), event.Bytes)
	assert.Equal(t, 1, event.Files)
	assert.Equal(t, 0, event.Dirs)
}

func TestSizeNeverFollowsSymlinks(t *testing.T) {
	bus := &sizeBus{}
	svc := NewService(bus, t.TempDir(), nil)
	t.Cleanup(svc.Close)

	root := t.TempDir()
	target := sizeTree(t)
	require.NoError(t, os.Symlink(target, filepath.Join(root, "link")))
	svc.startSize(context.Background(), []string{root})

	event := bus.final(t)
	assert.Equal(t, 1, event.Files, "the symlink counts as one entry, its target is not walked")
	assert.Equal(t, 0, event.Dirs)
	assert.Less(t, event.Bytes, int64(750))
}

func TestSizeReportsAMissingPath(t *testing.T) {
	bus := &sizeBus{}
	svc := NewService(bus, t.TempDir(), nil)
	t.Cleanup(svc.Close)

	svc.startSize(context.Background(), []string{filepath.Join(t.TempDir(), "gone")})

	event := bus.final(t)
	assert.Equal(t, string(CodeNotFound), event.Code)
	assert.Equal(t, int64(0), event.Bytes)
}

func TestSizeCancelledContextMarksTheEvent(t *testing.T) {
	bus := &sizeBus{}
	svc := NewService(bus, t.TempDir(), nil)
	t.Cleanup(svc.Close)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sc := &scan{id: "s1", svc: svc, cancel: func() {}}
	sc.run(ctx, []string{sizeTree(t)})

	event := bus.final(t)
	assert.True(t, event.Cancelled)
	assert.True(t, event.Done)
}

func TestSizeCancelUnregistersTheScan(t *testing.T) {
	bus := &sizeBus{}
	svc := NewService(bus, t.TempDir(), nil)
	t.Cleanup(svc.Close)

	ctx, cancel := context.WithCancel(context.Background())
	sc := &scan{svc: svc, cancel: cancel}
	sc.id = svc.registerScan(sc)
	assert.True(t, svc.cancelScan(sc.id))

	sc.run(ctx, []string{sizeTree(t)})
	assert.True(t, bus.final(t).Cancelled)
	assert.False(t, svc.cancelScan(sc.id), "a finished scan is no longer cancellable")
}

func TestSizeUnknownScanCancelIsReported(t *testing.T) {
	svc := newTestService(t)
	assert.False(t, svc.cancelScan("s404"))
}

func TestSizeScansLeaveNoGoroutinesBehind(t *testing.T) {
	bus := &sizeBus{}
	svc := NewService(bus, t.TempDir(), nil)

	root := sizeTree(t)
	for range 5 {
		svc.startSize(context.Background(), []string{root})
	}
	bus.final(t)
	svc.Close()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		svc.mu.Lock()
		live := len(svc.scans)
		svc.mu.Unlock()
		if live == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("scans still registered after Close")
}

func TestSizeOnCancelledContextCancelsTheScan(t *testing.T) {
	bus := &sizeBus{}
	svc := NewService(bus, t.TempDir(), nil)
	t.Cleanup(svc.Close)
	root := sizeTree(t)

	for range 20 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		svc.startSize(ctx, []string{root})
	}
	svc.Close()
	require.Eventually(t, func() bool {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		return len(svc.scans) == 0
	}, 5*time.Second, 5*time.Millisecond)
}

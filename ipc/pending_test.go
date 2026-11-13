package ipc_test

import (
	"bufio"
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/duxlabs-dev/dux-go/ipc"
)

func uiListener(t *testing.T, bus *ipc.EventBus) <-chan string {
	t.Helper()
	server, client := net.Pipe()
	t.Cleanup(func() { server.Close(); client.Close() })
	sub := bus.NewSubscriber(context.Background(), ipc.NewConnWriter(server))
	t.Cleanup(sub.Close)
	sub.Subscribe(ipc.PendingTopic)

	lines := make(chan string, 8)
	go func() {
		scanner := bufio.NewScanner(client)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	return lines
}

func next(t *testing.T, lines <-chan string) string {
	t.Helper()
	select {
	case line := <-lines:
		return line
	case <-time.After(time.Second):
		t.Fatal("no event")
		return ""
	}
}

func TestPendingOpenQueuesUntilTheUIListens(t *testing.T) {
	bus := ipc.NewEventBus()
	var pending ipc.PendingOpen

	pending.Deliver(bus, map[string]any{"n": 1})
	pending.Deliver(bus, map[string]any{"n": 2})
	assert.Equal(t, []map[string]any{{"n": 1}, {"n": 2}}, pending.Take())
	assert.Empty(t, pending.Take(), "Take empties the queue")

	pending.Deliver(bus, map[string]any{"n": 3})
	lines := uiListener(t, bus)
	pending.Flush(bus)
	assert.JSONEq(t, `{"event":"ui","data":{"n":3}}`, next(t, lines))
	assert.Empty(t, pending.Take())

	pending.Deliver(bus, map[string]any{"n": 4})
	assert.JSONEq(t, `{"event":"ui","data":{"n":4}}`, next(t, lines))
	assert.Empty(t, pending.Take(), "a listening UI gets it at once")
}

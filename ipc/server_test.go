package ipc_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/duxlabs-dev/dux-go/ipc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startServer(t *testing.T, cfg ipc.Config, handler ipc.Handler) *ipc.Server {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	srv := ipc.NewServer(cfg, handler)
	require.NoError(t, srv.Listen())

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Serve(ctx) }()
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func TestHandshakeAndPing(t *testing.T) {
	srv := startServer(t, ipc.Config{
		AppName:      "danktest",
		APIVersion:   1,
		Capabilities: []string{"things", "subscribe"},
	}, nil)

	conn, err := net.Dial("unix", srv.SocketPath())
	require.NoError(t, err)
	defer conn.Close()

	r := bufio.NewReader(conn)
	line, err := r.ReadBytes('\n')
	require.NoError(t, err)
	assert.JSONEq(t, `{"apiVersion":1,"capabilities":["things","subscribe"]}`, string(line))

	_, err = conn.Write([]byte(`{"id":1,"method":"ping"}` + "\n"))
	require.NoError(t, err)

	line, err = r.ReadBytes('\n')
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":1,"result":{"pong":true}}`, string(line))
}

func TestClientCallCustomHandler(t *testing.T) {
	handler := func(ctx context.Context, w *ipc.ConnWriter, req ipc.Request, sub *ipc.Subscriber) {
		switch req.Method {
		case "echo":
			w.Respond(req.ID, req.Params)
		default:
			w.RespondError(req.ID, "unknown method: "+req.Method)
		}
	}
	srv := startServer(t, ipc.Config{AppName: "danktest", APIVersion: 1}, handler)

	client, err := ipc.Dial(srv.SocketPath())
	require.NoError(t, err)
	defer client.Close()

	resp, err := client.Call(ipc.Request{ID: 7, Method: "echo", Params: map[string]any{"hello": "world"}})
	require.NoError(t, err)
	assert.Equal(t, 7, resp.ID)
	assert.Empty(t, resp.Error)
	require.NotNil(t, resp.Result)
	assert.Equal(t, map[string]any{"hello": "world"}, *resp.Result)

	resp, err = client.Call(ipc.Request{ID: 8, Method: "nope"})
	require.NoError(t, err)
	assert.Equal(t, "unknown method: nope", resp.Error)
}

func TestSubscribeAndPublish(t *testing.T) {
	subscribed := make(chan []string, 1)
	srv := startServer(t, ipc.Config{
		AppName:                "danktest",
		APIVersion:             1,
		DefaultSubscribeTopics: []string{"things"},
		OnSubscribe:            func(topics []string, sub *ipc.Subscriber) { subscribed <- topics },
	}, nil)

	conn, err := net.Dial("unix", srv.SocketPath())
	require.NoError(t, err)
	defer conn.Close()

	r := bufio.NewReader(conn)
	_, err = r.ReadBytes('\n')
	require.NoError(t, err)

	_, err = conn.Write([]byte(`{"id":1,"method":"subscribe"}` + "\n"))
	require.NoError(t, err)

	line, err := r.ReadBytes('\n')
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":1,"result":{"topics":["things"]}}`, string(line))

	select {
	case topics := <-subscribed:
		assert.Equal(t, []string{"things"}, topics)
	case <-time.After(time.Second):
		t.Fatal("OnSubscribe not called")
	}

	require.Eventually(t, func() bool { return srv.Bus().HasSubscriber("things") }, time.Second, 10*time.Millisecond)
	srv.Bus().Publish("things", map[string]any{"n": 1})
	srv.Bus().Publish("other", map[string]any{"n": 2})

	line, err = r.ReadBytes('\n')
	require.NoError(t, err)
	var ev map[string]any
	require.NoError(t, json.Unmarshal(line, &ev))
	assert.Equal(t, "things", ev["event"])
	assert.Equal(t, map[string]any{"n": float64(1)}, ev["data"])
}

func TestSubscribeHandlerOverride(t *testing.T) {
	srv := startServer(t, ipc.Config{
		AppName:    "danktest",
		APIVersion: 1,
		SubscribeHandler: func(ctx context.Context, w *ipc.ConnWriter, req ipc.Request, sub *ipc.Subscriber) {
			w.Respond(req.ID, map[string]any{"method": req.Method, "custom": true})
		},
	}, nil)

	client, err := ipc.Dial(srv.SocketPath())
	require.NoError(t, err)
	defer client.Close()

	resp, err := client.Call(ipc.Request{ID: 1, Method: "subscribe", Params: map[string]any{"services": []any{"network"}}})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"method": "subscribe", "custom": true}, *resp.Result)

	resp, err = client.Call(ipc.Request{ID: 2, Method: "unsubscribe"})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"method": "unsubscribe", "custom": true}, *resp.Result)
}

func TestFindRunningSocket(t *testing.T) {
	srv := startServer(t, ipc.Config{AppName: "danktest", APIVersion: 1}, nil)

	path, err := ipc.FindRunningSocket("danktest")
	require.NoError(t, err)
	assert.Equal(t, srv.SocketPath(), path)

	_, err = ipc.FindRunningSocket("nosuchapp")
	assert.Error(t, err)
}

func TestMux(t *testing.T) {
	mux := ipc.NewMux()
	mux.Handle("version", func(ctx context.Context, w *ipc.ConnWriter, req ipc.Request, sub *ipc.Subscriber) {
		w.Respond(req.ID, map[string]any{"version": "1.0"})
	})
	mux.HandlePrefix("things.", func(ctx context.Context, w *ipc.ConnWriter, req ipc.Request, sub *ipc.Subscriber) {
		w.Respond(req.ID, map[string]any{"method": req.Method})
	})
	srv := startServer(t, ipc.Config{AppName: "danktest", APIVersion: 1}, mux.ServeIPC)

	client, err := ipc.Dial(srv.SocketPath())
	require.NoError(t, err)
	defer client.Close()

	resp, err := client.Call(ipc.Request{ID: 1, Method: "version"})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"version": "1.0"}, *resp.Result)

	resp, err = client.Call(ipc.Request{ID: 2, Method: "things.list"})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"method": "things.list"}, *resp.Result)

	resp, err = client.Call(ipc.Request{ID: 3, Method: "bogus"})
	require.NoError(t, err)
	assert.Equal(t, "unknown method: bogus", resp.Error)
}

func echoLength(ctx context.Context, w *ipc.ConnWriter, req ipc.Request, sub *ipc.Subscriber) {
	w.Respond(req.ID, map[string]any{"len": len(req.GetOr("data", ""))})
}

func liveHeap() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

func TestLargeRequestDoesNotKeepItsBuffer(t *testing.T) {
	const size = 24 << 20
	srv := startServer(t, ipc.Config{AppName: "danktest", APIVersion: 1, MaxLineSize: 32 << 20}, echoLength)

	client, err := ipc.Dial(srv.SocketPath())
	require.NoError(t, err)
	defer client.Close()
	baseline := liveHeap()

	resp, err := client.Call(ipc.Request{ID: 1, Method: "echo", Params: map[string]any{"data": strings.Repeat("x", size)}})
	require.NoError(t, err)
	require.Empty(t, resp.Error)
	assert.Equal(t, map[string]any{"len": float64(size)}, *resp.Result)

	resp, err = client.Call(ipc.Request{ID: 2, Method: "echo", Params: map[string]any{"data": "small"}})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"len": float64(5)}, *resp.Result)

	assert.Eventually(t, func() bool { return liveHeap() < baseline+8<<20 }, 2*time.Second, 20*time.Millisecond,
		"the open connection must not hold on to the big request's buffer")
}

func TestOverLimitLineClosesTheConnection(t *testing.T) {
	srv := startServer(t, ipc.Config{AppName: "danktest", APIVersion: 1, MaxLineSize: 1 << 20}, echoLength)

	conn, err := net.Dial("unix", srv.SocketPath())
	require.NoError(t, err)
	defer conn.Close()
	r := bufio.NewReader(conn)
	_, err = r.ReadBytes('\n')
	require.NoError(t, err)

	go func() {
		payload := `{"id":1,"method":"echo","params":{"data":"` + strings.Repeat("x", 2<<20) + `"}}` + "\n"
		_, _ = conn.Write([]byte(payload))
	}()

	line, err := r.ReadBytes('\n')
	assert.Error(t, err, "the server closes the connection instead of answering")
	assert.Empty(t, line)
}

func TestHandlerPanicAnswersWithAnErrorAndKeepsServing(t *testing.T) {
	handler := func(ctx context.Context, w *ipc.ConnWriter, req ipc.Request, sub *ipc.Subscriber) {
		switch req.Method {
		case "boom":
			panic("kaboom")
		default:
			w.Respond(req.ID, map[string]any{"ok": true})
		}
	}
	srv := startServer(t, ipc.Config{AppName: "danktest", APIVersion: 1}, handler)

	client, err := ipc.Dial(srv.SocketPath())
	require.NoError(t, err)
	defer client.Close()

	resp, err := client.Call(ipc.Request{ID: 1, Method: "boom"})
	require.NoError(t, err)
	assert.Equal(t, 1, resp.ID)
	assert.Contains(t, resp.Error, "boom")
	assert.Nil(t, resp.Result)

	resp, err = client.Call(ipc.Request{ID: 2, Method: "fine"})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"ok": true}, *resp.Result)
}

func TestPanicAfterTheAnswerSendsNoSecondAnswer(t *testing.T) {
	handler := func(ctx context.Context, w *ipc.ConnWriter, req ipc.Request, sub *ipc.Subscriber) {
		w.Respond(req.ID, map[string]any{"id": req.ID})
		if req.Method == "boom" {
			panic("kaboom")
		}
	}
	cfg := ipc.Config{AppName: "danktest", APIVersion: 1, OnSubscribe: func([]string, *ipc.Subscriber) { panic("kaboom") }}
	srv := startServer(t, cfg, handler)

	client, err := ipc.Dial(srv.SocketPath())
	require.NoError(t, err)
	defer client.Close()

	for id, method := range []string{"boom", "subscribe", "fine"} {
		resp, err := client.Call(ipc.Request{ID: id + 1, Method: method})
		require.NoError(t, err)
		assert.Equal(t, id+1, resp.ID, method)
		assert.Empty(t, resp.Error, method)
	}
}

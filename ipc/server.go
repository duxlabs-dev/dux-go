package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"

	"github.com/duxlabs-dev/dux-go/ipc/params"
	"github.com/duxlabs-dev/dux-go/log"
	"github.com/duxlabs-dev/dux-go/paths"
)

const defaultMaxLineSize = 1024 * 1024

// Long lines are collected into a slice dropped after dispatch, so one big request does not pin its buffer.
const readBufferSize = 64 * 1024

var errLineTooLong = errors.New("request line exceeds MaxLineSize")

type Handler func(ctx context.Context, w *ConnWriter, req Request, sub *Subscriber)

type Config struct {
	AppName                string
	APIVersion             int
	Capabilities           []string
	CapabilitiesFunc       func() []string // computed per connection; overrides Capabilities when set
	MaxLineSize            int
	DefaultSubscribeTopics []string
	OnSubscribe            func(topics []string, sub *Subscriber)
	SubscribeHandler       Handler // replaces the built-in subscribe/unsubscribe handling when set
	Bus                    *EventBus
}

type Server struct {
	cfg      Config
	app      paths.App
	handler  Handler
	listener net.Listener
	socket   string
	bus      *EventBus

	mu      sync.Mutex
	stopped bool
}

func NewServer(cfg Config, handler Handler) *Server {
	if cfg.MaxLineSize <= 0 {
		cfg.MaxLineSize = defaultMaxLineSize
	}
	bus := cfg.Bus
	if bus == nil {
		bus = NewEventBus()
	}
	return &Server{
		cfg:     cfg,
		app:     paths.New(cfg.AppName),
		handler: handler,
		bus:     bus,
	}
}

func (s *Server) Listen() error {
	cleanupStaleSockets(s.app)

	socketPath := s.app.SocketPath()
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return fmt.Errorf("create socket dir: %w", err)
	}
	_ = os.Remove(socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen unix: %w", err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		listener.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}

	s.listener = listener
	s.socket = socketPath

	log.Infof("ipc listening on %s", socketPath)
	return nil
}

func (s *Server) SocketPath() string { return s.socket }

func (s *Server) Bus() *EventBus { return s.bus }

func (s *Server) Serve(ctx context.Context) error {
	if s.listener == nil {
		return errors.New("listen has not been called")
	}

	go func() {
		<-ctx.Done()
		s.Close()
	}()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			s.mu.Lock()
			stopped := s.stopped
			s.mu.Unlock()
			if stopped {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		go s.handleConnection(ctx, conn)
	}
}

func (s *Server) Run(ctx context.Context) error { return s.Serve(ctx) }

func (s *Server) Shutdown(context.Context) error { return s.Close() }

func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return nil
	}
	s.stopped = true

	if s.listener != nil {
		s.listener.Close()
	}
	if s.socket != "" {
		_ = os.Remove(s.socket)
	}
	return nil
}

func (s *Server) handleConnection(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	caps := Capabilities{
		APIVersion:   s.cfg.APIVersion,
		Capabilities: s.cfg.Capabilities,
	}
	if s.cfg.CapabilitiesFunc != nil {
		caps.Capabilities = s.cfg.CapabilitiesFunc()
	}
	encoder := json.NewEncoder(conn)
	if err := encoder.Encode(caps); err != nil {
		return
	}

	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	writer := NewConnWriter(conn)
	subscriber := s.bus.NewSubscriber(connCtx, writer)
	defer subscriber.Close()

	reader := bufio.NewReaderSize(conn, readBufferSize)
	for {
		line, err := readLine(reader, s.cfg.MaxLineSize)
		switch {
		case errors.Is(err, errLineTooLong):
			log.Warnf("ipc: closing connection: %v (%d bytes)", err, s.cfg.MaxLineSize)
			return
		case err != nil:
			return
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			_ = writer.WriteResponse(Response[any]{ID: 0, Error: "invalid json: " + err.Error()})
			continue
		}
		go s.dispatch(connCtx, writer, req, subscriber)
	}
}

// readLine returns the next line without its newline or a trailing \r, like bufio.ScanLines. A line
// longer than maxSize, newline included, is errLineTooLong. The result is only valid until the next call.
func readLine(r *bufio.Reader, maxSize int) ([]byte, error) {
	line, err := r.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		line, err = readLongLine(r, line, maxSize)
	}
	if len(line) > maxSize {
		return nil, errLineTooLong
	}
	switch {
	case err == nil:
		return dropCR(line[:len(line)-1]), nil
	case errors.Is(err, io.EOF) && len(line) > 0:
		return dropCR(line), nil
	}
	return nil, err
}

func readLongLine(r *bufio.Reader, first []byte, maxSize int) ([]byte, error) {
	line := append(make([]byte, 0, 2*len(first)), first...)
	for len(line) <= maxSize {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
	return line, nil
}

func dropCR(line []byte) []byte {
	if len(line) > 0 && line[len(line)-1] == '\r' {
		return line[:len(line)-1]
	}
	return line
}

func (s *Server) dispatch(ctx context.Context, w *ConnWriter, req Request, sub *Subscriber) {
	w.begin(req.ID)
	defer w.end(req.ID)
	defer recoverHandler(w, req)
	switch req.Method {
	case "ping":
		w.Respond(req.ID, map[string]any{"pong": true})
	case "subscribe":
		if s.cfg.SubscribeHandler != nil {
			s.cfg.SubscribeHandler(ctx, w, req, sub)
			return
		}
		s.handleSubscribe(w, req, sub)
	case "unsubscribe":
		if s.cfg.SubscribeHandler != nil {
			s.cfg.SubscribeHandler(ctx, w, req, sub)
			return
		}
		handleUnsubscribe(w, req, sub)
	default:
		if s.handler == nil {
			w.RespondError(req.ID, "unknown method: "+req.Method)
			return
		}
		s.handler(ctx, w, req, sub)
	}
}

// recoverHandler keeps a panicking handler from taking the whole daemon down with it.
func recoverHandler(w *ConnWriter, req Request) {
	r := recover()
	if r == nil {
		return
	}
	log.Errorf("ipc handler panic: method=%s id=%d: %v\n%s", req.Method, req.ID, r, debug.Stack())
	// A second answer to the same request would be read as the answer to the next one.
	if w.answered(req.ID) {
		return
	}
	w.RespondError(req.ID, "internal error handling "+req.Method)
}

func (s *Server) handleSubscribe(w *ConnWriter, req Request, sub *Subscriber) {
	topics := params.StringSlice(req.Params, "topics")
	if len(topics) == 0 {
		topics = s.cfg.DefaultSubscribeTopics
	}
	sub.Subscribe(topics...)
	w.Respond(req.ID, map[string]any{"topics": sub.Topics()})

	if s.cfg.OnSubscribe == nil {
		return
	}
	s.cfg.OnSubscribe(topics, sub)
}

func handleUnsubscribe(w *ConnWriter, req Request, sub *Subscriber) {
	topics := params.StringSlice(req.Params, "topics")
	if len(topics) == 0 {
		w.Respond(req.ID, map[string]any{"topics": sub.Topics()})
		return
	}
	sub.Unsubscribe(topics...)
	w.Respond(req.ID, map[string]any{"topics": sub.Topics()})
}

func cleanupStaleSockets(app paths.App) {
	dir := app.SocketDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	prefix := app.Name + "-"
	for _, entry := range entries {
		switch {
		case !strings.HasPrefix(entry.Name(), prefix):
			continue
		case !strings.HasSuffix(entry.Name(), ".sock"):
			continue
		}

		pidStr := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), prefix), ".sock")
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			continue
		}

		if !processAlive(pid) {
			path := filepath.Join(dir, entry.Name())
			_ = os.Remove(path)
			log.Debugf("removed stale socket %s", path)
		}
	}
}

func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := proc.Signal(syscallSignalZero); err != nil {
		return false
	}
	return true
}

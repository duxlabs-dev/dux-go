package ipc

import (
	"encoding/json"
	"net"
	"sync"
	"time"

	"github.com/duxlabs-dev/dux-go/log"
)

// A client that has not drained its socket for this long is dead; disconnect
// it rather than let it block publishers.
const writeTimeout = 30 * time.Second

type ConnWriter struct {
	mu   sync.Mutex
	conn net.Conn

	// Requests being dispatched, and whether each has been answered through Respond or RespondError.
	reqMu    sync.Mutex
	inFlight map[int]bool
}

func (w *ConnWriter) begin(id int) {
	w.reqMu.Lock()
	defer w.reqMu.Unlock()
	if w.inFlight == nil {
		w.inFlight = make(map[int]bool)
	}
	w.inFlight[id] = false
}

func (w *ConnWriter) end(id int) {
	w.reqMu.Lock()
	defer w.reqMu.Unlock()
	delete(w.inFlight, id)
}

func (w *ConnWriter) markAnswered(id int) {
	w.reqMu.Lock()
	defer w.reqMu.Unlock()
	if _, ok := w.inFlight[id]; ok {
		w.inFlight[id] = true
	}
}

func (w *ConnWriter) answered(id int) bool {
	w.reqMu.Lock()
	defer w.reqMu.Unlock()
	return w.inFlight[id]
}

func NewConnWriter(conn net.Conn) *ConnWriter {
	return &ConnWriter{conn: conn}
}

func (w *ConnWriter) WriteResponse(resp any) error {
	data, err := json.Marshal(resp)
	if err != nil {
		log.Warnf("ipc encode response: %v", err)
		return err
	}
	return w.write(append(data, '\n'))
}

func (w *ConnWriter) WriteEvent(ev Event) error {
	envelope := map[string]any{
		"event": ev.Topic,
		"data":  ev.Data,
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		log.Warnf("ipc encode event: %v", err)
		return err
	}
	return w.write(append(data, '\n'))
}

func (w *ConnWriter) write(data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, err := w.conn.Write(data)
	_ = w.conn.SetWriteDeadline(time.Time{})
	if err == nil {
		return nil
	}
	log.Debugf("ipc write: %v", err)
	w.conn.Close()
	return err
}

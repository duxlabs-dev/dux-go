package ipc

import (
	"github.com/duxlabs-dev/dux-go/log"
)

type Capabilities struct {
	APIVersion   int      `json:"apiVersion"`
	Capabilities []string `json:"capabilities"`
}

type Request struct {
	ID     int            `json:"id,omitempty"`
	Method string         `json:"method"`
	Params map[string]any `json:"params,omitempty"`
}

type Response[T any] struct {
	ID     int    `json:"id,omitempty"`
	Result *T     `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

func (r Request) Get[T any](key string) (T, bool) {
	v, ok := r.Params[key].(T)
	return v, ok
}

func (r Request) GetOr[T any](key string, def T) T {
	if v, ok := r.Params[key].(T); ok {
		return v
	}
	return def
}

func (w *ConnWriter) RespondError(id int, msg string) {
	log.Errorf("ipc error: id=%d method-error=%s", id, msg)
	w.markAnswered(id)
	_ = w.WriteResponse(Response[any]{ID: id, Error: msg})
}

func (w *ConnWriter) Respond[T any](id int, result T) {
	w.markAnswered(id)
	_ = w.WriteResponse(Response[T]{ID: id, Result: &result})
}

package weather

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

const (
	commandTimeout = providerTimeout
	stderrTail     = 512
)

type commandQuery struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// Command is a Fetcher that runs name per lookup, with the location as JSON on stdin and
// an Open-Meteo forecast on stdout. The README has the format.
func Command(name string, args ...string) Fetcher {
	return func(ctx context.Context, req Request) (*Forecast, error) {
		query, err := json.Marshal(commandQuery{Latitude: req.Latitude, Longitude: req.Longitude})
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(ctx, commandTimeout)
		defer cancel()

		started := time.Now()
		var stdout boundedBuffer
		var stderr tailBuffer
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Stdin = bytes.NewReader(query)
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
		cmd.WaitDelay = time.Second
		err = cmd.Run()
		deadline, _ := ctx.Deadline()
		switch {
		case err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
			return nil, fmt.Errorf("timed out after %s: %w", deadline.Sub(started).Round(time.Millisecond), ctx.Err())
		case err != nil && ctx.Err() != nil:
			return nil, ctx.Err()
		case stdout.overflowed:
			return nil, ErrTooLarge
		case err != nil && len(bytes.TrimSpace(stderr)) > 0:
			return nil, fmt.Errorf("%w: %s", err, bytes.TrimSpace(stderr))
		case err != nil:
			return nil, err
		}
		return decodeCommand(stdout.data)
	}
}

func decodeCommand(output []byte) (*Forecast, error) {
	if len(bytes.TrimSpace(output)) == 0 {
		return nil, errors.New("no forecast")
	}
	var forecast Forecast
	if err := decodeJSON(output, &forecast); err != nil {
		return nil, err
	}
	return &forecast, nil
}

// Not a bytes.Buffer: io.Copy would call its ReadFrom and never hit the cap in Write.
type boundedBuffer struct {
	data       []byte
	overflowed bool
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	if len(b.data)+len(data) > MaxResponseBytes {
		b.overflowed = true
		return 0, ErrTooLarge
	}
	b.data = append(b.data, data...)
	return len(data), nil
}

// tailBuffer keeps the last stderrTail bytes, where a failing program says why.
type tailBuffer []byte

func (t *tailBuffer) Write(data []byte) (int, error) {
	*t = append(*t, data...)
	if len(*t) > stderrTail {
		*t = append((*t)[:0], (*t)[len(*t)-stderrTail:]...)
	}
	return len(data), nil
}

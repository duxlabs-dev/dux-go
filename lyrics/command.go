package lyrics

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

const commandTimeout = 8 * time.Second

var commandFormats = map[string]Parser{
	"lrc":        ParseLRC,
	"ttml":       ParseTTML,
	"lyricsfile": ParseLyricsfile,
}

type commandQuery struct {
	Title    string `json:"title"`
	Artist   string `json:"artist"`
	Album    string `json:"album,omitempty"`
	Duration int    `json:"duration,omitempty"`
}

type commandAnswer struct {
	Format       string `json:"format"`
	Lyrics       string `json:"lyrics"`
	Instrumental bool   `json:"instrumental"`
}

// Command is a Fetcher that runs name per lookup, with the track as JSON on stdin and the
// answer on stdout. The README has the format.
func Command(name string, args ...string) Fetcher {
	return func(ctx context.Context, req Request) (*Lyrics, error) {
		query, err := json.Marshal(commandQuery{Title: req.Title, Artist: req.Artist, Album: req.Album, Duration: req.seconds()})
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(ctx, commandTimeout)
		defer cancel()

		started := time.Now()
		var stdout boundedBuffer
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Stdin = bytes.NewReader(query)
		cmd.Stdout = &stdout
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
		case err != nil:
			return nil, err
		}
		return decodeCommand(stdout.data)
	}
}

func decodeCommand(output []byte) (*Lyrics, error) {
	if len(bytes.TrimSpace(output)) == 0 {
		return &Lyrics{}, nil
	}
	var answer commandAnswer
	if err := decodeJSON(output, &answer); err != nil {
		return nil, err
	}
	if answer.Instrumental {
		return &Lyrics{Instrumental: true}, nil
	}
	if answer.Lyrics == "" {
		return &Lyrics{}, nil
	}
	parse, ok := commandFormats[answer.Format]
	if !ok {
		return nil, fmt.Errorf("unknown lyrics format %q", answer.Format)
	}
	return parse([]byte(answer.Lyrics))
}

// Not a bytes.Buffer: io.Copy would call its ReadFrom and never hit the cap in Write.
type boundedBuffer struct {
	data       []byte
	overflowed bool
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	if len(b.data)+len(data) > MaxSourceBytes {
		b.overflowed = true
		return 0, ErrTooLarge
	}
	b.data = append(b.data, data...)
	return len(data), nil
}

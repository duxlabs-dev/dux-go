package ipc

import (
	"bufio"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadLine(t *testing.T) {
	long := strings.Repeat("y", 40)
	r := bufio.NewReaderSize(strings.NewReader("a\r\n"+long+"\nb\n"), 16)

	for _, want := range []string{"a", long, "b"} {
		line, err := readLine(r, 50)
		require.NoError(t, err)
		assert.Equal(t, want, string(line))
	}
	assert.Equal(t, 16, r.Size(), "the reader keeps its small buffer after a long line")
}

func TestReadLineLimitCountsTheNewline(t *testing.T) {
	r := bufio.NewReaderSize(strings.NewReader("123456789\n1234567890\n"), 16)
	line, err := readLine(r, 10)
	require.NoError(t, err)
	assert.Equal(t, "123456789", string(line))
	_, err = readLine(r, 10)
	assert.ErrorIs(t, err, errLineTooLong)
}

func TestReadLineReturnsALastLineWithoutNewline(t *testing.T) {
	r := bufio.NewReaderSize(strings.NewReader("tail"), 16)
	line, err := readLine(r, 50)
	require.NoError(t, err)
	assert.Equal(t, "tail", string(line))
	_, err = readLine(r, 50)
	assert.ErrorIs(t, err, io.EOF)
}

func TestReadLineStopsEarlyOnAnOversizedLine(t *testing.T) {
	src := strings.NewReader(strings.Repeat("z", 1000) + "\n")
	_, err := readLine(bufio.NewReaderSize(src, 16), 100)
	assert.ErrorIs(t, err, errLineTooLong)
	assert.Positive(t, src.Len(), "the rest of an oversized line is never read into memory")
}

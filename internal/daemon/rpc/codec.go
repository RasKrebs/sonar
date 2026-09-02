package rpc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// ErrOversize is returned by Decoder.Next when a single message exceeds the
// decoder's size limit. The offending line is drained, so the decoder resyncs
// on the next message rather than corrupting the stream.
var ErrOversize = errors.New("rpc: message too large")

// readChunk bounds how much ReadSlice returns per call; it is unrelated to the
// message size limit, which is enforced by accumulation.
const readChunk = 64 << 10

// Encoder writes newline-delimited JSON messages. It is safe for concurrent
// use: the daemon writes responses and broadcast notifications from different
// goroutines onto one connection.
type Encoder struct {
	mu  sync.Mutex
	enc *json.Encoder
}

// NewEncoder returns an Encoder writing to w.
func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{enc: json.NewEncoder(w)}
}

// Encode writes one message followed by a newline.
func (e *Encoder) Encode(v any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.enc.Encode(v)
}

// Decoder reads newline-delimited JSON messages with a per-message size limit.
type Decoder struct {
	r   *bufio.Reader
	max int
}

// NewDecoder returns a Decoder reading from r, rejecting any single message
// longer than max bytes.
func NewDecoder(r io.Reader, max int) *Decoder {
	if max <= 0 {
		max = 4 << 20
	}
	return &Decoder{r: bufio.NewReaderSize(r, readChunk), max: max}
}

// Next reads the next message. It returns io.EOF at end of stream, ErrOversize
// (wrapped) for a message over the limit, and an *Error with CodeInvalidParams
// for malformed JSON.
func (d *Decoder) Next() (Message, error) {
	for {
		line, err := d.readLine()
		if err != nil {
			return Message{}, err
		}
		if len(line) == 0 {
			continue // tolerate blank keepalive lines
		}
		var m Message
		if err := json.Unmarshal(line, &m); err != nil {
			return Message{}, NewError(CodeInvalidParams,
				fmt.Sprintf("malformed JSON message: %v", err),
				"messages must be single-line JSON-RPC 2.0 objects")
		}
		return m, nil
	}
}

// readLine returns one newline-terminated line with the terminator stripped,
// enforcing the size limit and draining an oversize line so the stream resyncs.
func (d *Decoder) readLine() ([]byte, error) {
	var buf []byte
	over := false
	total := 0

	for {
		chunk, err := d.r.ReadSlice('\n')
		total += len(chunk)
		if total > d.max {
			over = true
			buf = nil // stop retaining an oversize payload
		} else if !over {
			buf = append(buf, chunk...)
		}

		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if total == 0 {
				return nil, io.EOF
			}
			if over {
				return nil, fmt.Errorf("%w: %d bytes exceeds limit %d", ErrOversize, total, d.max)
			}
			return trimEOL(buf), nil
		case err != nil:
			return nil, err
		}

		if over {
			return nil, fmt.Errorf("%w: %d bytes exceeds limit %d", ErrOversize, total, d.max)
		}
		return trimEOL(buf), nil
	}
}

// trimEOL strips a trailing \n and an optional preceding \r.
func trimEOL(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	if n := len(b); n > 0 && b[n-1] == '\r' {
		b = b[:n-1]
	}
	return b
}

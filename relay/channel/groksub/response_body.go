package groksub

import (
	"bufio"
	"bytes"
	"io"
	"strings"

	"github.com/QuantumNous/new-api/relay/helper"
)

// grokResponseBody normalizes upstream usage before either the native Responses
// handler or a protocol bridge consumes it. Close interrupts the original body.
type grokResponseBody struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
	pending []byte
	err     error
	ping    bool
	loaded  bool
}

func newGrokResponseBody(body io.ReadCloser, stream bool) io.ReadCloser {
	b := &grokResponseBody{body: body}
	if stream {
		b.scanner = helper.NewStreamScanner(body)
	}
	return b
}

func (b *grokResponseBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(b.pending) == 0 {
		if b.err != nil {
			return 0, b.err
		}
		if b.scanner == nil {
			if b.loaded {
				return 0, io.EOF
			}
			b.loaded = true
			b.pending, b.err = io.ReadAll(b.body)
			if b.err == nil {
				b.pending, b.err = normalizeGrokUsage(b.pending, "usage")
			}
			continue
		}
		if !b.scanner.Scan() {
			b.err = b.scanner.Err()
			if b.err == nil {
				b.err = io.EOF
			}
			continue
		}
		line := b.scanner.Bytes()
		if len(line) == 0 {
			b.ping = false
		} else if event, ok := strings.CutPrefix(string(line), "event:"); ok {
			b.ping = strings.TrimSpace(event) == "ping"
		}
		if b.ping {
			continue
		}
		if data, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			data, err := normalizeGrokUsage(bytes.TrimSpace(data), "response.usage")
			if err != nil {
				b.err = err
				continue
			}
			b.pending = append([]byte("data: "), data...)
		} else {
			b.pending = append(b.pending, line...)
		}
		b.pending = append(b.pending, '\n')
	}
	n := copy(p, b.pending)
	b.pending = b.pending[n:]
	return n, nil
}

func (b *grokResponseBody) Close() error { return b.body.Close() }

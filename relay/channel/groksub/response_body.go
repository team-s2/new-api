package groksub

import (
	"bufio"
	"bytes"
	"io"
	"strings"

	"github.com/QuantumNous/new-api/relay/helper"
)

// grokResponseBody normalizes the upstream body before the shared Responses
// handlers consume it:
//   - replaces the gateway's `event: ping` billing/keepalive frames with an SSE
//     comment (strict Responses clients abort on unknown event types, while a
//     line still has to reach the scanner to keep its idle timer alive);
//   - folds xAI's separately reported reasoning tokens into output_tokens so
//     client usage and settlement see OpenAI-consistent counts;
//   - restores client tool shapes lowered by adaptClientTools.
//
// Stream output is re-emitted as bare `data:` frames; the shared handlers
// derive the `event:` line from each payload's type.
type grokResponseBody struct {
	body     io.ReadCloser
	scanner  *bufio.Scanner
	mapping  *clientToolMapping
	restorer *streamEventRestorer
	pending  []byte
	err      error
	inPing   bool
	loaded   bool
}

func newGrokResponseBody(body io.ReadCloser, stream bool, mapping *clientToolMapping) io.ReadCloser {
	b := &grokResponseBody{body: body, mapping: mapping}
	if stream {
		b.scanner = helper.NewStreamScanner(body)
		if !mapping.empty() {
			b.restorer = newStreamEventRestorer(mapping)
		}
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
			b.loadNonStream()
			continue
		}
		b.scanLine()
	}
	n := copy(p, b.pending)
	b.pending = b.pending[n:]
	return n, nil
}

func (b *grokResponseBody) loadNonStream() {
	if b.loaded {
		b.err = io.EOF
		return
	}
	b.loaded = true
	body, err := io.ReadAll(b.body)
	if err != nil {
		b.err = err
		return
	}
	body = normalizeGrokUsage(body, "usage")
	if restored, changed := restoreClientToolPayload(body, b.mapping); changed {
		body = restored
	}
	b.pending = body
}

func (b *grokResponseBody) scanLine() {
	if !b.scanner.Scan() {
		b.err = b.scanner.Err()
		if b.err == nil {
			b.err = io.EOF
		}
		return
	}
	line := bytes.TrimSpace(b.scanner.Bytes())
	if len(line) == 0 {
		b.inPing = false
		return
	}
	if event, ok := strings.CutPrefix(string(line), "event:"); ok {
		b.inPing = strings.TrimSpace(event) == "ping"
		if b.inPing {
			b.pending = append(b.pending, ": ping\n\n"...)
		}
		return
	}
	data, ok := bytes.CutPrefix(line, []byte("data:"))
	if !ok || b.inPing {
		return
	}
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("[DONE]")) {
		b.pending = append(b.pending, "data: [DONE]\n\n"...)
		return
	}
	data = normalizeGrokUsage(data, "response.usage")
	payloads := [][]byte{data}
	if b.restorer != nil {
		payloads = b.restorer.restore(data)
	}
	for _, payload := range payloads {
		b.pending = append(b.pending, "data: "...)
		b.pending = append(b.pending, payload...)
		b.pending = append(b.pending, "\n\n"...)
	}
}

func (b *grokResponseBody) Close() error { return b.body.Close() }

package groksub

import (
	"bufio"
	"io"
	"strings"
)

// maxPingFrameLines bounds how many lines of a candidate ping frame we consume
// while looking for its terminator. Real ping frames are two lines; anything
// longer is passed through rather than buffered.
const maxPingFrameLines = 16

// grokPingFilterBody removes the CLI gateway's billing ping SSE frames
// ("event: ping" + data). Strict Responses clients treat event types as a
// closed enum and abort the stream on unknown events, and the host scanner
// would forward a ping data payload as a regular event. Dropping the frame
// entirely is the safest rewrite: every SSE parser ignores nothing at all, and
// the remaining frames pass through byte-for-byte.
type grokPingFilterBody struct {
	reader *bufio.Reader
}

func newGrokPingFilterBody(body io.ReadCloser) io.ReadCloser {
	if body == nil {
		return nil
	}
	return &grokPingFilterBody{reader: bufio.NewReaderSize(body, 64<<10)}
}

func (b *grokPingFilterBody) Read(p []byte) (int, error) {
	for {
		line, err := b.reader.ReadString('\n')
		if line != "" {
			if strings.HasPrefix(line, "event: ping") {
				dropPingFrame(b.reader)
				continue
			}
			n := copy(p, line)
			return n, nil
		}
		if err != nil {
			return 0, err
		}
	}
}

// dropPingFrame consumes the remainder of an "event: ping" frame through its
// terminating blank line without emitting anything.
func dropPingFrame(reader *bufio.Reader) {
	for range maxPingFrameLines {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		if strings.TrimRight(line, "\r\n") == "" {
			return
		}
	}
}

func (b *grokPingFilterBody) Close() error { return nil }

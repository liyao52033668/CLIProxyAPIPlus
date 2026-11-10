package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
)

type StreamForwardOptions struct {
	// KeepAliveInterval overrides the configured streaming keep-alive interval.
	// If nil, the configured default is used. If set to <= 0, keep-alives are disabled.
	KeepAliveInterval *time.Duration

	// WriteChunk writes a single data chunk to the response body. It should not flush.
	WriteChunk func(chunk []byte)

	// WriteTerminalError writes an error payload to the response body when streaming fails
	// after headers have already been committed. It should not flush.
	WriteTerminalError func(errMsg *interfaces.ErrorMessage)

	// CloseError optionally validates a clean upstream channel close before WriteDone.
	// Returning an error surfaces it through WriteTerminalError instead of completing the stream.
	CloseError func() *interfaces.ErrorMessage

	// WriteDone optionally writes a terminal marker when the upstream data channel closes
	// without an error (e.g. OpenAI's `[DONE]`). It should not flush.
	WriteDone func()

	// WriteKeepAlive optionally writes a keep-alive heartbeat. It should not flush.
	// When nil, a standard SSE comment heartbeat is used.
	WriteKeepAlive func()

	// InitialTrailingNewlines seeds the SSE boundary tracker when the caller has
	// already written bytes before handing the stream to ForwardStream. Zero is
	// treated as "at a clean boundary" (2).
	InitialTrailingNewlines int
}

func (h *BaseAPIHandler) ForwardStream(c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage, opts StreamForwardOptions) {
	if c == nil {
		return
	}
	if cancel == nil {
		return
	}

	writeChunk := opts.WriteChunk
	if writeChunk == nil {
		writeChunk = func([]byte) {}
	}

	writeKeepAlive := opts.WriteKeepAlive
	if writeKeepAlive == nil {
		writeKeepAlive = func() {
			_, _ = c.Writer.Write([]byte(": keep-alive\n\n"))
		}
	}

	keepAliveInterval := StreamingKeepAliveInterval(h.CurrentConfig())
	if opts.KeepAliveInterval != nil {
		keepAliveInterval = *opts.KeepAliveInterval
	}
	var keepAliveC <-chan time.Time
	stopKeepAlive := func() {}
	resetKeepAlive := func() {}
	if keepAliveInterval > 0 {
		keepAliveC, stopKeepAlive, resetKeepAlive = NewStreamingKeepAliveTickerWithReset(keepAliveInterval)
	}
	defer stopKeepAlive()

	// trailingNewlines tracks how far the downstream stream is from a clean SSE
	// event boundary (two newlines). A keep-alive comment written inside a
	// partially delivered event corrupts the client parser, so heartbeats are
	// suppressed until the stream is back at a boundary.
	trailingNewlines := opts.InitialTrailingNewlines
	if trailingNewlines <= 0 {
		trailingNewlines = 2
	}

	var terminalErr *interfaces.ErrorMessage
	writeTerminalError := func(errMsg *interfaces.ErrorMessage) {
		if opts.WriteTerminalError == nil {
			return
		}
		trailingNewlines = ensureSSEEventBoundary(c.Writer, trailingNewlines)
		opts.WriteTerminalError(errMsg)
	}

	handleChunk := func(chunk []byte, ok bool) bool {
		if !ok {
			// Prefer surfacing a terminal error if one is pending.
			if terminalErr == nil {
				select {
				case errMsg, ok := <-errs:
					if ok && errMsg != nil {
						terminalErr = errMsg
					}
				default:
				}
			}
			if terminalErr == nil && opts.CloseError != nil {
				terminalErr = opts.CloseError()
			}
			if terminalErr != nil {
				writeTerminalError(terminalErr)
				flusher.Flush()
				cancel(terminalErr.Error)
				return true
			}
			if opts.WriteDone != nil {
				opts.WriteDone()
			}
			flusher.Flush()
			cancel(nil)
			return true
		}
		beforeSize := c.Writer.Size()
		writeChunk(chunk)
		trailingNewlines = AdvanceSSENewlineCount(trailingNewlines, chunk)
		if c.Writer.Size() != beforeSize {
			// Real bytes restarted the idle clock, so a heartbeat is not due yet.
			resetKeepAlive()
		}
		flusher.Flush()
		return false
	}

	for {
		select {
		case <-c.Request.Context().Done():
			cancel(c.Request.Context().Err())
			return
		case chunk, ok := <-data:
			if handleChunk(chunk, ok) {
				return
			}
		case errMsg, ok := <-errs:
			if !ok {
				continue
			}
			if errMsg != nil {
				terminalErr = errMsg
				if opts.WriteTerminalError != nil {
					writeTerminalError(errMsg)
					flusher.Flush()
				}
			}
			var execErr error
			if errMsg != nil {
				execErr = errMsg.Error
			}
			cancel(execErr)
			return
		case <-keepAliveC:
			if trailingNewlines < 2 {
				// Mid-event: emitting now would split the event the client is parsing.
				continue
			}
			writeKeepAlive()
			flusher.Flush()
		}
	}
}

// AdvanceSSENewlineCount tracks the number of consecutive newlines at the end of
// the streamed bytes, capped at 2 (an SSE event boundary). Carriage returns are
// ignored so CRLF and LF framing are treated identically.
func AdvanceSSENewlineCount(prev int, chunk []byte) int {
	count := prev
	for _, b := range chunk {
		switch b {
		case '\r':
			continue
		case '\n':
			if count < 2 {
				count++
			}
		default:
			count = 0
		}
	}
	return count
}

// ensureSSEEventBoundary pads the stream with the newlines needed to start a new
// SSE event, returning the resulting trailing-newline count (always 2).
func ensureSSEEventBoundary(w http.ResponseWriter, trailingNewlines int) int {
	if w == nil {
		return 2
	}
	switch trailingNewlines {
	case 0:
		_, _ = w.Write([]byte("\n\n"))
	case 1:
		_, _ = w.Write([]byte("\n"))
	}
	return 2
}

package proto

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

const (
	defaultInitialWindowSize = 65535 // HTTP/2 default
	maxFramePayload          = 16384 // HTTP/2 default max frame size
	// connectHandshakeTimeout bounds only the HTTP CONNECT tunnel setup with an
	// egress proxy, before the upstream TLS connection exists. Zero afterwards.
	connectHandshakeTimeout = 30 * time.Second
)

// H2Conn is a TLS+HTTP/2 connection that has completed the client preface and
// SETTINGS handshake but carries no stream yet. One stream is opened per
// connection (OpenStream), mirroring Cursor's per-turn stream lifecycle; the
// executor pool caches pre-warmed H2Conns to skip the handshake RTT.
type H2Conn struct {
	framer *http2.Framer
	conn   net.Conn
	host   string

	// Server's send-window state, captured during the handshake.
	serverInitialWindowSize int32
	connWindowSize          int32

	createdAt time.Time
	openMu    sync.Mutex
	opened    bool
}

// H2Stream provides bidirectional HTTP/2 streaming for the Connect protocol.
// Go's net/http does not support full-duplex HTTP/2, so we use the low-level framer.
type H2Stream struct {
	framer   *http2.Framer
	conn     net.Conn
	streamID uint32
	mu       sync.Mutex
	id       string // unique identifier for debugging
	frameNum int64  // sequential frame counter for debugging

	dataCh chan []byte
	doneCh chan struct{}
	err    error

	// Send-side flow control
	sendWindow int32      // available bytes we can send on this stream
	connWindow int32      // available bytes on the connection level
	windowCond *sync.Cond // signaled when window is updated
	windowMu   sync.Mutex // protects sendWindow, connWindow
}

// ID returns the unique identifier for this stream (for logging).
func (s *H2Stream) ID() string { return s.id }

// FrameNum returns the current frame number for debugging.
func (s *H2Stream) FrameNum() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frameNum
}

// dialTunnel opens a TCP connection to host:443, tunneled through an HTTP
// CONNECT proxy when proxyURL is non-empty (http/https proxy schemes).
func dialTunnel(host, proxyURL string) (net.Conn, error) {
	if strings.TrimSpace(proxyURL) == "" {
		return net.Dial("tcp", host+":443")
	}
	if !strings.Contains(proxyURL, "://") {
		proxyURL = "http://" + proxyURL
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("h2: invalid proxy URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("h2: unsupported proxy scheme %q", u.Scheme)
	}
	proxyPort := u.Port()
	if proxyPort == "" {
		if u.Scheme == "https" {
			proxyPort = "443"
		} else {
			proxyPort = "80"
		}
	}
	var raw net.Conn
	if u.Scheme == "https" {
		raw, err = tls.Dial("tcp", net.JoinHostPort(u.Hostname(), proxyPort), &tls.Config{ServerName: u.Hostname()})
	} else {
		raw, err = net.Dial("tcp", net.JoinHostPort(u.Hostname(), proxyPort))
	}
	if err != nil {
		return nil, fmt.Errorf("h2: proxy dial failed: %w", err)
	}
	// Bound only the CONNECT handshake; the deadline is cleared once the
	// tunnel is established and before TLS.
	_ = raw.SetDeadline(time.Now().Add(connectHandshakeTimeout))
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Scheme: "https", Host: host},
		Host:   host,
		Header: make(http.Header),
	}
	if u.User != nil {
		pass, _ := u.User.Password()
		req.SetBasicAuth(u.User.Username(), pass)
	}
	if err := req.Write(raw); err != nil {
		raw.Close()
		return nil, fmt.Errorf("h2: proxy CONNECT write failed: %w", err)
	}
	br := bufio.NewReader(raw)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		raw.Close()
		return nil, fmt.Errorf("h2: proxy CONNECT read failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw.Close()
		return nil, fmt.Errorf("h2: proxy CONNECT failed: %s", resp.Status)
	}
	_ = raw.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		// The reader may have buffered the first bytes of the TLS stream.
		return &bufferedConn{Conn: raw, r: br}, nil
	}
	return raw, nil
}

// bufferedConn serves buffered bytes before the underlying connection, so
// TLS can read a response the CONNECT reader already pulled in.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// DialH2Conn establishes a TLS+HTTP/2 connection (optionally through an HTTP
// CONNECT proxy) and completes the client preface + SETTINGS handshake.
func DialH2Conn(host, proxyURL string) (*H2Conn, error) {
	raw, err := dialTunnel(host, proxyURL)
	if err != nil {
		return nil, err
	}
	tlsConn := tls.Client(raw, &tls.Config{
		ServerName: host,
		NextProtos: []string{"h2"},
	})
	if err := tlsConn.Handshake(); err != nil {
		raw.Close()
		return nil, fmt.Errorf("h2: TLS handshake failed: %w", err)
	}
	if tlsConn.ConnectionState().NegotiatedProtocol != "h2" {
		tlsConn.Close()
		return nil, fmt.Errorf("h2: server did not negotiate h2")
	}

	framer := http2.NewFramer(tlsConn, tlsConn)

	// Client connection preface
	if _, err := tlsConn.Write([]byte(http2.ClientPreface)); err != nil {
		tlsConn.Close()
		return nil, fmt.Errorf("h2: preface write failed: %w", err)
	}

	// Send initial SETTINGS (tell server how much WE can receive)
	if err := framer.WriteSettings(
		http2.Setting{ID: http2.SettingInitialWindowSize, Val: 4 * 1024 * 1024},
		http2.Setting{ID: http2.SettingMaxConcurrentStreams, Val: 100},
	); err != nil {
		tlsConn.Close()
		return nil, fmt.Errorf("h2: settings write failed: %w", err)
	}

	// Connection-level window update (for receiving)
	if err := framer.WriteWindowUpdate(0, 3*1024*1024); err != nil {
		tlsConn.Close()
		return nil, fmt.Errorf("h2: window update failed: %w", err)
	}

	// Read and handle initial server frames (SETTINGS, WINDOW_UPDATE)
	// Track server's initial window size (how much WE can send)
	serverInitialWindowSize := int32(defaultInitialWindowSize)
	connWindowSize := int32(defaultInitialWindowSize) // connection-level send window
	for range 10 {
		f, err := framer.ReadFrame()
		if err != nil {
			tlsConn.Close()
			return nil, fmt.Errorf("h2: initial frame read failed: %w", err)
		}
		switch sf := f.(type) {
		case *http2.SettingsFrame:
			if !sf.IsAck() {
				sf.ForeachSetting(func(s http2.Setting) error {
					if s.ID == http2.SettingInitialWindowSize {
						serverInitialWindowSize = int32(s.Val)
						log.Debugf("h2: server initial window size: %d", s.Val)
					}
					return nil
				})
				framer.WriteSettingsAck()
			} else {
				goto handshakeDone
			}
		case *http2.WindowUpdateFrame:
			if sf.StreamID == 0 {
				connWindowSize += int32(sf.Increment)
				log.Debugf("h2: initial conn window update: +%d, total=%d", sf.Increment, connWindowSize)
			}
		default:
			// unexpected but continue
		}
	}
handshakeDone:

	return &H2Conn{
		framer:                  framer,
		conn:                    tlsConn,
		host:                    host,
		serverInitialWindowSize: serverInitialWindowSize,
		connWindowSize:          connWindowSize,
		createdAt:               time.Now(),
	}, nil
}

// OpenStream writes the request HEADERS on the connection's single stream and
// starts the frame read loop. It returns an error if a stream was already
// opened on this connection.
func (c *H2Conn) OpenStream(headers map[string]string) (*H2Stream, error) {
	c.openMu.Lock()
	defer c.openMu.Unlock()
	if c.opened {
		return nil, fmt.Errorf("h2: stream already opened on this connection")
	}
	c.opened = true

	streamID := uint32(1)
	var hdrBuf []byte
	enc := hpack.NewEncoder(&sliceWriter{buf: &hdrBuf})
	enc.WriteField(hpack.HeaderField{Name: ":method", Value: "POST"})
	enc.WriteField(hpack.HeaderField{Name: ":scheme", Value: "https"})
	enc.WriteField(hpack.HeaderField{Name: ":authority", Value: c.host})
	if p, ok := headers[":path"]; ok {
		enc.WriteField(hpack.HeaderField{Name: ":path", Value: p})
	}
	for k, v := range headers {
		if len(k) > 0 && k[0] == ':' {
			continue
		}
		enc.WriteField(hpack.HeaderField{Name: k, Value: v})
	}

	if err := c.framer.WriteHeaders(http2.HeadersFrameParam{
		StreamID:      streamID,
		BlockFragment: hdrBuf,
		EndStream:     false,
		EndHeaders:    true,
	}); err != nil {
		return nil, fmt.Errorf("h2: headers write failed: %w", err)
	}

	s := &H2Stream{
		framer:     c.framer,
		conn:       c.conn,
		streamID:   streamID,
		dataCh:     make(chan []byte, 256),
		doneCh:     make(chan struct{}),
		id:         fmt.Sprintf("%d-%s", streamID, time.Now().Format("150405.000")),
		frameNum:   0,
		sendWindow: c.serverInitialWindowSize,
		connWindow: c.connWindowSize,
	}
	s.windowCond = sync.NewCond(&s.windowMu)
	go s.readLoop()
	return s, nil
}

// Close tears the connection down.
func (c *H2Conn) Close() {
	c.conn.Close()
}

// CreatedAt reports when the connection finished its handshake; the pool uses
// it to expire idle connections.
func (c *H2Conn) CreatedAt() time.Time { return c.createdAt }

// DialH2Stream establishes a TLS+HTTP/2 connection (optionally through an
// HTTP CONNECT proxy), opens a stream and writes the request HEADERS.
func DialH2Stream(host, proxyURL string, headers map[string]string) (*H2Stream, error) {
	conn, err := DialH2Conn(host, proxyURL)
	if err != nil {
		return nil, err
	}
	stream, err := conn.OpenStream(headers)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return stream, nil
}

// Write sends a DATA frame on the stream, respecting flow control.
func (s *H2Stream) Write(data []byte) error {
	for len(data) > 0 {
		chunk := data
		if len(chunk) > maxFramePayload {
			chunk = data[:maxFramePayload]
		}

		// Wait for flow control window
		s.windowMu.Lock()
		for s.sendWindow <= 0 || s.connWindow <= 0 {
			s.windowCond.Wait()
		}
		// Limit chunk to available window
		allowed := min(int(s.connWindow), int(s.sendWindow))
		if len(chunk) > allowed {
			chunk = chunk[:allowed]
		}
		s.sendWindow -= int32(len(chunk))
		s.connWindow -= int32(len(chunk))
		s.windowMu.Unlock()

		s.mu.Lock()
		err := s.framer.WriteData(s.streamID, false, chunk)
		s.mu.Unlock()
		if err != nil {
			return err
		}
		data = data[len(chunk):]
	}
	return nil
}

// Data returns the channel of received data chunks.
func (s *H2Stream) Data() <-chan []byte { return s.dataCh }

// Done returns a channel closed when the stream ends.
func (s *H2Stream) Done() <-chan struct{} { return s.doneCh }

// Err returns the error (if any) that caused the stream to close.
// Returns nil for a clean shutdown (EOF / StreamEnded).
func (s *H2Stream) Err() error { return s.err }

// Close tears down the connection.
func (s *H2Stream) Close() {
	s.conn.Close()
	// Unblock any writers waiting on flow control
	s.windowCond.Broadcast()
}

func (s *H2Stream) readLoop() {
	defer close(s.doneCh)
	defer close(s.dataCh)

	for {
		f, err := s.framer.ReadFrame()
		if err != nil {
			if err != io.EOF {
				s.err = err
				log.Debugf("h2stream[%s]: readLoop error: %v", s.id, err)
			}
			return
		}

		// Increment frame counter
		s.mu.Lock()
		s.frameNum++
		s.mu.Unlock()

		switch frame := f.(type) {
		case *http2.DataFrame:
			if frame.StreamID == s.streamID && len(frame.Data()) > 0 {
				cp := make([]byte, len(frame.Data()))
				copy(cp, frame.Data())
				s.dataCh <- cp

				// Flow control: send WINDOW_UPDATE for received data
				s.mu.Lock()
				s.framer.WriteWindowUpdate(0, uint32(len(cp)))
				s.framer.WriteWindowUpdate(s.streamID, uint32(len(cp)))
				s.mu.Unlock()
			}
			if frame.StreamEnded() {
				return
			}

		case *http2.HeadersFrame:
			if frame.StreamEnded() {
				return
			}

		case *http2.RSTStreamFrame:
			s.err = fmt.Errorf("h2: RST_STREAM code=%d", frame.ErrCode)
			log.Debugf("h2stream[%s]: received RST_STREAM code=%d", s.id, frame.ErrCode)
			return

		case *http2.GoAwayFrame:
			s.err = fmt.Errorf("h2: GOAWAY code=%d", frame.ErrCode)
			return

		case *http2.PingFrame:
			if !frame.IsAck() {
				s.mu.Lock()
				s.framer.WritePing(true, frame.Data)
				s.mu.Unlock()
			}

		case *http2.SettingsFrame:
			if !frame.IsAck() {
				// Check for window size changes
				frame.ForeachSetting(func(setting http2.Setting) error {
					if setting.ID == http2.SettingInitialWindowSize {
						s.windowMu.Lock()
						delta := int32(setting.Val) - s.sendWindow
						s.sendWindow += delta
						s.windowMu.Unlock()
						s.windowCond.Broadcast()
					}
					return nil
				})
				s.mu.Lock()
				s.framer.WriteSettingsAck()
				s.mu.Unlock()
			}

		case *http2.WindowUpdateFrame:
			// Update send-side flow control window
			s.windowMu.Lock()
			if frame.StreamID == 0 {
				s.connWindow += int32(frame.Increment)
			} else if frame.StreamID == s.streamID {
				s.sendWindow += int32(frame.Increment)
			}
			s.windowMu.Unlock()
			s.windowCond.Broadcast()
		}
	}
}

type sliceWriter struct{ buf *[]byte }

func (w *sliceWriter) Write(p []byte) (int, error) {
	*w.buf = append(*w.buf, p...)
	return len(p), nil
}

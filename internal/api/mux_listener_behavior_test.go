package api

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type transientEMFILEListener struct {
	net.Listener
	remainingEMFILE atomic.Int32
}

func (l *transientEMFILEListener) Accept() (net.Conn, error) {
	for {
		remaining := l.remainingEMFILE.Load()
		if remaining <= 0 {
			return l.Listener.Accept()
		}
		if l.remainingEMFILE.CompareAndSwap(remaining, remaining-1) {
			return nil, &net.OpError{
				Op:   "accept",
				Net:  "tcp",
				Addr: l.Listener.Addr(),
				Err:  os.NewSyscallError("accept4", syscall.EMFILE),
			}
		}
	}
}

func TestAcceptMuxRecoversFromEMFILE(t *testing.T) {
	baseListener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("failed to listen: %v", errListen)
	}
	defer func() {
		_ = baseListener.Close()
	}()

	listener := &transientEMFILEListener{Listener: baseListener}
	listener.remainingEMFILE.Store(3)

	var routed atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routed.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewUnstartedServer(handler)
	defer srv.Close()

	muxLn := newMuxListener(listener.Addr(), 1024)
	defer func() {
		_ = muxLn.Close()
	}()
	server := &Server{}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.acceptMuxConnections(listener, muxLn)
	}()

	srv.Listener = muxLn
	srv.Start()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, errGet := client.Get("http://" + listener.Addr().String() + "/")
	if errGet != nil {
		_ = baseListener.Close()
		t.Fatalf("HTTP request failed after transient EMFILE: %v", errGet)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			t.Errorf("close response body: %v", errClose)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if routed.Load() != 1 {
		t.Fatalf("routed = %d, want 1", routed.Load())
	}
}

func TestMuxListenerPutWithTimeout(t *testing.T) {
	t.Run("deliver before timeout", func(t *testing.T) {
		muxLn := newMuxListener(&net.TCPAddr{}, 1)
		defer func() { _ = muxLn.Close() }()

		conn, _ := net.Pipe()
		defer func() { _ = conn.Close() }()

		if errPut := muxLn.putWithTimeout(conn, time.Second); errPut != nil {
			t.Fatalf("putWithTimeout() error = %v, want nil", errPut)
		}
		got, errAccept := muxLn.Accept()
		if errAccept != nil {
			t.Fatalf("Accept() error = %v", errAccept)
		}
		if got != conn {
			t.Fatal("accepted connection differs from the delivered one")
		}
	})

	t.Run("reject after close", func(t *testing.T) {
		muxLn := newMuxListener(&net.TCPAddr{}, 1)
		_ = muxLn.Close()

		conn, _ := net.Pipe()
		defer func() { _ = conn.Close() }()

		if errPut := muxLn.putWithTimeout(conn, time.Second); errPut != net.ErrClosed {
			t.Fatalf("putWithTimeout() error = %v, want net.ErrClosed", errPut)
		}
	})

	t.Run("timeout on full buffer", func(t *testing.T) {
		muxLn := newMuxListener(&net.TCPAddr{}, 1)
		defer func() { _ = muxLn.Close() }()

		connA, connB := net.Pipe()
		defer func() { _ = connA.Close() }()
		defer func() { _ = connB.Close() }()

		if errPut := muxLn.putWithTimeout(connA, 0); errPut != nil {
			t.Fatalf("putWithTimeout(connA) error = %v, want nil", errPut)
		}
		if errPut := muxLn.putWithTimeout(connB, 20*time.Millisecond); errPut != net.ErrClosed {
			t.Fatalf("putWithTimeout(connB) error = %v, want net.ErrClosed on timeout", errPut)
		}
	})
}

func TestIsTransientAcceptError(t *testing.T) {
	if isTransientAcceptError(nil) {
		t.Fatal("nil error must not be transient")
	}
	if isTransientAcceptError(net.ErrClosed) {
		t.Fatal("net.ErrClosed must not be transient")
	}
	emfile := &net.OpError{Err: os.NewSyscallError("accept4", syscall.EMFILE)}
	if !isTransientAcceptError(emfile) {
		t.Fatal("EMFILE must be transient")
	}
	if isTransientAcceptError(io.EOF) {
		t.Fatal("io.EOF must not be transient")
	}
}

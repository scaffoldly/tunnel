package service

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/scaffoldly/tunnel/consts"
)

// TestProbe runs against real listeners on loopback. A fake would only prove
// the fake's behaviour, and what is actually in question here is how a TLS
// handshake lands on servers that do and do not speak it.
func TestProbe(t *testing.T) {
	plaintext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(plaintext.Close)

	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(tlsServer.Close)

	// Accepts connections and then says nothing at all — the case a naive
	// implementation hangs on.
	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = silent.Close() })
	go func() {
		for {
			conn, err := silent.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()

	tests := []struct {
		name    string
		address string
		want    string
		wantErr bool
	}{
		{
			name:    "a TLS server completes the handshake",
			address: tlsServer.Listener.Addr().String(),
			want:    consts.OriginSchemeTLS,
		},
		{
			name:    "a plaintext HTTP server does not",
			address: plaintext.Listener.Addr().String(),
			want:    consts.OriginScheme,
		},
		{
			name: "nothing listening is undetermined, not plaintext",
			// Port 1 on loopback: reliably refused, never in use.
			address: "127.0.0.1:1",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Probe(context.Background(), tc.address)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Probe() = %q, want an error: an unreachable origin proves nothing", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Probe() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("Probe() = %q, want %q", got, tc.want)
			}
		})
	}

	// Separately, because it asserts termination rather than a value: a server
	// that accepts and stalls must not hold the reconcile open.
	t.Run("a silent server times out rather than hanging", func(t *testing.T) {
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = Probe(context.Background(), silent.Addr().String())
		}()
		select {
		case <-done:
		case <-time.After(probeTimeout + 5*time.Second):
			t.Fatal("Probe did not return; a stalled origin would block every other Service")
		}
	})
}

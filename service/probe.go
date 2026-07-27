package service

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/scaffoldly/tunnel/consts"
)

// probeTimeout bounds one probe. Reconcile is single-threaded per controller,
// so a hung dial to one Service would stall every other Service in the cluster.
// Short enough that a black-holed address costs a few seconds, long enough for
// a TLS handshake across a slow node.
const probeTimeout = 3 * time.Second

// Prober reports how the origin at address speaks: consts.OriginScheme or
// consts.OriginSchemeTLS.
//
// An error means undetermined, not plaintext. The difference matters: guessing
// plaintext at a TLS origin produces a tunnel that connects and then fails
// every request, which looks like a broken tunnel rather than a misconfigured
// one.
//
// A field on the Reconciler rather than a package function so tests can answer
// without a network, and so this stays the one place that opens a socket.
type Prober func(ctx context.Context, address string) (string, error)

// Probe is the production Prober: try to speak TLS to the origin, and fall
// back to a plain connection.
//
// Two dials rather than one connection upgraded in place, because they answer
// two different questions and conflating them loses the third answer. A
// completed TLS handshake is strong evidence of TLS — a plaintext server
// cannot produce one. A plain connection that opens where TLS failed is good
// evidence of plaintext: an HTTP server answers a ClientHello with a 400 or
// closes, neither of which parses as a ServerHello. And neither succeeding
// says nothing at all about the origin, which is reported as undetermined
// rather than guessed at — the backend may simply not be listening yet.
//
// The fallback is a fresh dial on purpose. A server that rejects a handshake
// has usually closed the connection, so reusing it would confirm nothing; a
// second dial confirms the origin is actually reachable before concluding
// anything about how it speaks.
//
// InsecureSkipVerify because verification is not the question being asked, and
// answering it would answer "no" for every legitimate in-cluster origin: a
// Service's certificate is signed by the cluster CA or self-signed, and neither
// chains to a public root. The tunnel engine does not verify this hop either —
// it cannot, for the same reason — so requiring a valid chain here would refuse
// exactly the origins that do work.
//
// Each dial gets its own timeout, so a TLS attempt that stalls cannot starve
// the fallback of the budget it needs to answer. Worst case is therefore twice
// probeTimeout.
func Probe(ctx context.Context, address string) (string, error) {
	tlsCtx, cancelTLS := context.WithTimeout(ctx, probeTimeout)
	defer cancelTLS()

	dialer := tls.Dialer{
		Config: &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // detection, not authentication
		},
	}
	if conn, err := dialer.DialContext(tlsCtx, "tcp", address); err == nil {
		_ = conn.Close()
		return consts.OriginSchemeTLS, nil
	}

	plainCtx, cancelPlain := context.WithTimeout(ctx, probeTimeout)
	defer cancelPlain()

	var plain net.Dialer
	conn, err := plain.DialContext(plainCtx, "tcp", address)
	if err != nil {
		return "", fmt.Errorf("dial %s: %w", address, err)
	}
	_ = conn.Close()
	return consts.OriginScheme, nil
}

// originAddress is where the controller dials a Service to probe it: the same
// host:port the tunnel will front, so the probe answers the question actually
// being asked rather than one about a different endpoint.
func originAddress(namespace, service string, port int32) string {
	return fmt.Sprintf("%s.%s.%s:%d", service, namespace, consts.OriginDomain, port)
}

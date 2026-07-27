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

// Probe is the production Prober: connect, offer a TLS handshake, and see what
// comes back.
//
// Best effort by construction, and worth being precise about what it can and
// cannot tell. A completed handshake is strong evidence — a plaintext server
// cannot produce one. A refused handshake on a connection that opened is good
// evidence of plaintext: an HTTP server answers a ClientHello with a 400 or
// closes, neither of which parses as a ServerHello. A connection that never
// opens says nothing at all, and is reported as such.
//
// InsecureSkipVerify because verification is not the question. The certificate
// is almost always signed by the cluster CA or self-signed, and the tunnel
// engine does not verify it either; asking whether it chains to a public root
// would answer "no" for every legitimate in-cluster origin.
func Probe(ctx context.Context, address string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		// Nothing is listening, or nothing answered in time. The backend may
		// simply not be ready yet, so this is undetermined rather than
		// plaintext.
		return "", fmt.Errorf("dial %s: %w", address, err)
	}
	defer conn.Close() //nolint:errcheck // read-only probe

	if deadline, ok := ctx.Deadline(); ok {
		// A handshake against a server that accepts the connection and then
		// says nothing would otherwise hang past the context.
		_ = conn.SetDeadline(deadline)
	}

	tlsConn := tls.Client(conn, &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // detection, not authentication
	})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		// It answered, but not in TLS.
		return consts.OriginScheme, nil
	}
	return consts.OriginSchemeTLS, nil
}

// originAddress is where the controller dials a Service to probe it: the same
// host:port the tunnel will front, so the probe answers the question actually
// being asked rather than one about a different endpoint.
func originAddress(namespace, service string, port int32) string {
	return fmt.Sprintf("%s.%s.%s:%d", service, namespace, consts.OriginDomain, port)
}

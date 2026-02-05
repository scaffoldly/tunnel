package websocket

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	gobwas "github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/rs/zerolog"
)

const (
	// Time allowed to read the next pong message from the peer.
	defaultPongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	defaultPingPeriod = (defaultPongWait * 9) / 10

	PingPeriodContextKey = PingPeriodContext("pingPeriod")
)

type PingPeriodContext string

type Conn struct {
	rw  io.ReadWriter
	log *zerolog.Logger
	// writeLock makes sure
	// 1. Only one write at a time. The pinger and Stream function can both call write.
	// 2. Close only returns after in progress Write is finished, and no more Write will succeed after calling Close.
	writeLock sync.Mutex
	done      bool
}

func NewConn(ctx context.Context, rw io.ReadWriter, log *zerolog.Logger) *Conn {
	c := &Conn{
		rw:  rw,
		log: log,
	}
	go c.pinger(ctx)
	return c
}

// Read will read messages from the websocket connection
func (c *Conn) Read(reader []byte) (int, error) {
	data, err := wsutil.ReadClientBinary(c.rw)
	if err != nil {
		return 0, err
	}
	return copy(reader, data), nil
}

// Write will write messages to the websocket connection.
// It will not write to the connection after Close is called to fix TUN-5184
func (c *Conn) Write(p []byte) (int, error) {
	c.writeLock.Lock()
	defer c.writeLock.Unlock()
	if c.done {
		return 0, errors.New("write to closed websocket connection")
	}
	if err := wsutil.WriteServerBinary(c.rw, p); err != nil {
		return 0, err
	}

	return len(p), nil
}

func (c *Conn) pinger(ctx context.Context) {
	pongMessge := wsutil.Message{
		OpCode:  gobwas.OpPong,
		Payload: []byte{},
	}

	ticker := time.NewTicker(c.pingPeriod(ctx))
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			done, err := c.ping()
			if done {
				return
			}
			if err != nil {
				c.log.Debug().Err(err).Msgf("failed to write ping message")
			}
			if err := wsutil.HandleClientControlMessage(c.rw, pongMessge); err != nil {
				c.log.Debug().Err(err).Msgf("failed to write pong message")
			}
		case <-ctx.Done():
			return
		}
	}
}

func (c *Conn) ping() (bool, error) {
	c.writeLock.Lock()
	defer c.writeLock.Unlock()

	if c.done {
		return true, nil
	}

	return false, wsutil.WriteServerMessage(c.rw, gobwas.OpPing, []byte{})
}

func (c *Conn) pingPeriod(ctx context.Context) time.Duration {
	if val := ctx.Value(PingPeriodContextKey); val != nil {
		if period, ok := val.(time.Duration); ok {
			return period
		}
	}
	return defaultPingPeriod
}

// Close waits for the current write to finish. Further writes will return error
func (c *Conn) Close() {
	c.writeLock.Lock()
	defer c.writeLock.Unlock()
	c.done = true
}

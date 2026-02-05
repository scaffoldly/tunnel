package connection

import (
	"fmt"
	"sync"
)

// tunnelsForHA maps this cloudflared instance's HA connections to the tunnel IDs they serve.
type tunnelsForHA struct {
	sync.Mutex
	entries map[uint8]string
}

// NewTunnelsForHA initializes a tunnelsForHA.
func newTunnelsForHA() tunnelsForHA {
	return tunnelsForHA{
		entries: make(map[uint8]string),
	}
}

// Track a new tunnel ID, removing the disconnected tunnel (if any).
func (t *tunnelsForHA) AddTunnelID(haConn uint8, tunnelID string) {
	t.Lock()
	defer t.Unlock()
	t.entries[haConn] = tunnelID
}

func (t *tunnelsForHA) String() string {
	t.Lock()
	defer t.Unlock()
	return fmt.Sprintf("%v", t.entries)
}

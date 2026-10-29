package sshtunnel

// DropSession ends the tunnel's SSH session as a network failure would.
func DropSession(t *Tunnel) {
	t.mu.Lock()
	t.client.Close()
	t.mu.Unlock()
}

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"strconv"
	"sync"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/sshtunnel"
)

// sshVaultKey names a profile's SSH key passphrase in the vault, beside its password.
func sshVaultKey(profileID string) string { return profileID + ":ssh" }

func tunnelConfig(t *conn.SSHTunnel, passphrase string) sshtunnel.Config {
	return sshtunnel.Config{Host: t.Host, Port: t.Port, User: t.User, KeyFile: t.KeyFile, Passphrase: passphrase}
}

func tunnelTarget(p conn.Profile) string { return net.JoinHostPort(p.Host, strconv.Itoa(p.Port)) }

// tunnels keeps one SSH tunnel per profile open while the agent runs. Its
// local port stays the same, so the sources' pools for the profile survive
// the SSH session dropping and reconnecting.
type tunnels struct {
	mu     sync.Mutex
	byID   map[string]*tunnelEntry
	closed bool
}

type tunnelEntry struct {
	tunnel      *sshtunnel.Tunnel
	fingerprint string
}

func newTunnels() *tunnels { return &tunnels{byID: map[string]*tunnelEntry{}} }

func tunnelFingerprint(p conn.Profile, passphrase string) string {
	h := sha256.New()
	t := p.SSH
	for _, f := range []string{t.Host, strconv.Itoa(t.Port), t.User, t.KeyFile, passphrase, tunnelTarget(p)} {
		h.Write([]byte(strconv.Itoa(len(f)) + ":" + f))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// reach returns p as the sources dial it: unchanged without a tunnel, else
// at its tunnel's local port, opening the tunnel on first use.
func (ts *tunnels) reach(ctx context.Context, p conn.Profile, passphrase string) (conn.Profile, error) {
	if p.SSH == nil {
		return p, nil
	}
	fp := tunnelFingerprint(p, passphrase)
	ts.mu.Lock()
	if ts.closed {
		ts.mu.Unlock()
		return p, errClosed
	}
	if e := ts.byID[p.ID]; e != nil && e.fingerprint == fp {
		ts.mu.Unlock()
		return p.ViaTunnel(e.tunnel.Port()), nil
	}
	ts.mu.Unlock()

	// Opening can take the SSH dial timeout, so other profiles are not held up.
	t, err := sshtunnel.Open(ctx, tunnelConfig(p.SSH, passphrase), tunnelTarget(p))
	if err != nil {
		return p, err
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.closed {
		go t.Close()
		return p, errClosed
	}
	if e := ts.byID[p.ID]; e != nil {
		if e.fingerprint == fp { // another request opened the same tunnel meanwhile
			go t.Close()
			return p.ViaTunnel(e.tunnel.Port()), nil
		}
		go e.tunnel.Close()
	}
	ts.byID[p.ID] = &tunnelEntry{tunnel: t, fingerprint: fp}
	return p.ViaTunnel(t.Port()), nil
}

func (ts *tunnels) Forget(profileID string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if e := ts.byID[profileID]; e != nil {
		go e.tunnel.Close()
		delete(ts.byID, profileID)
	}
}

func (ts *tunnels) Close() {
	ts.mu.Lock()
	ts.closed = true
	entries := ts.byID
	ts.byID = map[string]*tunnelEntry{}
	ts.mu.Unlock()
	for _, e := range entries {
		e.tunnel.Close()
	}
}

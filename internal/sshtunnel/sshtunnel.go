// Package sshtunnel forwards a local port to a database through an SSH
// server (a bastion), so every driver reaches the database by dialling
// 127.0.0.1. Host keys are checked strictly against known_hosts; keys come
// from ssh-agent and a key file, never passwords.
package sshtunnel

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ErrTunnel marks every failure to set up or use the tunnel, so it is
// reported as an SSH problem rather than a database one.
var ErrTunnel = errors.New("ssh tunnel")

const (
	dialTimeout       = 10 * time.Second
	keepaliveInterval = 30 * time.Second
)

// Config is how to reach and authenticate to the SSH server.
type Config struct {
	Host string
	Port int
	User string
	// KeyFile is a private key tried after the agent's keys; empty means the
	// default ~/.ssh/id_ed25519, id_ecdsa and id_rsa when they need no passphrase.
	KeyFile    string
	Passphrase string
	// KnownHosts is the known_hosts file; empty means ~/.ssh/known_hosts.
	KnownHosts string
}

func (c Config) addr() string { return net.JoinHostPort(c.Host, strconv.Itoa(c.Port)) }

func fail(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrTunnel}, args...)...)
}

// Dial connects and authenticates to the SSH server.
func Dial(ctx context.Context, cfg Config) (*ssh.Client, error) {
	check, algorithms, err := hostKeyCheck(cfg)
	if err != nil {
		return nil, err
	}
	var keyErr error
	hostKey := func(host string, remote net.Addr, key ssh.PublicKey) error {
		keyErr = check(host, remote, key)
		return keyErr
	}
	auth, closeAgent, err := authMethod(cfg)
	if err != nil {
		return nil, err
	}
	defer closeAgent()
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	nc, err := (&net.Dialer{}).DialContext(ctx, "tcp", cfg.addr())
	if err != nil {
		return nil, fail("cannot reach the SSH server %s: %w", cfg.addr(), err)
	}
	deadline, _ := ctx.Deadline()
	nc.SetDeadline(deadline)
	c, chans, reqs, err := ssh.NewClientConn(nc, cfg.addr(), &ssh.ClientConfig{
		User: cfg.User, Auth: []ssh.AuthMethod{auth}, HostKeyCallback: hostKey, HostKeyAlgorithms: algorithms,
	})
	if err != nil {
		nc.Close()
		if keyErr != nil {
			return nil, keyErr // without x/crypto's "handshake failed" prefix
		}
		return nil, fail("SSH login to %s@%s failed: %w", cfg.User, cfg.addr(), err)
	}
	nc.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), nil
}

// hostKeyCheck accepts only keys known_hosts lists for the server, and asks
// for the key types it lists so a server with several keys offers a known one.
func hostKeyCheck(cfg Config) (ssh.HostKeyCallback, []string, error) {
	path := cfg.KnownHosts
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil, fail("cannot find the home directory for known_hosts: %w", err)
		}
		path = filepath.Join(home, ".ssh", "known_hosts")
	}
	cb, err := knownhosts.New(path)
	if errors.Is(err, fs.ErrNotExist) {
		// No known_hosts yet knows no host, which the check below reports with the server's fingerprint.
		cb, err = func(string, net.Addr, ssh.PublicKey) error { return &knownhosts.KeyError{} }, nil
	}
	if err != nil {
		return nil, nil, fail("cannot read %s, which must list the SSH server's host key: %w", path, err)
	}
	var known []knownhosts.KnownKey
	var ke *knownhosts.KeyError
	// A key that matches nothing makes the callback list the keys known for the host.
	if err := cb(cfg.addr(), &net.TCPAddr{IP: net.IPv4zero}, placeholderKey); errors.As(err, &ke) {
		known = ke.Want
	}
	var algorithms []string
	for _, k := range known {
		if k.Key.Type() == ssh.KeyAlgoRSA {
			algorithms = append(algorithms, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256)
		} else {
			algorithms = append(algorithms, k.Key.Type())
		}
	}
	check := func(host string, remote net.Addr, key ssh.PublicKey) error {
		err := cb(host, remote, key)
		if errors.As(err, &ke) {
			if len(ke.Want) == 0 {
				return fail("the SSH server %s is not in %s (its %s key is %s); connect once with ssh to check and add it",
					cfg.addr(), path, key.Type(), ssh.FingerprintSHA256(key))
			}
			return fail("the host key of the SSH server %s does not match %s (it sent %s %s): the server was reinstalled or someone is intercepting the connection",
				cfg.addr(), path, key.Type(), ssh.FingerprintSHA256(key))
		}
		return err
	}
	return check, algorithms, nil
}

var placeholderKey, _ = ssh.NewPublicKey(ed25519.PublicKey(make([]byte, ed25519.PublicKeySize)))

// authMethod offers the agent's keys, then the key file's.
func authMethod(cfg Config) (ssh.AuthMethod, func(), error) {
	var signers []ssh.Signer
	closeAgent := func() {}
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if ac, err := net.Dial("unix", sock); err == nil {
			closeAgent = func() { ac.Close() }
			if s, err := agent.NewClient(ac).Signers(); err == nil {
				signers = append(signers, s...)
			}
		}
	}
	files, explicit := []string{cfg.KeyFile}, cfg.KeyFile != ""
	if !explicit {
		if home, err := os.UserHomeDir(); err == nil {
			files = []string{filepath.Join(home, ".ssh", "id_ed25519"), filepath.Join(home, ".ssh", "id_ecdsa"), filepath.Join(home, ".ssh", "id_rsa")}
		}
	}
	for _, f := range files {
		s, err := keyFileSigner(f, cfg.Passphrase)
		if err == nil {
			signers = append(signers, s)
		} else if explicit {
			closeAgent()
			return nil, nil, err
		}
	}
	if len(signers) == 0 {
		closeAgent()
		return nil, nil, fail("no SSH key to log in with: add your key to ssh-agent or choose a key file")
	}
	return ssh.PublicKeys(signers...), closeAgent, nil
}

func keyFileSigner(path, passphrase string) (ssh.Signer, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fail("cannot read the key file: %w", err)
	}
	s, err := ssh.ParsePrivateKey(pem)
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		if passphrase == "" {
			return nil, fail("the key file %s is encrypted: save its passphrase with the profile", path)
		}
		s, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(passphrase))
		if errors.Is(err, x509.IncorrectPasswordError) {
			return nil, fail("wrong passphrase for the key file %s", path)
		}
	}
	if err != nil {
		return nil, fail("cannot use the key file %s: %w", path, err)
	}
	return s, nil
}

// Tunnel listens on a local port and forwards each connection to the target
// through the SSH server, reconnecting to it when the session drops.
type Tunnel struct {
	cfg    Config
	target string
	ln     net.Listener
	done   chan struct{}
	wg     sync.WaitGroup

	mu     sync.Mutex
	client *ssh.Client
}

// Open connects to the SSH server, checks that it can reach target
// (host:port as the SSH server resolves it) and starts listening.
func Open(ctx context.Context, cfg Config, target string) (*Tunnel, error) {
	c, err := Dial(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := probe(ctx, c, target); err != nil {
		c.Close()
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		c.Close()
		return nil, fail("cannot listen on a local port: %w", err)
	}
	t := &Tunnel{cfg: cfg, target: target, ln: ln, done: make(chan struct{}), client: c}
	t.wg.Add(2)
	go t.serve()
	go t.keepalive()
	return t, nil
}

func probe(ctx context.Context, c *ssh.Client, target string) error {
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	rc, err := c.DialContext(ctx, "tcp", target)
	if err != nil {
		return fail("the SSH server could not reach %s: %w", target, err)
	}
	rc.Close()
	return nil
}

// Port is the local port the database is reached on, at 127.0.0.1.
func (t *Tunnel) Port() int { return t.ln.Addr().(*net.TCPAddr).Port }

// Close stops listening and ends the SSH session and every forwarded connection.
func (t *Tunnel) Close() error {
	select {
	case <-t.done:
		return nil
	default:
	}
	close(t.done)
	err := t.ln.Close()
	t.mu.Lock()
	if t.client != nil {
		t.client.Close()
		t.client = nil
	}
	t.mu.Unlock()
	t.wg.Wait()
	return err
}

func (t *Tunnel) serve() {
	defer t.wg.Done()
	for {
		lc, err := t.ln.Accept()
		if err != nil {
			return
		}
		t.wg.Add(1)
		go func() {
			defer t.wg.Done()
			t.forward(lc)
		}()
	}
}

func (t *Tunnel) forward(lc net.Conn) {
	defer lc.Close()
	rc, err := t.dialTarget()
	if err != nil {
		return
	}
	defer rc.Close()
	// Closing both ends when either direction finishes ends the other copy.
	var once sync.Once
	stop := func() { once.Do(func() { lc.Close(); rc.Close() }) }
	go func() {
		io.Copy(rc, lc)
		stop()
	}()
	io.Copy(lc, rc)
	stop()
}

// dialTarget opens a channel to the target, reconnecting once if the SSH
// session has dropped; a refusal by the target is not retried.
func (t *Tunnel) dialTarget() (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*dialTimeout)
	defer cancel()
	for attempt := 0; ; attempt++ {
		c, err := t.session(ctx)
		if err != nil {
			return nil, err
		}
		rc, err := c.DialContext(ctx, "tcp", t.target)
		var refused *ssh.OpenChannelError
		if err == nil || errors.As(err, &refused) || attempt > 0 {
			return rc, err
		}
		t.drop(c)
	}
}

func (t *Tunnel) session(ctx context.Context) (*ssh.Client, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	select {
	case <-t.done:
		return nil, net.ErrClosed
	default:
	}
	if t.client == nil {
		c, err := Dial(ctx, t.cfg)
		if err != nil {
			return nil, err
		}
		t.client = c
	}
	return t.client, nil
}

func (t *Tunnel) drop(c *ssh.Client) {
	t.mu.Lock()
	if t.client == c {
		t.client = nil
	}
	t.mu.Unlock()
	c.Close()
}

// keepalive notices a dead session (NAT timeouts, a restarted bastion), so the
// next connection reconnects instead of waiting on a half-open one.
func (t *Tunnel) keepalive() {
	defer t.wg.Done()
	tick := time.NewTicker(keepaliveInterval)
	defer tick.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-tick.C:
		}
		t.mu.Lock()
		c := t.client
		t.mu.Unlock()
		if c == nil {
			continue
		}
		reply := make(chan error, 1)
		go func() {
			_, _, err := c.SendRequest("keepalive@openssh.com", true, nil)
			reply <- err
		}()
		select {
		case err := <-reply:
			if err != nil {
				t.drop(c)
			}
		case <-time.After(dialTimeout):
			t.drop(c)
		case <-t.done:
			return
		}
	}
}

package sshtunnel_test

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/dbscope/dbscope/internal/mytest"
	. "github.com/dbscope/dbscope/internal/sshtunnel"
)

// echo listens on this machine and answers each line with "echo: line".
func echo(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				s := bufio.NewScanner(c)
				for s.Scan() {
					c.Write([]byte("echo: " + s.Text() + "\n"))
				}
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func isolate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")
}

func config(b mytest.Bastion) Config {
	return Config{Host: b.Host, Port: b.Port, User: b.User, KeyFile: b.KeyFile, KnownHosts: b.KnownHosts}
}

func target(port int) string {
	h, p := mytest.TunnelTarget(port)
	return net.JoinHostPort(h, strconv.Itoa(p))
}

func roundTrip(t *testing.T, tun *Tunnel, msg string) {
	t.Helper()
	c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(tun.Port()))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte(msg + "\n"))
	got, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || got != "echo: "+msg+"\n" {
		t.Fatalf("through the tunnel: %q, %v", got, err)
	}
}

func wantErr(t *testing.T, err error, part string) {
	t.Helper()
	if err == nil || !errors.Is(err, ErrTunnel) || !strings.Contains(err.Error(), part) {
		t.Fatalf("error %v, want ErrTunnel containing %q", err, part)
	}
}

func TestTunnel(t *testing.T) {
	isolate(t)
	b := mytest.SSH(t)
	port := echo(t)
	ctx := context.Background()

	t.Run("forwards and reconnects", func(t *testing.T) {
		tun, err := Open(ctx, config(b), target(port))
		if err != nil {
			t.Fatal(err)
		}
		defer tun.Close()
		roundTrip(t, tun, "one")
		roundTrip(t, tun, "two")
		// A dropped session is replaced on the next connection, on the same local port.
		DropSession(tun)
		roundTrip(t, tun, "three")
		if err := tun.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(tun.Port())); err == nil {
			t.Error("the port still listens after Close")
		}
	})

	t.Run("unknown host key", func(t *testing.T) {
		cfg := config(b)
		cfg.KnownHosts = filepath.Join(t.TempDir(), "known_hosts")
		os.WriteFile(cfg.KnownHosts, nil, 0o600)
		_, err := Open(ctx, cfg, target(port))
		wantErr(t, err, "is not in")
		wantErr(t, err, "key is SHA256:")
		cfg.KnownHosts = filepath.Join(t.TempDir(), "missing")
		_, err = Open(ctx, cfg, target(port))
		wantErr(t, err, "is not in")
	})

	t.Run("changed host key", func(t *testing.T) {
		cfg := config(b)
		other, _, _ := ed25519.GenerateKey(rand.Reader)
		pk, _ := ssh.NewPublicKey(other)
		cfg.KnownHosts = filepath.Join(t.TempDir(), "known_hosts")
		os.WriteFile(cfg.KnownHosts, []byte("["+b.Host+"]:"+strconv.Itoa(b.Port)+" "+string(ssh.MarshalAuthorizedKey(pk))), 0o600)
		_, err := Open(ctx, cfg, target(port))
		wantErr(t, err, "does not match")
	})

	t.Run("key the server does not accept", func(t *testing.T) {
		cfg := config(b)
		_, other, _ := ed25519.GenerateKey(rand.Reader)
		cfg.KeyFile = writeKey(t, other, "")
		_, err := Open(ctx, cfg, target(port))
		wantErr(t, err, "SSH login to tunnel@")
	})

	t.Run("encrypted key file", func(t *testing.T) {
		cfg := config(b)
		cfg.KeyFile = writeKey(t, b.Key, "s3cret")
		_, err := Open(ctx, cfg, target(port))
		wantErr(t, err, "is encrypted")
		cfg.Passphrase = "wrong"
		_, err = Open(ctx, cfg, target(port))
		wantErr(t, err, "wrong passphrase")
		cfg.Passphrase = "s3cret"
		tun, err := Open(ctx, cfg, target(port))
		if err != nil {
			t.Fatal(err)
		}
		defer tun.Close()
		roundTrip(t, tun, "encrypted")
	})

	t.Run("ssh-agent", func(t *testing.T) {
		dir, err := os.MkdirTemp("", "dbscope-agent")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(dir)
		sock := filepath.Join(dir, "agent.sock")
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		ring := agent.NewKeyring()
		ring.Add(agent.AddedKey{PrivateKey: b.Key})
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go agent.ServeAgent(ring, c)
			}
		}()
		t.Setenv("SSH_AUTH_SOCK", sock)
		cfg := config(b)
		cfg.KeyFile = ""
		tun, err := Open(ctx, cfg, target(port))
		if err != nil {
			t.Fatal(err)
		}
		defer tun.Close()
		roundTrip(t, tun, "agent")
	})

	t.Run("no key at all", func(t *testing.T) {
		cfg := config(b)
		cfg.KeyFile = ""
		_, err := Open(ctx, cfg, target(port))
		wantErr(t, err, "no SSH key")
	})

	t.Run("target the server cannot reach", func(t *testing.T) {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		closed := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		_, err := Open(ctx, config(b), target(closed))
		wantErr(t, err, "could not reach")
	})
}

func writeKey(t *testing.T, key ed25519.PrivateKey, passphrase string) string {
	t.Helper()
	var block *pem.Block
	var err error
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(key, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(key, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

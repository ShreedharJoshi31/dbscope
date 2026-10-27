// Package capture records the SQL an ORM migration command sends, through a
// PostgreSQL wire-protocol proxy in front of a throwaway shadow database, and
// splits it into per-migration SQL files (docs/ORM-CAPTURE.md).
package capture

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

const (
	codeSSL    = 80877103
	codeGSS    = 80877104
	codeCancel = 80877102

	maxStartup = 10_000
	maxBody    = 1 << 30
)

// Upstream is where the proxy forwards connections: the shadow database.
type Upstream struct {
	Network, Addr string
	TLS           *tls.Config // nil: plaintext
	TLSRequired   bool        // with TLS: fail instead of continuing in plaintext after 'N'
}

// UpstreamFrom derives the shadow address and TLS settings from a parsed URL.
func UpstreamFrom(cfg *pgconn.Config) Upstream {
	up := Upstream{Network: "tcp", Addr: net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))), TLS: cfg.TLSConfig}
	if len(cfg.Host) > 0 && cfg.Host[0] == '/' {
		up.Network, up.Addr = "unix", fmt.Sprintf("%s/.s.PGSQL.%d", cfg.Host, cfg.Port)
	}
	if up.TLS != nil {
		up.TLSRequired = true
		for _, fb := range cfg.Fallbacks {
			if fb.TLSConfig == nil {
				up.TLSRequired = false
			}
		}
	}
	return up
}

// Proxy relays client connections to the upstream unchanged and records the
// statements they run.
type Proxy struct {
	ln    net.Listener
	up    Upstream
	rec   *Recorder
	wg    sync.WaitGroup
	conns atomic.Int64

	mu   sync.Mutex
	open map[net.Conn]bool
}

// Listen starts a proxy on addr (use "127.0.0.1:0").
func Listen(addr string, up Upstream, rec *Recorder) (*Proxy, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	p := &Proxy{ln: ln, up: up, rec: rec, open: map[net.Conn]bool{}}
	p.wg.Add(1)
	go p.serve()
	return p, nil
}

// Port is the local port the proxy listens on.
func (p *Proxy) Port() int { return p.ln.Addr().(*net.TCPAddr).Port }

// Close stops accepting and waits for open connections to finish; after
// grace it closes the ones still open (e.g. a pool a background process kept).
func (p *Proxy) Close(grace time.Duration) error {
	err := p.ln.Close()
	done := make(chan struct{})
	go func() { p.wg.Wait(); close(done) }()
	select {
	case <-done:
		return err
	case <-time.After(grace):
	}
	p.mu.Lock()
	for c := range p.open {
		c.Close()
	}
	p.mu.Unlock()
	<-done
	return err
}

func (p *Proxy) serve() {
	defer p.wg.Done()
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.wg.Add(1)
		p.mu.Lock()
		p.open[c] = true
		p.mu.Unlock()
		go func() {
			defer p.wg.Done()
			defer func() {
				p.mu.Lock()
				delete(p.open, c)
				p.mu.Unlock()
				c.Close()
			}()
			if err := p.handle(c); err != nil {
				p.rec.note("connection: %v", err)
			}
		}()
	}
}

func (p *Proxy) handle(client net.Conn) error {
	startup, err := negotiate(client)
	if err != nil || startup == nil {
		return err
	}
	code := binary.BigEndian.Uint32(startup[4:8])
	if code == codeCancel {
		up, err := p.dial()
		if err != nil {
			return err
		}
		defer up.Close()
		_, err = up.Write(startup)
		return err
	}
	if code>>16 != 3 {
		return fmt.Errorf("unsupported protocol version %d.%d", code>>16, code&0xffff)
	}
	var sm pgproto3.StartupMessage
	_ = sm.Decode(startup[4:]) // only for labels; the raw bytes are forwarded either way

	up, err := p.dial()
	if err != nil {
		return err
	}
	defer up.Close()
	if _, err := up.Write(startup); err != nil {
		return err
	}
	s := &session{id: int(p.conns.Add(1)), rec: p.rec, database: sm.Parameters["database"], stmts: map[string]string{}, portals: map[string]portal{}, txStatus: 'I'}
	p.rec.connected()

	errc := make(chan error, 2)
	go func() { errc <- s.fromClient(bufio.NewReader(client), up) }()
	go func() { errc <- s.fromServer(bufio.NewReader(up), client) }()
	err = <-errc
	// Either side finishing ends the session; unblock the other copy loop.
	client.Close()
	up.Close()
	<-errc
	s.mu.Lock()
	if s.txStatus != 'I' {
		// The server rolls back a transaction left open at disconnect.
		p.rec.add(Event{Conn: s.id, Database: s.database, InTx: true, SQL: "ROLLBACK", Tag: "ROLLBACK", Error: "connection closed inside a transaction"})
	}
	s.mu.Unlock()
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// negotiate answers SSL and GSS encryption requests with 'N' and returns the
// next startup packet (StartupMessage or CancelRequest), length included.
func negotiate(c net.Conn) ([]byte, error) {
	for range 3 {
		var head [8]byte
		if _, err := io.ReadFull(c, head[:1]); err != nil {
			return nil, err
		}
		if head[0] == 0x16 {
			return nil, errors.New("client started TLS directly (sslnegotiation=direct); connect with sslmode=disable")
		}
		if _, err := io.ReadFull(c, head[1:]); err != nil {
			return nil, err
		}
		n := binary.BigEndian.Uint32(head[:4])
		if n < 8 || n > maxStartup {
			return nil, fmt.Errorf("bad startup packet length %d", n)
		}
		switch code := binary.BigEndian.Uint32(head[4:8]); code {
		case codeSSL, codeGSS:
			if n != 8 {
				return nil, fmt.Errorf("bad encryption request length %d", n)
			}
			if _, err := c.Write([]byte{'N'}); err != nil {
				return nil, err
			}
			continue
		}
		pkt := make([]byte, n)
		copy(pkt, head[:])
		if _, err := io.ReadFull(c, pkt[8:]); err != nil {
			return nil, err
		}
		return pkt, nil
	}
	return nil, errors.New("too many encryption requests")
}

func (p *Proxy) dial() (net.Conn, error) {
	c, err := net.Dial(p.up.Network, p.up.Addr)
	if err != nil {
		return nil, fmt.Errorf("shadow database: %w", err)
	}
	if p.up.TLS == nil {
		return c, nil
	}
	req := binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, 8), codeSSL)
	if _, err := c.Write(req); err != nil {
		c.Close()
		return nil, err
	}
	// Exactly one byte: anything read ahead would be injected plaintext (CVE-2021-23222).
	var b [1]byte
	if _, err := io.ReadFull(c, b[:]); err != nil {
		c.Close()
		return nil, err
	}
	switch {
	case b[0] == 'S':
		tc := tls.Client(c, p.up.TLS)
		if err := tc.HandshakeContext(context.Background()); err != nil {
			c.Close()
			return nil, fmt.Errorf("shadow database TLS: %w", err)
		}
		return tc, nil
	case b[0] == 'N' && !p.up.TLSRequired:
		return c, nil
	}
	c.Close()
	return nil, errors.New("shadow database refused TLS, which its sslmode requires")
}

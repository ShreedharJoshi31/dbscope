package capture

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/dbscope/dbscope/internal/migrate/parse"
)

type portal struct {
	sql    string
	params []Param
}

// pending is a client message whose result the server has not sent yet.
type pending struct {
	kind  byte // 'Q' query, 'E' execute, 'S' sync
	stmts []string
	exec  int // statements of a query that have completed
	unit  int
	param []Param
}

// session is one proxied connection. The client loop appends to the queue
// before forwarding, so a response never arrives before its request.
type session struct {
	id       int
	database string
	rec      *Recorder

	mu       sync.Mutex
	stmts    map[string]string
	portals  map[string]portal
	queue    []pending
	units    int
	unit     int // current extended-protocol unit, ended by Sync
	txStatus byte
}

func (s *session) fromClient(r *bufio.Reader, w io.Writer) error {
	for {
		typ, body, err := relay(r, w, func(t byte) bool { return t == 'Q' || t == 'P' || t == 'B' || t == 'E' || t == 'S' || t == 'C' })
		if err != nil {
			return err
		}
		if body == nil {
			continue
		}
		s.mu.Lock()
		err = s.client(typ, body)
		s.mu.Unlock()
		if err != nil {
			return err
		}
		if err := forward(w, typ, body); err != nil {
			return err
		}
	}
}

func (s *session) fromServer(r *bufio.Reader, w io.Writer) error {
	for {
		typ, body, err := relay(r, w, func(t byte) bool { return t == 'Z' || t == 'C' || t == 'E' || t == 'I' || t == 's' || t == 'R' })
		if err != nil {
			return err
		}
		if body == nil {
			continue
		}
		if typ == 'R' {
			body = withoutChannelBinding(body)
		}
		s.mu.Lock()
		err = s.server(typ, body)
		s.mu.Unlock()
		if err != nil {
			return err
		}
		if err := forward(w, typ, body); err != nil {
			return err
		}
	}
}

// withoutChannelBinding removes SCRAM-SHA-256-PLUS from an AuthenticationSASL
// offer. The client's leg is plaintext, and libpq refuses a -PLUS offer on a
// connection it did not encrypt; plain SCRAM still authenticates over the
// shadow's TLS. Other authentication messages pass unchanged.
func withoutChannelBinding(body []byte) []byte {
	if len(body) < 4 || binary.BigEndian.Uint32(body) != 10 {
		return body
	}
	out := append([]byte(nil), body[:4]...)
	for _, m := range bytes.Split(bytes.TrimRight(body[4:], "\x00"), []byte{0}) {
		if !bytes.HasSuffix(m, []byte("-PLUS")) {
			out = append(append(out, m...), 0)
		}
	}
	return append(out, 0)
}

// relay reads one message. Messages the caller does not inspect are copied
// to w as they are read and returned with a nil body.
func relay(r *bufio.Reader, w io.Writer, inspect func(byte) bool) (byte, []byte, error) {
	var head [5]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, nil, err
	}
	n := int64(binary.BigEndian.Uint32(head[1:])) - 4
	if n < 0 || n > maxBody {
		return 0, nil, fmt.Errorf("bad message length for %q", head[0])
	}
	if !inspect(head[0]) {
		if _, err := w.Write(head[:]); err != nil {
			return 0, nil, err
		}
		_, err := io.CopyN(w, r, n)
		return head[0], nil, err
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return head[0], body, nil
}

func forward(w io.Writer, typ byte, body []byte) error {
	msg := make([]byte, 5, 5+len(body))
	msg[0] = typ
	binary.BigEndian.PutUint32(msg[1:], uint32(len(body)+4))
	_, err := w.Write(append(msg, body...))
	return err
}

func (s *session) client(typ byte, body []byte) error {
	switch typ {
	case 'Q':
		var m pgproto3.Query
		if err := m.Decode(body); err != nil {
			return err
		}
		// A simple query discards the unnamed statement and portal.
		delete(s.stmts, "")
		delete(s.portals, "")
		s.units++
		s.queue = append(s.queue, pending{kind: 'Q', stmts: splitSQL(m.String), unit: s.units})
	case 'P':
		var m pgproto3.Parse
		if err := m.Decode(body); err != nil {
			return err
		}
		s.stmts[m.Name] = m.Query
	case 'B':
		var m pgproto3.Bind
		if err := m.Decode(body); err != nil {
			return err
		}
		s.portals[m.DestinationPortal] = portal{sql: s.stmts[m.PreparedStatement], params: params(m)}
	case 'C':
		var m pgproto3.Close
		if err := m.Decode(body); err != nil {
			return err
		}
		if m.ObjectType == 'S' {
			delete(s.stmts, m.Name)
		} else {
			delete(s.portals, m.Name)
		}
	case 'E':
		var m pgproto3.Execute
		if err := m.Decode(body); err != nil {
			return err
		}
		if s.unit == 0 {
			s.units++
			s.unit = s.units
		}
		p := s.portals[m.Portal]
		s.queue = append(s.queue, pending{kind: 'E', stmts: []string{p.sql}, unit: s.unit, param: p.params})
	case 'S':
		s.queue = append(s.queue, pending{kind: 'S', unit: s.unit})
		s.unit = 0
	}
	return nil
}

func (s *session) server(typ byte, body []byte) error {
	switch typ {
	case 'C', 'I', 's':
		if len(s.queue) == 0 {
			return nil
		}
		head := &s.queue[0]
		tag := ""
		if typ == 'C' {
			var m pgproto3.CommandComplete
			if err := m.Decode(body); err != nil {
				return err
			}
			tag = string(m.CommandTag)
		}
		switch head.kind {
		case 'E':
			s.done(head, 0, tag, "")
			s.queue = s.queue[1:]
		case 'Q':
			if head.exec < len(head.stmts) {
				s.done(head, head.exec, tag, "")
				head.exec++
			}
		}
	case 'E':
		var m pgproto3.ErrorResponse
		if err := m.Decode(body); err != nil {
			return err
		}
		msg := m.Code + " " + m.Message
		if len(s.queue) == 0 {
			return nil
		}
		head := &s.queue[0]
		if s.txStatus == 'I' {
			// An error inside an implicit transaction undoes the unit's earlier
			// statements; at a Sync it is the implicit commit failing (e.g. a
			// deferred constraint), so nothing in the unit took effect.
			s.rec.failUnit(s.id, head.unit, msg)
		}
		switch head.kind {
		case 'Q':
			if head.exec < len(head.stmts) {
				s.done(head, head.exec, "", msg)
			}
			head.exec = len(head.stmts)
		case 'E':
			s.done(head, 0, "", msg)
			// The server discards everything up to the next Sync.
			for len(s.queue) > 0 && s.queue[0].kind != 'S' {
				s.queue = s.queue[1:]
			}
		}
	case 'Z':
		var m pgproto3.ReadyForQuery
		if err := m.Decode(body); err != nil {
			return err
		}
		s.txStatus = m.TxStatus
		// ReadyForQuery answers one Query or one Sync; Executes still queued
		// before them produced no result.
		for len(s.queue) > 0 {
			k := s.queue[0].kind
			s.queue = s.queue[1:]
			if k == 'Q' || k == 'S' {
				break
			}
		}
	}
	return nil
}

// done records the result of statement i of a queued Query or Execute.
func (s *session) done(p *pending, i int, tag, errMsg string) {
	s.rec.add(Event{
		Conn:     s.id,
		Database: s.database,
		Unit:     p.unit,
		InTx:     s.txStatus != 'I',
		SQL:      p.stmts[i],
		Params:   p.param,
		Tag:      tag,
		Error:    errMsg,
	})
}

// splitSQL splits a simple-protocol query string into its statements, the
// unit the server reports one CommandComplete for each.
func splitSQL(text string) []string {
	f := parse.Split(text)
	if len(f.Statements) == 0 {
		return []string{text}
	}
	out := make([]string, len(f.Statements))
	for i, st := range f.Statements {
		out[i] = st.SQL
	}
	return out
}

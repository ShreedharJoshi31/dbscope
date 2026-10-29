package agent

import (
	"encoding/pem"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/pgtest"
)

// sshHome makes the bastion's host key the only one known, in a home of its
// own. Open the vault first: on macOS it looks for the login keychain in HOME.
func sshHome(t *testing.T, b mytest.Bastion) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTH_SOCK", "")
	known, err := os.ReadFile(b.KnownHosts)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	if err := os.WriteFile(filepath.Join(home, ".ssh", "known_hosts"), known, 0o600); err != nil {
		t.Fatal(err)
	}
}

// tunnelled is the profile body for p reached through the bastion. The host
// is a name only the bastion resolves, so nothing reaches it except through the tunnel.
func tunnelled(p conn.Profile, password string, b mytest.Bastion, ssh map[string]any) map[string]any {
	host, port := mytest.TunnelTarget(p.Port)
	if ssh == nil {
		ssh = map[string]any{"host": b.Host, "port": b.Port, "user": b.User, "keyFile": b.KeyFile}
	}
	return map[string]any{"name": string(p.Engine) + " via ssh", "engine": p.Engine, "host": host, "port": port,
		"database": p.Database, "user": p.User, "password": password, "sslMode": p.SSLMode, "env": "local",
		"options": p.Options, "ssh": ssh}
}

func TestSSHTunnelEveryEngine(t *testing.T) {
	b := mytest.SSH(t)
	e := keyringEnv(t)
	sshHome(t, b)
	cases := []struct {
		name  string
		start func(t *testing.T) (conn.Profile, string)
	}{
		{"postgres", func(t *testing.T) (conn.Profile, string) {
			d := pgtest.Servers(t, "18")[0].NewDB(t)
			d.Exec(t, `CREATE TABLE users (id int PRIMARY KEY)`)
			return d.Superuser(t)
		}},
		{"mysql", func(t *testing.T) (conn.Profile, string) { return mytest.Server(t, "mysql"), mytest.Password }},
		{"mongodb", func(t *testing.T) (conn.Profile, string) {
			p := mytest.Mongo(t)
			// Without directConnection the driver would dial the replica set's own member names.
			p.Options = "authSource=admin"
			return p, mytest.Password
		}},
		{"redis", func(t *testing.T) (conn.Profile, string) { return mytest.Redis(t, "redis7"), "" }},
		{"cassandra", func(t *testing.T) (conn.Profile, string) {
			p := mytest.Cassandra(t, "cassandra")
			p.Database = "system"
			return p, mytest.CassandraPassword
		}},
		{"neo4j", func(t *testing.T) (conn.Profile, string) { return mytest.Neo4j(t, "5"), mytest.Neo4jPassword }},
		{"elasticsearch", func(t *testing.T) (conn.Profile, string) { return mytest.Elastic(t, "es9"), mytest.ElasticPassword }},
		{"clickhouse", func(t *testing.T) (conn.Profile, string) {
			return mytest.ClickHouse(t, "26.8"), mytest.ClickHousePassword
		}},
		{"sqlserver", func(t *testing.T) (conn.Profile, string) { return mytest.MSSQL(t, "2025"), mytest.MSSQLPassword }},
		{"oracle", func(t *testing.T) (conn.Profile, string) { return mytest.Oracle(t), mytest.OraclePassword }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, password := tc.start(t)
			body := mustJSON(tunnelled(p, password, b, nil))
			tr := decodeBody[conn.TestResult](t, e.do("POST", "/api/profiles/test", body))
			if !tr.OK {
				t.Fatalf("unsaved test: %+v", tr.Error)
			}
			w := e.do("POST", "/api/profiles", body)
			expect(t, w, 201, "")
			saved := decodeBody[conn.Profile](t, w)
			if saved.SSH == nil || saved.SSH.Host != b.Host || saved.Host != "host.docker.internal" {
				t.Fatalf("saved %s", w.Body.String())
			}
			if tr := decodeBody[conn.TestResult](t, e.do("POST", "/api/profiles/"+saved.ID+"/test", "")); !tr.OK {
				t.Fatalf("saved test: %+v", tr.Error)
			}
			expect(t, e.do("GET", "/api/profiles/"+saved.ID+"/schema", ""), 200, "")
			if saved.Capabilities.Health {
				expect(t, e.do("GET", "/api/profiles/"+saved.ID+"/health", ""), 200, "")
			}
		})
	}
}

func TestSSHTunnelProfiles(t *testing.T) {
	b := mytest.SSH(t)
	e := keyringEnv(t)
	sshHome(t, b)
	srv := mytest.Redis(t, "redis7")

	t.Run("unknown host key is an ssh error", func(t *testing.T) {
		tr := decodeBody[conn.TestResult](t, e.do("POST", "/api/profiles/test", mustJSON(tunnelled(srv, "", b,
			map[string]any{"host": "127.0.0.1", "port": b.Port, "user": b.User, "keyFile": b.KeyFile}))))
		if tr.OK || tr.Error == nil || tr.Error.Kind != "ssh" || !strings.Contains(tr.Error.Message, "is not in") {
			t.Fatalf("result %+v", tr.Error)
		}
	})

	t.Run("passphrase is kept in the vault and goes with the key file", func(t *testing.T) {
		block, _ := ssh.MarshalPrivateKeyWithPassphrase(b.Key, "", []byte("key-pass"))
		keyFile := filepath.Join(t.TempDir(), "id_enc")
		os.WriteFile(keyFile, pem.EncodeToMemory(block), 0o600)
		tunnel := map[string]any{"host": b.Host, "port": b.Port, "user": b.User, "keyFile": keyFile}
		body := tunnelled(srv, "", b, tunnel)
		body["sshPassphrase"] = "key-pass"
		w := e.do("POST", "/api/profiles", mustJSON(body))
		expect(t, w, 201, "")
		p := decodeBody[conn.Profile](t, w)
		if !p.SSH.HasPassphrase || strings.Contains(w.Body.String(), "key-pass") {
			t.Fatalf("created %s", w.Body.String())
		}
		if got, _, _ := e.vault.Get(sshVaultKey(p.ID)); got != "key-pass" {
			t.Fatalf("vault holds %q", got)
		}
		if tr := decodeBody[conn.TestResult](t, e.do("POST", "/api/profiles/"+p.ID+"/test", "")); !tr.OK {
			t.Fatalf("test: %+v", tr.Error)
		}

		// An update without a passphrase keeps it.
		delete(body, "sshPassphrase")
		body["name"] = "renamed"
		w = e.do("PUT", "/api/profiles/"+p.ID, mustJSON(body))
		expect(t, w, 200, "")
		if !decodeBody[conn.Profile](t, w).SSH.HasPassphrase {
			t.Fatalf("updated %s", w.Body.String())
		}
		expect(t, e.do("GET", "/api/profiles/"+p.ID+"/keyspace", ""), 200, "")

		// Dropping the tunnel drops the passphrase.
		delete(body, "ssh")
		w = e.do("PUT", "/api/profiles/"+p.ID, mustJSON(body))
		expect(t, w, 200, "")
		if decodeBody[conn.Profile](t, w).SSH != nil {
			t.Fatalf("updated %s", w.Body.String())
		}
		if _, ok, _ := e.vault.Get(sshVaultKey(p.ID)); ok {
			t.Error("the passphrase outlived the tunnel")
		}
	})

	t.Run("validation", func(t *testing.T) {
		for _, c := range []struct{ change func(map[string]any) string }{
			{func(m map[string]any) string { m["ssh"].(map[string]any)["user"] = ""; return "SSH user" }},
			{func(m map[string]any) string { m["ssh"].(map[string]any)["keyFile"] = "id_rsa"; return "absolute" }},
			{func(m map[string]any) string {
				m["engine"], m["user"], m["options"] = "mongodb", "u", url.Values{"srv": {"true"}}.Encode()
				return "srv"
			}},
			{func(m map[string]any) string { m["engine"], m["path"] = "sqlite", "/x.db"; return "SSH tunnel" }},
		} {
			body := tunnelled(srv, "", b, nil)
			want := c.change(body)
			w := e.do("POST", "/api/profiles", mustJSON(body))
			expect(t, w, 400, "validation")
			if !strings.Contains(w.Body.String(), want) {
				t.Errorf("%s: %s", want, w.Body.String())
			}
		}
	})
}

package mysql

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/dbscope/dbscope/internal/conn"
)

// ParseURL reads mysql://user:password@host:port/database?sslmode=... (also
// mariadb://) into a profile and its password. sslmode takes the profile's
// values; the default is prefer.
func ParseURL(raw string) (conn.Profile, string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return conn.Profile{}, "", errors.New("not a URL") // the URL may hold a password; never echo it
	}
	if u.Scheme != "mysql" && u.Scheme != "mariadb" {
		return conn.Profile{}, "", fmt.Errorf("the URL scheme must be mysql:// or mariadb://, not %s://", u.Scheme)
	}
	p := conn.Profile{Engine: conn.EngineMySQL, Host: u.Hostname(), Port: 3306, Database: strings.TrimPrefix(u.Path, "/"),
		SSLMode: conn.SSLPrefer, Env: conn.EnvLocal, StatementTimeoutMs: 30000}
	if s := u.Port(); s != "" {
		if p.Port, err = strconv.Atoi(s); err != nil {
			return conn.Profile{}, "", errors.New("the port must be a number")
		}
	}
	if m := u.Query().Get("sslmode"); m != "" {
		p.SSLMode = conn.SSLMode(m)
	}
	p.SSLRootCert = u.Query().Get("sslrootcert")
	password := ""
	if u.User != nil {
		p.User = u.User.Username()
		password, _ = u.User.Password()
	}
	if p.Host == "" || p.Database == "" || p.User == "" {
		return conn.Profile{}, "", errors.New("the URL needs a user, a host and a database")
	}
	return p, password, nil
}

// ParseServer reads a --mysql-version value: "8.4", "8.0.36" or "mariadb-11.8".
func ParseServer(v string) (Server, error) {
	flavor := "mysql"
	if rest, ok := strings.CutPrefix(strings.ToLower(v), "mariadb-"); ok {
		flavor, v = "mariadb", rest
	} else {
		v = strings.TrimPrefix(strings.ToLower(v), "mysql-")
	}
	s := parseVersion(v)
	if s.Num < 50000 {
		return Server{}, fmt.Errorf("%q is not a version like 8.4, 8.0.36 or mariadb-11.8", v)
	}
	s.Flavor = flavor
	return s, nil
}

package conn

import (
	"net/url"
	"strconv"
	"strings"
)

// ParsedURL is what a pasted connection URL contributes to the profile form.
type ParsedURL struct {
	Host     string  `json:"host"`
	Port     int     `json:"port"`
	Database string  `json:"database"`
	User     string  `json:"user"`
	Password string  `json:"password,omitempty"`
	SSLMode  SSLMode `json:"sslMode"`
}

// ParseURL reads a postgres:// or postgresql:// URL. Errors never quote the
// input because it may contain a password.
func ParseURL(raw string) (ParsedURL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ParsedURL{}, invalid("url is not a valid URL")
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return ParsedURL{}, invalid("url must start with postgres:// or postgresql://")
	}
	if strings.Contains(u.Host, ",") {
		return ParsedURL{}, invalid("url with multiple hosts is not supported")
	}
	p := ParsedURL{
		Host:     u.Hostname(),
		Port:     5432,
		Database: strings.TrimPrefix(u.Path, "/"),
		SSLMode:  SSLPrefer,
	}
	if ps := u.Port(); ps != "" {
		n, err := strconv.Atoi(ps)
		if err != nil || n < 1 || n > 65535 {
			return ParsedURL{}, invalid("port must be 1-65535")
		}
		p.Port = n
	}
	if u.User != nil {
		p.User = u.User.Username()
		p.Password, _ = u.User.Password()
	}
	if m := u.Query().Get("sslmode"); m != "" {
		p.SSLMode = SSLMode(m)
		if !p.SSLMode.valid() {
			return ParsedURL{}, invalid("sslmode must be one of disable, prefer, require, verify-ca, verify-full")
		}
	}
	return p, nil
}

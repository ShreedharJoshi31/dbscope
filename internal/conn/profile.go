// Package conn holds connection profiles, their validation, and everything
// that turns a profile into a live PostgreSQL connection.
package conn

import (
	"fmt"
	"math"
	"net"
	"strings"
	"time"
	"unicode/utf8"
)

type Env string

const (
	EnvLocal       Env = "local"
	EnvStaging     Env = "staging"
	EnvProdReplica Env = "prod-replica"
)

type SummaryMode string

const (
	SummaryOff     SummaryMode = "off"
	SummaryProfile SummaryMode = "profile"
	SummaryRows    SummaryMode = "rows"
)

type SSLMode string

const (
	SSLDisable    SSLMode = "disable"
	SSLPrefer     SSLMode = "prefer"
	SSLRequire    SSLMode = "require"
	SSLVerifyCA   SSLMode = "verify-ca"
	SSLVerifyFull SSLMode = "verify-full"
)

func (m SSLMode) valid() bool {
	switch m {
	case SSLDisable, SSLPrefer, SSLRequire, SSLVerifyCA, SSLVerifyFull:
		return true
	}
	return false
}

// Profile is a saved connection. The password lives in the secrets vault;
// HasPassword only records whether one is stored.
type Profile struct {
	ID                 string      `json:"id"`
	Name               string      `json:"name"`
	Host               string      `json:"host"`
	Port               int         `json:"port"`
	Database           string      `json:"database"`
	User               string      `json:"user"`
	SSLMode            SSLMode     `json:"sslMode"`
	SSLRootCert        string      `json:"sslRootCert,omitempty"`
	Env                Env         `json:"env"`
	Schemas            []string    `json:"schemas"`
	SummaryMode        SummaryMode `json:"summaryMode"`
	MaxRowsToLLM       int         `json:"maxRowsToLLM"`
	StatementTimeoutMs int         `json:"statementTimeoutMs"`
	// MaxCost is the EXPLAIN cost above which a query needs confirming;
	// 0 means no limit and nil means the env default (see CostLimit).
	MaxCost     *float64  `json:"maxCost,omitempty"`
	HasPassword bool      `json:"hasPassword"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// ProfileInput is the create/update/test request body. Pointer fields
// distinguish "omitted" (use the default, or keep the stored password) from
// an explicit value.
type ProfileInput struct {
	Name               string      `json:"name"`
	Host               string      `json:"host"`
	Port               *int        `json:"port"`
	Database           string      `json:"database"`
	User               string      `json:"user"`
	SSLMode            SSLMode     `json:"sslMode"`
	SSLRootCert        string      `json:"sslRootCert"`
	Env                Env         `json:"env"`
	Schemas            []string    `json:"schemas"`
	SummaryMode        SummaryMode `json:"summaryMode"`
	MaxRowsToLLM       *int        `json:"maxRowsToLLM"`
	StatementTimeoutMs *int        `json:"statementTimeoutMs"`
	MaxCost            *float64    `json:"maxCost"`
	Password           *string     `json:"password"`
}

type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// Profile validates the input and returns a profile with defaults applied.
// ID, HasPassword and timestamps are left for the caller to fill in.
func (in ProfileInput) Profile() (Profile, error) {
	p := Profile{
		Name:        strings.TrimSpace(in.Name),
		Host:        in.Host,
		Port:        5432,
		Database:    in.Database,
		User:        in.User,
		SSLMode:     in.SSLMode,
		SSLRootCert: in.SSLRootCert,
		Env:         in.Env,
		Schemas:     in.Schemas,
		SummaryMode: in.SummaryMode,
	}
	if n := utf8.RuneCountInString(p.Name); n < 1 || n > 80 {
		return Profile{}, invalid("name must be 1-80 characters")
	}
	if p.Host == "" {
		return Profile{}, invalid("host is required")
	}
	if strings.ContainsAny(p.Host, " \t\r\n,/") {
		return Profile{}, invalid("host must be a single hostname or IP address")
	}
	if in.Port != nil {
		p.Port = *in.Port
	}
	if p.Port < 1 || p.Port > 65535 {
		return Profile{}, invalid("port must be 1-65535")
	}
	if p.Database == "" {
		return Profile{}, invalid("database is required")
	}
	if p.User == "" {
		return Profile{}, invalid("user is required")
	}
	if p.SSLMode == "" {
		p.SSLMode = SSLPrefer
	}
	if !p.SSLMode.valid() {
		return Profile{}, invalid("sslMode must be one of disable, prefer, require, verify-ca, verify-full")
	}
	switch p.Env {
	case EnvLocal, EnvStaging, EnvProdReplica:
	case "":
		return Profile{}, invalid("env is required")
	default:
		return Profile{}, invalid("env must be one of local, staging, prod-replica")
	}
	if p.Schemas == nil {
		p.Schemas = []string{}
	}
	for _, s := range p.Schemas {
		if s == "" {
			return Profile{}, invalid("schemas must not contain empty names")
		}
	}
	switch p.SummaryMode {
	case "":
		p.SummaryMode = SummaryProfile
	case SummaryOff, SummaryProfile, SummaryRows:
	default:
		return Profile{}, invalid("summaryMode must be one of off, profile, rows")
	}
	p.MaxRowsToLLM = 50
	if in.MaxRowsToLLM != nil {
		p.MaxRowsToLLM = *in.MaxRowsToLLM
	}
	if p.MaxRowsToLLM < 1 || p.MaxRowsToLLM > 500 {
		return Profile{}, invalid("maxRowsToLLM must be 1-500")
	}
	p.StatementTimeoutMs = 30000
	if p.Env == EnvProdReplica {
		p.StatementTimeoutMs = 10000
	}
	if in.StatementTimeoutMs != nil {
		p.StatementTimeoutMs = *in.StatementTimeoutMs
	}
	if p.StatementTimeoutMs < 1 {
		return Profile{}, invalid("statementTimeoutMs must be a positive number of milliseconds")
	}
	if in.MaxCost != nil {
		if c := *in.MaxCost; c < 0 || math.IsNaN(c) || math.IsInf(c, 0) {
			return Profile{}, invalid("maxCost must be 0 (no limit) or a positive number")
		}
		c := *in.MaxCost
		p.MaxCost = &c
	}
	return p, nil
}

// Default EXPLAIN cost limits per env; local has none.
const (
	DefaultMaxCostProdReplica = 1e6
	DefaultMaxCostStaging     = 1e7
)

// CostLimit is the EXPLAIN total cost above which a query needs
// confirming, or 0 for no limit: the profile's maxCost when set, else the
// env default.
func (p Profile) CostLimit() float64 {
	if p.MaxCost != nil {
		return *p.MaxCost
	}
	switch p.Env {
	case EnvProdReplica:
		return DefaultMaxCostProdReplica
	case EnvStaging:
		return DefaultMaxCostStaging
	}
	return 0
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

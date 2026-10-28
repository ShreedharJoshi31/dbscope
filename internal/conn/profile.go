// Package conn holds connection profiles, their validation, and everything
// that turns a profile into a live PostgreSQL connection. Other engines'
// connections live in their own packages; profiles name the engine.
package conn

import (
	"fmt"
	"math"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Engine is the kind of database a profile connects to (docs/ENGINES.md).
type Engine string

const (
	EnginePostgres Engine = "postgres"
	EngineSQLite   Engine = "sqlite"
	// EngineMySQL covers MySQL and MariaDB; EngineMariaDB appears only in test
	// results, which report the server actually reached.
	EngineMySQL   Engine = "mysql"
	EngineMariaDB Engine = "mariadb"
	EngineMongoDB Engine = "mongodb"
	// EngineRedis covers Redis and Valkey; EngineValkey appears only in test results.
	EngineRedis  Engine = "redis"
	EngineValkey Engine = "valkey"
	// EngineDynamoDB reaches a region's DynamoDB with the AWS SDK's own credentials.
	EngineDynamoDB Engine = "dynamodb"
	// EngineCassandra covers Cassandra and ScyllaDB; EngineScylla appears only in test results.
	EngineCassandra Engine = "cassandra"
	EngineScylla    Engine = "scylladb"
	// EngineNeo4j reaches one Neo4j server (bolt) or a cluster (options routing=true).
	EngineNeo4j Engine = "neo4j"
)

// Capabilities are the modes an engine supports beyond the ERD, which every engine has.
type Capabilities struct {
	Query      bool `json:"query"`
	Ask        bool `json:"ask"`
	Migrations bool `json:"migrations"`
	Workload   bool `json:"workload"`
	// Keyspace is the key-pattern report of key-value stores.
	Keyspace bool `json:"keyspace"`
}

func (e Engine) Capabilities() Capabilities {
	switch e {
	case EnginePostgres:
		return Capabilities{Query: true, Ask: true, Migrations: true, Workload: true}
	case EngineSQLite:
		return Capabilities{Query: true, Migrations: true}
	case EngineMySQL:
		return Capabilities{Query: true, Migrations: true}
	case EngineMongoDB:
		return Capabilities{Query: true}
	case EngineRedis:
		return Capabilities{Keyspace: true}
	case EngineDynamoDB, EngineCassandra, EngineNeo4j:
		return Capabilities{Query: true}
	}
	return Capabilities{}
}

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
	ID     string `json:"id"`
	Name   string `json:"name"`
	Engine Engine `json:"engine"`
	// Options are connection-string options for engines that take them (MongoDB), as a URL query.
	Options string `json:"options,omitempty"`
	// Path is the database file of a file-based engine (SQLite); network engines leave it empty.
	Path               string      `json:"path,omitempty"`
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
	MaxCost     *float64 `json:"maxCost,omitempty"`
	HasPassword bool     `json:"hasPassword"`
	// Capabilities follow from Engine; they are sent so the UI need not know each engine.
	Capabilities Capabilities `json:"capabilities"`
	CreatedAt    time.Time    `json:"createdAt"`
	UpdatedAt    time.Time    `json:"updatedAt"`
}

// ProfileInput is the create/update/test request body. Pointer fields
// distinguish "omitted" (use the default, or keep the stored password) from
// an explicit value.
type ProfileInput struct {
	Name               string      `json:"name"`
	Engine             Engine      `json:"engine"`
	Path               string      `json:"path"`
	Options            string      `json:"options"`
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
		Engine:      in.Engine,
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
	switch p.Engine {
	case "":
		p.Engine = EnginePostgres
	case EnginePostgres, EngineSQLite, EngineMySQL, EngineMongoDB, EngineRedis, EngineDynamoDB, EngineCassandra, EngineNeo4j:
	default:
		return Profile{}, invalid("engine must be one of postgres, sqlite, mysql, mongodb, redis, dynamodb, cassandra, neo4j")
	}
	p.Capabilities = p.Engine.Capabilities()
	switch p.Engine {
	case EngineMySQL:
		p.Port = 3306
	case EngineMongoDB:
		p.Port = 27017
	case EngineCassandra:
		p.Port = 9042
	case EngineNeo4j:
		p.Port = 7687
		if p.Database == "" {
			p.Database = "neo4j"
		}
	case EngineRedis:
		p.Port = 6379
		// A Redis database is a number and the user is optional (the default user).
		if p.Database == "" {
			p.Database = "0"
		}
		if n, err := strconv.Atoi(p.Database); err != nil || n < 0 {
			return Profile{}, invalid("the database of a Redis profile is its number, such as 0")
		}
	}
	if err := p.connOptions(in); err != nil {
		return Profile{}, err
	}
	if p.Engine == EngineSQLite {
		if err := p.fileTarget(in); err != nil {
			return Profile{}, err
		}
	} else if p.Engine == EngineDynamoDB {
		if err := p.awsTarget(in); err != nil {
			return Profile{}, err
		}
	} else if err := p.networkTarget(in); err != nil {
		return Profile{}, err
	}
	if err := p.options(in); err != nil {
		return Profile{}, err
	}
	return p, nil
}

// connOptions validates driver options a connection string carries, which
// only MongoDB takes: a URL query such as "authSource=admin&replicaSet=rs0".
func (p *Profile) connOptions(in ProfileInput) error {
	p.Options = strings.TrimPrefix(strings.TrimSpace(in.Options), "?")
	if p.Options == "" {
		return nil
	}
	if p.Engine != EngineMongoDB && p.Engine != EngineDynamoDB && p.Engine != EngineCassandra && p.Engine != EngineNeo4j {
		return invalid("options are only for MongoDB, DynamoDB, Cassandra and Neo4j profiles")
	}
	q, err := url.ParseQuery(p.Options)
	if err != nil {
		return invalid("options must be a URL query like authSource=admin&replicaSet=rs0")
	}
	for k := range q {
		switch strings.ToLower(k) {
		case "password", "username", "authmechanismproperties":
			return invalid("options must not hold credentials; use the user and password fields")
		}
	}
	if p.Engine == EngineCassandra {
		for k := range q {
			if k != "dc" {
				return invalid("Cassandra options take dc (the local datacenter), not %s", k)
			}
		}
	}
	if p.Engine == EngineNeo4j {
		for k, v := range q {
			if k != "routing" || (v[0] != "true" && v[0] != "false") {
				return invalid("Neo4j options take routing=true (a cluster's neo4j:// routing), not %s", k)
			}
		}
	}
	return nil
}

// awsTarget validates a DynamoDB profile: everything is in options, a region,
// and optionally an AWS profile name and an endpoint (DynamoDB Local).
// Credentials come from the AWS SDK's chain, never from dbscope.
func (p *Profile) awsTarget(in ProfileInput) error {
	q, _ := url.ParseQuery(p.Options)
	if q.Get("region") == "" {
		return invalid("a DynamoDB profile needs region in its options, such as region=eu-west-1")
	}
	for k := range q {
		switch k {
		case "region", "profile", "endpoint":
		default:
			return invalid("DynamoDB options take region, profile and endpoint, not %s", k)
		}
	}
	if e := q.Get("endpoint"); e != "" {
		if u, err := url.Parse(e); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return invalid("endpoint must be an http(s) URL such as http://localhost:8000")
		}
	}
	if in.Password != nil && *in.Password != "" {
		return invalid("a DynamoDB profile stores no password; credentials come from the AWS profile or environment")
	}
	p.Host, p.Port, p.Database, p.User, p.SSLMode, p.SSLRootCert = "", 0, "", "", "", ""
	return nil
}

// fileTarget validates a file-based profile: an absolute path and no network fields.
func (p *Profile) fileTarget(in ProfileInput) error {
	p.Path = filepath.Clean(strings.TrimSpace(in.Path))
	if in.Path == "" || !filepath.IsAbs(p.Path) {
		return invalid("path must be the absolute path of the database file")
	}
	if in.Password != nil && *in.Password != "" {
		return invalid("a %s database has no password", p.Engine)
	}
	p.Host, p.Port, p.Database, p.User, p.SSLMode, p.SSLRootCert = "", 0, "", "", "", ""
	return nil
}

func (p *Profile) networkTarget(in ProfileInput) error {
	if in.Path != "" {
		return invalid("path is only for file-based engines")
	}
	if p.Host == "" {
		return invalid("host is required")
	}
	if strings.ContainsAny(p.Host, " \t\r\n,/") {
		return invalid("host must be a single hostname or IP address")
	}
	if in.Port != nil {
		p.Port = *in.Port
	}
	if p.Port < 1 || p.Port > 65535 {
		return invalid("port must be 1-65535")
	}
	if p.Database == "" {
		return invalid("database is required")
	}
	// Redis has a default user, and Cassandra or Neo4j may run without authentication.
	if p.User == "" && p.Engine != EngineRedis && p.Engine != EngineCassandra && p.Engine != EngineNeo4j {
		return invalid("user is required")
	}
	if p.SSLMode == "" {
		p.SSLMode = SSLPrefer
	}
	if !p.SSLMode.valid() {
		return invalid("sslMode must be one of disable, prefer, require, verify-ca, verify-full")
	}
	return nil
}

// options validates the settings every engine shares.
func (p *Profile) options(in ProfileInput) error {
	switch p.Env {
	case EnvLocal, EnvStaging, EnvProdReplica:
	case "":
		return invalid("env is required")
	default:
		return invalid("env must be one of local, staging, prod-replica")
	}
	if p.Schemas == nil {
		p.Schemas = []string{}
	}
	for _, s := range p.Schemas {
		if s == "" {
			return invalid("schemas must not contain empty names")
		}
	}
	switch p.SummaryMode {
	case "":
		p.SummaryMode = SummaryProfile
	case SummaryOff, SummaryProfile, SummaryRows:
	default:
		return invalid("summaryMode must be one of off, profile, rows")
	}
	p.MaxRowsToLLM = 50
	if in.MaxRowsToLLM != nil {
		p.MaxRowsToLLM = *in.MaxRowsToLLM
	}
	if p.MaxRowsToLLM < 1 || p.MaxRowsToLLM > 500 {
		return invalid("maxRowsToLLM must be 1-500")
	}
	p.StatementTimeoutMs = 30000
	if p.Env == EnvProdReplica {
		p.StatementTimeoutMs = 10000
	}
	if in.StatementTimeoutMs != nil {
		p.StatementTimeoutMs = *in.StatementTimeoutMs
	}
	if p.StatementTimeoutMs < 1 {
		return invalid("statementTimeoutMs must be a positive number of milliseconds")
	}
	if in.MaxCost != nil {
		if c := *in.MaxCost; c < 0 || math.IsNaN(c) || math.IsInf(c, 0) {
			return invalid("maxCost must be 0 (no limit) or a positive number")
		}
		c := *in.MaxCost
		p.MaxCost = &c
	}
	return nil
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

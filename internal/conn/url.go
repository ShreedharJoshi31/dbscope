package conn

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// ParsedURL is what a pasted connection URL contributes to the profile form.
type ParsedURL struct {
	Engine   Engine  `json:"engine"`
	Host     string  `json:"host"`
	Port     int     `json:"port"`
	Database string  `json:"database"`
	User     string  `json:"user"`
	Password string  `json:"password,omitempty"`
	SSLMode  SSLMode `json:"sslMode"`
	// Options is the profile's options query (MongoDB's authSource, Neo4j's routing).
	Options string `json:"options,omitempty"`
	// Notes say what the URL held that the profile cannot, such as extra hosts.
	Notes []string `json:"notes,omitempty"`
}

// Schemes ParseURL reads, by engine, for the form's hint.
var urlEngines = map[string]Engine{
	"postgres": EnginePostgres, "postgresql": EnginePostgres,
	"mysql": EngineMySQL, "mariadb": EngineMySQL,
	"mongodb": EngineMongoDB, "mongodb+srv": EngineMongoDB,
	"redis": EngineRedis, "rediss": EngineRedis,
	"neo4j": EngineNeo4j, "neo4j+s": EngineNeo4j, "neo4j+ssc": EngineNeo4j,
	"bolt": EngineNeo4j, "bolt+s": EngineNeo4j, "bolt+ssc": EngineNeo4j,
	"clickhouse": EngineClickHouse,
	"sqlserver":  EngineSQLServer, "mssql": EngineSQLServer,
	"http": EngineElasticsearch, "https": EngineElasticsearch,
}

// mongoCredentialOptions are URI options that carry or select credentials;
// the profile keeps the password in the vault and never in options.
var mongoCredentialOptions = map[string]bool{"authmechanismproperties": true, "username": true, "password": true}

// ParseURL reads a connection URL of any engine with one (PostgreSQL, MySQL,
// MariaDB, MongoDB, Redis, Neo4j, ClickHouse, SQL Server, Elasticsearch over http(s)).
// Errors never quote the input because it may contain a password.
func ParseURL(raw string) (ParsedURL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ParsedURL{}, invalid("url is not a valid URL")
	}
	scheme := strings.ToLower(u.Scheme)
	engine, ok := urlEngines[scheme]
	if !ok {
		return ParsedURL{}, invalid("url must start with postgres://, mysql://, mongodb://, redis://, neo4j://, bolt://, clickhouse://, sqlserver:// or http(s):// for Elasticsearch")
	}
	p := ParsedURL{Engine: engine, Database: strings.TrimPrefix(u.Path, "/"), SSLMode: SSLPrefer}
	host := u.Host
	if strings.Contains(host, ",") {
		// MongoDB lists a replica set's members; the driver finds the others from one.
		if engine != EngineMongoDB {
			return ParsedURL{}, invalid("url with multiple hosts is not supported")
		}
		first, _, _ := strings.Cut(host, ",")
		p.Notes = append(p.Notes, "the URL lists several hosts; the profile keeps the first, and the driver finds the replica set's other members from it")
		host = first
	}
	hu, err := url.Parse("x://" + host)
	if err != nil {
		return ParsedURL{}, invalid("url host is not valid")
	}
	p.Host = hu.Hostname()
	p.Port = defaultPorts[engine]
	if ps := hu.Port(); ps != "" {
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
	q := u.Query()
	switch engine {
	case EnginePostgres:
		if m := q.Get("sslmode"); m != "" {
			p.SSLMode = SSLMode(m)
		}
	case EngineMySQL:
		switch strings.ToUpper(q.Get("ssl-mode") + q.Get("sslmode")) {
		case "DISABLED", "DISABLE":
			p.SSLMode = SSLDisable
		case "REQUIRED", "REQUIRE":
			p.SSLMode = SSLRequire
		case "VERIFY_CA":
			p.SSLMode = SSLVerifyCA
		case "VERIFY_IDENTITY":
			p.SSLMode = SSLVerifyFull
		}
		if t := q.Get("tls"); t == "true" {
			p.SSLMode = SSLVerifyFull
		} else if t == "skip-verify" {
			p.SSLMode = SSLRequire
		}
	case EngineMongoDB:
		opts := url.Values{}
		keys := make([]string, 0, len(q))
		for k := range q {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			lk := strings.ToLower(k)
			switch {
			case mongoCredentialOptions[lk]:
				p.Notes = append(p.Notes, "the option "+k+" is not kept: credentials go in the user and password fields")
			case lk == "tls" || lk == "ssl":
				if q.Get(k) == "true" {
					p.SSLMode = SSLVerifyFull
				}
			case lk == "tlsallowinvalidcertificates" || lk == "tlsinsecure":
				if q.Get(k) == "true" {
					p.SSLMode = SSLRequire
				}
			default:
				opts[k] = q[k]
			}
		}
		if scheme == "mongodb+srv" {
			opts.Set("srv", "true")
			p.Port = 27017
			if p.SSLMode == SSLPrefer {
				p.SSLMode = SSLVerifyFull // mongodb+srv implies TLS
			}
		}
		p.Options = opts.Encode()
	case EngineRedis:
		if scheme == "rediss" {
			p.SSLMode = SSLVerifyFull
		}
		if p.Database == "" {
			p.Database = "0"
		}
	case EngineNeo4j:
		if strings.HasPrefix(scheme, "neo4j") {
			p.Options = "routing=true"
		}
		switch {
		case strings.HasSuffix(scheme, "+ssc"):
			p.SSLMode = SSLRequire
		case strings.HasSuffix(scheme, "+s"):
			p.SSLMode = SSLVerifyFull
		default:
			p.SSLMode = SSLDisable
		}
	case EngineClickHouse:
		if q.Get("secure") == "true" {
			p.SSLMode = SSLVerifyFull
			if u.Port() == "" {
				p.Port = 9440
			}
			if q.Get("skip_verify") == "true" {
				p.SSLMode = SSLRequire
			}
		} else {
			p.SSLMode = SSLDisable
		}
	case EngineSQLServer:
		// go-mssqldb's URL form: the database is a query option and a path names an instance.
		if p.Database != "" {
			p.Notes = append(p.Notes, "the URL names the instance "+p.Database+"; dbscope connects by port, so set the port the instance listens on")
		}
		p.Database = getFold(q, "database")
		switch strings.ToLower(getFold(q, "encrypt")) {
		case "disable", "false", "no", "0", "optional":
			p.SSLMode = SSLDisable
		case "true", "yes", "1", "mandatory", "strict":
			p.SSLMode = SSLVerifyFull
		}
		if t := strings.ToLower(getFold(q, "trustservercertificate")); t == "true" && p.SSLMode != SSLDisable {
			p.SSLMode = SSLRequire
		}
	case EngineElasticsearch:
		// The path, if any, is the index pattern.
		if scheme == "https" {
			p.SSLMode = SSLVerifyFull
		} else {
			p.SSLMode = SSLDisable
		}
	}
	if !p.SSLMode.valid() {
		return ParsedURL{}, invalid("sslmode must be one of disable, prefer, require, verify-ca, verify-full")
	}
	return p, nil
}

var defaultPorts = map[Engine]int{
	EnginePostgres: 5432, EngineMySQL: 3306, EngineMongoDB: 27017, EngineRedis: 6379,
	EngineNeo4j: 7687, EngineClickHouse: 9000, EngineElasticsearch: 9200, EngineSQLServer: 1433,
}

// getFold reads a query option whose name is case-insensitive, as go-mssqldb's are.
func getFold(q url.Values, name string) string {
	for k, v := range q {
		if strings.EqualFold(k, name) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

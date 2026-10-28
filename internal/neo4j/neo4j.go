// Package neo4j maps a Neo4j database's graph for dbscope (docs/ENGINES.md):
// node labels with their properties and counts, the relationship types that
// connect them, indexes and constraints; and it runs read-only Cypher with
// its EXPLAIN plan checked for scans, cartesian products and unbounded paths.
package neo4j

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	drv "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j/config"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j/dbtype"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/schema"
)

// Client is a driver and the database it reads.
type Client struct {
	Driver   drv.Driver
	Database string
}

// Close releases the driver's connections.
func (c *Client) Close() { c.Driver.Close(context.Background()) }

// target is the URI for the profile: bolt:// talks to one server, neo4j://
// (options routing=true) asks a cluster for its routing table. sslMode
// require trusts any certificate (+ssc); verify-ca and verify-full check it (+s).
func target(p conn.Profile) (string, error) {
	q, err := url.ParseQuery(p.Options)
	if err != nil {
		return "", err
	}
	scheme := "bolt"
	if q.Get("routing") == "true" {
		scheme = "neo4j"
	}
	switch p.SSLMode {
	case conn.SSLRequire:
		scheme += "+ssc"
	case conn.SSLVerifyCA, conn.SSLVerifyFull:
		scheme += "+s"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, p.Host, p.Port), nil
}

// Connect builds a driver for the profile; it opens connections lazily.
func Connect(p conn.Profile, password string) (*Client, error) {
	uri, err := target(p)
	if err != nil {
		return nil, err
	}
	auth := drv.NoAuth()
	if p.User != "" {
		auth = drv.BasicAuth(p.User, password, "")
	}
	cfgs, err := conn.TLSConfigs(p)
	if err != nil {
		return nil, err
	}
	d, err := drv.NewDriver(uri, auth, func(c *config.Config) {
		c.TelemetryDisabled = true
		c.SocketConnectTimeout = 10 * time.Second
		c.MaxConnectionPoolSize = 4
		c.MaxTransactionRetryTime = 0 // user queries are never retried
		if cfgs[0] != nil && cfgs[0].RootCAs != nil {
			c.TlsConfig = cfgs[0]
		}
	})
	if err != nil {
		return nil, err
	}
	db := p.Database
	if db == "" {
		db = "neo4j"
	}
	return &Client{Driver: d, Database: db}, nil
}

// read runs one statement in a read transaction that is always rolled back,
// and returns its records and summary.
func (c *Client) read(ctx context.Context, timeout time.Duration, cypher string, params map[string]any) ([]*drv.Record, drv.ResultSummary, error) {
	s := c.Driver.NewSession(ctx, drv.SessionConfig{AccessMode: drv.AccessModeRead, DatabaseName: c.Database})
	defer s.Close(ctx)
	tx, err := s.BeginTransaction(ctx, drv.WithTxTimeout(timeout))
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)
	r, err := tx.Run(ctx, cypher, params)
	if err != nil {
		return nil, nil, err
	}
	recs, err := r.Collect(ctx)
	if err != nil {
		return nil, nil, err
	}
	sum, err := r.Consume(ctx)
	return recs, sum, err
}

// Server is the Neo4j version and edition.
type Server struct {
	Version   string
	Edition   string // community, enterprise
	Community bool
}

func describe(ctx context.Context, c *Client) (Server, error) {
	recs, _, err := c.read(ctx, 10*time.Second, "CALL dbms.components() YIELD name, versions, edition RETURN versions[0] AS v, edition", nil)
	if err != nil {
		return Server{}, err
	}
	var srv Server
	if len(recs) > 0 {
		v, _ := recs[0].Get("v")
		e, _ := recs[0].Get("edition")
		srv.Version, _ = v.(string)
		srv.Edition, _ = e.(string)
	}
	srv.Community = srv.Edition == "community"
	return srv, nil
}

// writeRoles are built-in roles whose holders may change data or schema.
var writeRoles = map[string]bool{"admin": true, "architect": true, "publisher": true, "editor": true}

// Test connects and reports the server and what the user may change.
// Community Edition has no roles, so every user can write; dbscope then
// relies on its own checks and read transactions that are never committed.
func Test(ctx context.Context, p conn.Profile, password string) conn.TestResult {
	res := conn.TestResult{Engine: conn.EngineNeo4j, Warnings: []string{}}
	if (p.SSLMode == conn.SSLDisable || p.SSLMode == conn.SSLPrefer) && p.Host != "localhost" && p.Host != "127.0.0.1" && p.Host != "::1" {
		res.Warnings = append(res.Warnings, "the connection is not encrypted (Bolt has no \"TLS if offered\"), so the password and results travel in plain text; use sslMode require or verify-full.")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, err := Connect(p, password)
	if err != nil {
		res.Error = classify(err)
		return res
	}
	defer c.Close()
	start := time.Now()
	if err := c.Driver.VerifyConnectivity(ctx); err != nil {
		res.Error = classify(err)
		return res
	}
	srv, err := describe(ctx, c)
	if err != nil {
		res.Error = classify(err)
		return res
	}
	ms := time.Since(start).Milliseconds()
	res.OK, res.LatencyMs, res.ServerVersion = true, &ms, srv.Version+" "+srv.Edition
	recs, _, err := c.read(ctx, 10*time.Second, "SHOW CURRENT USER YIELD roles", nil)
	switch {
	case err != nil:
		res.Warnings = append(res.Warnings, "dbscope could not read this user's roles: "+err.Error())
	case srv.Community:
		res.Warnings = append(res.Warnings, "Neo4j Community Edition has no roles, so every user can write; dbscope refuses writing Cypher and rolls back every transaction, but cannot make the user read-only (docs/ENGINES.md)")
	case len(recs) > 0:
		roles, _ := recs[0].Get("roles")
		list, _ := roles.([]any)
		for _, r := range list {
			if s, _ := r.(string); writeRoles[s] {
				res.WritePrivileges = append(res.WritePrivileges, "role "+s)
			}
		}
	}
	return res
}

func classify(err error) *conn.TestError {
	var ne *drv.Neo4jError
	if errors.As(err, &ne) {
		kind := "database"
		switch {
		case strings.HasPrefix(ne.Code, "Neo.ClientError.Security."):
			kind = "auth"
		case strings.Contains(ne.Code, "TransactionTimedOut"):
			kind = "timeout"
		}
		return &conn.TestError{Kind: kind, Message: ne.Msg, SQLState: ne.Code}
	}
	var ce *drv.ConnectivityError
	if errors.As(err, &ce) {
		return &conn.TestError{Kind: "network", Message: err.Error()}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &conn.TestError{Kind: "timeout", Message: err.Error()}
	}
	return conn.Classify(err)
}

// probeTimeout bounds each check that a relationship pattern exists.
const probeTimeout = 2 * time.Second

// Introspect maps the database's graph: each node label becomes a card with
// its properties, count and outgoing relationship types; an edge joins two
// labels for each relationship type seen between them.
func Introspect(ctx context.Context, c *Client) (*schema.Model, error) {
	start := time.Now()
	srv, err := describe(ctx, c)
	if err != nil {
		return nil, err
	}
	m := &schema.Model{Engine: "neo4j", Schemas: []string{c.Database}, Relations: []schema.Relation{}, Enums: []schema.Enum{}}
	labels, err := c.strings(ctx, "CALL db.labels() YIELD label RETURN label ORDER BY label")
	if err != nil {
		return nil, fmt.Errorf("reading labels: %w", err)
	}
	byLabel := map[string]*schema.Relation{}
	for _, l := range labels {
		r := &schema.Relation{Schema: c.Database, Name: l, Kind: schema.KindNodeLabel, Columns: []schema.Column{}}
		if n, err := c.count(ctx, "MATCH (n:"+quote(l)+") RETURN count(n)"); err == nil {
			r.RowEstimate = &n
			if n == 0 {
				r.Comment = "token only: no node has this label"
			}
		}
		byLabel[l] = r
	}

	// Property types come from Java names under Cypher 5 and Cypher names under Cypher 25.
	recs, _, err := c.read(ctx, 30*time.Second, "CALL db.schema.nodeTypeProperties() YIELD nodeLabels, propertyName, propertyTypes, mandatory RETURN nodeLabels, propertyName, propertyTypes, mandatory", nil)
	if err != nil {
		return nil, fmt.Errorf("reading node properties: %w", err)
	}
	for _, rec := range recs {
		ls, _ := rec.Get("nodeLabels")
		name, _ := rec.Get("propertyName")
		types, _ := rec.Get("propertyTypes")
		mandatory, _ := rec.Get("mandatory")
		prop, _ := name.(string)
		if prop == "" {
			continue // a label combination with no properties
		}
		for _, l := range toStrings(ls) {
			if r := byLabel[l]; r != nil {
				addProperty(r, prop, typeText(toStrings(types)), mandatory == true)
			}
		}
	}

	if err := c.addRelationships(ctx, byLabel); err != nil {
		return nil, err
	}
	if err := c.addIndexes(ctx, byLabel); err != nil {
		return nil, err
	}
	for _, l := range labels {
		m.Relations = append(m.Relations, *byLabel[l])
	}
	m.ServerVersion = versionNum(srv.Version)
	m.IntrospectedAt, m.DurationMs = start.UTC(), time.Since(start).Milliseconds()
	return m, nil
}

// addRelationships adds a row per outgoing relationship type to each label
// and an edge to the label at its other end. db.schema.visualization reads
// the count store and can list combinations no relationship has, so each is
// checked with a bounded pattern match; one that cannot be checked in time
// is kept and marked unchecked.
func (c *Client) addRelationships(ctx context.Context, byLabel map[string]*schema.Relation) error {
	props := map[string][]string{}
	recs, _, err := c.read(ctx, 30*time.Second, "CALL db.schema.relTypeProperties() YIELD relType, propertyName, propertyTypes RETURN relType, propertyName, propertyTypes", nil)
	if err != nil {
		return fmt.Errorf("reading relationship properties: %w", err)
	}
	for _, rec := range recs {
		rt, _ := rec.Get("relType")
		name, _ := rec.Get("propertyName")
		ts, _ := rec.Get("propertyTypes")
		t, _ := rt.(string)
		t = strings.Trim(strings.TrimPrefix(t, ":"), "`")
		if n, _ := name.(string); n != "" {
			props[t] = append(props[t], n+" "+typeText(toStrings(ts)))
		}
	}

	recs, _, err = c.read(ctx, 30*time.Second, "CALL db.schema.visualization() YIELD nodes, relationships RETURN nodes, relationships", nil)
	if err != nil {
		return fmt.Errorf("reading the schema graph: %w", err)
	}
	type triple struct{ from, typ, to string }
	var triples []triple
	if len(recs) > 0 {
		ns, _ := recs[0].Get("nodes")
		rs, _ := recs[0].Get("relationships")
		names := map[string]string{}
		for _, n := range toSlice(ns) {
			if node, ok := n.(dbtype.Node); ok {
				name, _ := node.Props["name"].(string)
				names[node.ElementId] = name
			}
		}
		for _, r := range toSlice(rs) {
			if rel, ok := r.(dbtype.Relationship); ok {
				triples = append(triples, triple{names[rel.StartElementId], rel.Type, names[rel.EndElementId]})
			}
		}
	}
	sort.Slice(triples, func(i, j int) bool {
		a, b := triples[i], triples[j]
		return a.from+"\x00"+a.typ+"\x00"+a.to < b.from+"\x00"+b.typ+"\x00"+b.to
	})
	for _, t := range triples {
		from, to := byLabel[t.from], byLabel[t.to]
		if from == nil || to == nil {
			continue
		}
		recs, _, err := c.read(ctx, probeTimeout, "MATCH (:"+quote(t.from)+")-[:"+quote(t.typ)+"]->(:"+quote(t.to)+") RETURN 1 LIMIT 1", nil)
		checked := err == nil
		if checked && len(recs) == 0 {
			continue
		}
		row := "-[:" + t.typ + "]->"
		if from.Column(row+" "+t.to) != nil {
			continue
		}
		comment := "relationship to " + t.to
		if n, err := c.count(ctx, "MATCH (:"+quote(t.from)+")-[r:"+quote(t.typ)+"]->() RETURN count(r)"); err == nil {
			comment += fmt.Sprintf(", %d from %s", n, t.from)
		}
		if len(props[t.typ]) > 0 {
			comment += "; properties: " + strings.Join(props[t.typ], ", ")
		}
		if !checked {
			comment += "; from count statistics, not checked"
		}
		col := row + " " + t.to
		from.Columns = append(from.Columns, schema.Column{Name: col, Type: t.to, Nullable: true, Comment: comment})
		from.ForeignKeys = append(from.ForeignKeys, schema.ForeignKey{Name: t.typ + " to " + t.to, Columns: []string{col},
			Ref: schema.QualifiedName{Schema: from.Schema, Name: t.to}, RefCols: []string{""}, Validated: checked})
	}
	return nil
}

// addIndexes attaches range, text, point, full-text and vector indexes to
// their labels; uniqueness and key constraints become uniques and keys.
func (c *Client) addIndexes(ctx context.Context, byLabel map[string]*schema.Relation) error {
	recs, _, err := c.read(ctx, 10*time.Second, "SHOW INDEXES YIELD name, type, entityType, labelsOrTypes, properties, state, owningConstraint RETURN *", nil)
	if err != nil {
		return fmt.Errorf("reading indexes: %w", err)
	}
	for _, rec := range recs {
		m := rec.AsMap()
		if m["entityType"] != "NODE" {
			continue
		}
		props := toStrings(m["properties"])
		for _, l := range toStrings(m["labelsOrTypes"]) {
			r := byLabel[l]
			if r == nil || len(props) == 0 {
				continue
			}
			typ, _ := m["type"].(string)
			name, _ := m["name"].(string)
			r.Indexes = append(r.Indexes, schema.Index{Name: name, Columns: props, Method: strings.ToLower(typ),
				Unique: m["owningConstraint"] != nil, Valid: m["state"] == "ONLINE"})
		}
	}
	recs, _, err = c.read(ctx, 10*time.Second, "SHOW CONSTRAINTS YIELD name, type, entityType, labelsOrTypes, properties RETURN *", nil)
	if err != nil {
		return fmt.Errorf("reading constraints: %w", err)
	}
	for _, rec := range recs {
		m := rec.AsMap()
		if m["entityType"] != "NODE" {
			continue
		}
		props := toStrings(m["properties"])
		typ, _ := m["type"].(string)
		for _, l := range toStrings(m["labelsOrTypes"]) {
			r := byLabel[l]
			if r == nil {
				continue
			}
			switch typ {
			case "UNIQUENESS", "NODE_PROPERTY_UNIQUENESS":
				r.Uniques = append(r.Uniques, props)
			case "NODE_KEY":
				r.PrimaryKey = props
			case "NODE_PROPERTY_EXISTENCE":
				for _, p := range props {
					if col := r.Column(p); col != nil {
						col.Nullable = false
					}
				}
			}
		}
	}
	return nil
}

// addProperty adds a property column, or widens its types when another
// label combination carries it too.
func addProperty(r *schema.Relation, name, typ string, mandatory bool) {
	if col := r.Column(name); col != nil {
		if !strings.Contains(col.Type, typ) {
			col.Type += " | " + typ
		}
		col.Nullable = col.Nullable || !mandatory
		return
	}
	r.Columns = append(r.Columns, schema.Column{Name: name, Type: typ, Nullable: !mandatory})
}

// javaTypes maps Cypher 5's Java type names to Cypher's.
var javaTypes = map[string]string{"String": "string", "Long": "integer", "Double": "float", "Boolean": "boolean", "Date": "date",
	"DateTime": "zoned datetime", "LocalDateTime": "local datetime", "LocalTime": "local time", "Time": "zoned time",
	"Duration": "duration", "Point": "point", "StringArray": "list<string>", "LongArray": "list<integer>",
	"DoubleArray": "list<float>", "BooleanArray": "list<boolean>"}

func typeText(types []string) string {
	out := make([]string, 0, len(types))
	for _, t := range types {
		if j, ok := javaTypes[t]; ok {
			t = j
		}
		out = append(out, strings.ToLower(strings.TrimSuffix(t, " NOT NULL")))
	}
	return strings.Join(out, " | ")
}

func (c *Client) strings(ctx context.Context, cypher string) ([]string, error) {
	recs, _, err := c.read(ctx, 10*time.Second, cypher, nil)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		if s, ok := r.Values[0].(string); ok {
			out = append(out, s)
		}
	}
	return out, nil
}

func (c *Client) count(ctx context.Context, cypher string) (int64, error) {
	recs, _, err := c.read(ctx, 10*time.Second, cypher, nil)
	if err != nil || len(recs) == 0 {
		return 0, err
	}
	n, _ := recs[0].Values[0].(int64)
	return n, nil
}

// quote writes a label or type as a Cypher identifier.
func quote(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

func toSlice(v any) []any { s, _ := v.([]any); return s }

func toStrings(v any) []string {
	var out []string
	for _, x := range toSlice(v) {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// versionNum is major*10000 + minor*100 + patch for 5.26.32 or 2026.10.1.
func versionNum(v string) int {
	var a, b, c int
	fmt.Sscanf(v, "%d.%d.%d", &a, &b, &c)
	return a*10000 + b*100 + c
}

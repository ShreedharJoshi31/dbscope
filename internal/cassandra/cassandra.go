// Package cassandra maps Cassandra and ScyllaDB keyspaces for dbscope
// (docs/ENGINES.md) and runs read-only CQL SELECTs, telling whether each
// reads one partition or scans the cluster.
package cassandra

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	gocql "github.com/apache/cassandra-gocql-driver/v2"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/schema"
)

// Connect opens a session reading at LOCAL_ONE: a read at a higher
// consistency level can trigger blocking read repair, which writes.
// Profile.Database is the keyspace; Options may name the local datacenter (dc).
func Connect(p conn.Profile, password string) (*gocql.Session, error) {
	q, err := url.ParseQuery(p.Options)
	if err != nil {
		return nil, err
	}
	c := gocql.NewCluster(p.Host)
	c.Port = p.Port
	c.Consistency = gocql.LocalOne
	c.Timeout = 12 * time.Second // above the servers' 10 s range-scan timeout
	c.ConnectTimeout = 10 * time.Second
	c.NumConns = 1
	c.Keyspace = p.Database // a session keyspace: ScyllaDB lacks protocol v5's per-query keyspace
	if p.Origin != nil {
		// Through a tunnel only this node is reachable: don't learn peers, and
		// send the addresses the node reports for itself back to the tunnel.
		c.DisableInitialHostLookup = true
		local := net.ParseIP(p.Host)
		c.AddressTranslator = gocql.AddressTranslatorFunc(func(net.IP, int) (net.IP, int) { return local, p.Port })
	}
	if dc := q.Get("dc"); dc != "" {
		c.PoolConfig.HostSelectionPolicy = gocql.TokenAwareHostPolicy(gocql.DCAwareRoundRobinPolicy(dc))
	} else {
		c.PoolConfig.HostSelectionPolicy = gocql.TokenAwareHostPolicy(gocql.RoundRobinHostPolicy())
	}
	if p.User != "" {
		c.Authenticator = gocql.PasswordAuthenticator{Username: p.User, Password: password,
			AllowedAuthenticators: []string{"org.apache.cassandra.auth.PasswordAuthenticator", "com.scylladb.auth.TransitionalAuthenticator"}}
	}
	cfgs, err := conn.TLSConfigs(p)
	if err != nil {
		return nil, err
	}
	if cfgs[0] != nil && len(cfgs) == 1 { // CQL has no "TLS if offered": prefer means plaintext
		c.SslOpts = &gocql.SslOptions{Config: cfgs[0]}
	}
	return c.CreateSession()
}

// Server is what dbscope learns about the node it talks to.
type Server struct {
	Flavor  string // "cassandra" or "scylla"
	Version string
}

// describe reads the release; ScyllaDB reports release_version 3.0.8, and its
// real version is in system.versions, which a plain role may not read.
func describe(ctx context.Context, s *gocql.Session) (Server, error) {
	srv := Server{Flavor: "cassandra"}
	if err := s.Query("SELECT release_version FROM system.local").ScanContext(ctx, &srv.Version); err != nil {
		return srv, err
	}
	var v string
	err := s.Query("SELECT version FROM system.versions").ScanContext(ctx, &v)
	switch {
	case err == nil:
		srv.Flavor, srv.Version = "scylla", v
	case strings.Contains(err.Error(), "system.versions"): // exists but not granted: only ScyllaDB has it
		srv.Flavor, srv.Version = "scylla", ""
	}
	return srv, nil
}

// Test connects and reports the server and what the role may change. Grants
// are dbscope's only write barrier, so a cluster with AllowAllAuthorizer,
// the default, gets a warning.
func Test(ctx context.Context, p conn.Profile, password string) conn.TestResult {
	res := conn.TestResult{Engine: conn.EngineCassandra, Warnings: []string{}}
	if p.SSLMode == conn.SSLDisable && !conn.Loopback(p.Endpoint().Host) {
		res.Warnings = append(res.Warnings, "sslMode is disable on a non-loopback host, so the password and results travel unencrypted.")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	start := time.Now()
	ks := p.Database
	p.Database = "" // connect without it, so a missing keyspace is a warning, not a failed connection
	s, err := Connect(p, password)
	if err != nil {
		res.Error = classify(err)
		return res
	}
	defer s.Close()
	srv, err := describe(ctx, s)
	if err != nil {
		res.Error = classify(err)
		return res
	}
	ms := time.Since(start).Milliseconds()
	res.OK, res.LatencyMs, res.ServerVersion = true, &ms, srv.Version
	if srv.Flavor == "scylla" {
		res.Engine = conn.EngineScylla
		if srv.Version == "" {
			res.ServerVersion = "(not readable by this role)"
		}
	}
	var name string
	if err := s.Query("SELECT keyspace_name FROM system_schema.keyspaces WHERE keyspace_name = ?", ks).ScanContext(ctx, &name); err != nil {
		res.Warnings = append(res.Warnings, "keyspace "+ks+" was not found")
	}
	res.Warnings = append(res.Warnings, writeAccess(ctx, s, p.User, &res)...)
	return res
}

// writeAccess lists the role's write grants with LIST, which a role may run
// on itself; under AllowAllAuthorizer LIST fails and every user may write.
func writeAccess(ctx context.Context, s *gocql.Session, user string, res *conn.TestResult) []string {
	allowAll := "the cluster does not enforce grants (AllowAllAuthorizer, the default), so any connection can write and dbscope's statement check is the only guard; enable CassandraAuthorizer and use a role with only SELECT (docs/ENGINES.md)"
	if user == "" {
		return []string{allowAll}
	}
	var super bool
	iter := s.Query("LIST ROLES OF " + quoteIdent(user)).IterContext(ctx)
	row := map[string]any{}
	for iter.MapScan(row) {
		if b, ok := row["super"].(bool); ok && b {
			super = true
		}
		row = map[string]any{}
	}
	if err := iter.Close(); err != nil {
		// Under AllowAllAuthorizer, LIST fails as "not supported" or, with no authenticator, as an anonymous request.
		if e := strings.ToLower(err.Error()); strings.Contains(e, "allowall") || strings.Contains(e, "not supported") || strings.Contains(e, "not anonymous") {
			return []string{allowAll}
		}
		return []string{"dbscope could not list this role's grants: " + err.Error()}
	}
	if super {
		res.WritePrivileges = append(res.WritePrivileges, "SUPERUSER")
		return nil
	}
	iter = s.Query("LIST ALL PERMISSIONS OF " + quoteIdent(user)).IterContext(ctx)
	seen := map[string]bool{}
	row = map[string]any{}
	for iter.MapScan(row) {
		perm, _ := row["permission"].(string)
		resource, _ := row["resource"].(string)
		switch perm {
		case "MODIFY", "CREATE", "ALTER", "DROP", "AUTHORIZE":
			if k := perm + " ON " + resource; !seen[k] && len(res.WritePrivileges) < 20 {
				seen[k] = true
				res.WritePrivileges = append(res.WritePrivileges, k)
			}
		}
		row = map[string]any{}
	}
	if err := iter.Close(); err != nil {
		return []string{"dbscope could not list this role's grants: " + err.Error()}
	}
	return nil
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func classify(err error) *conn.TestError {
	msg := err.Error()
	var re gocql.RequestError
	if errors.As(err, &re) {
		kind := "database"
		switch re.Code() {
		case gocql.ErrCodeCredentials, gocql.ErrCodeUnauthorized:
			kind = "auth"
		case gocql.ErrCodeReadTimeout, gocql.ErrCodeWriteTimeout:
			kind = "timeout"
		}
		return &conn.TestError{Kind: kind, Message: re.Message(), SQLState: fmt.Sprint(re.Code())}
	}
	switch {
	case strings.Contains(msg, "authentication"), strings.Contains(msg, "password are incorrect"), strings.Contains(msg, "unexpected authenticator"):
		return &conn.TestError{Kind: "auth", Message: msg}
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, gocql.ErrTimeoutNoResponse):
		return &conn.TestError{Kind: "timeout", Message: msg}
	}
	return conn.Classify(err)
}

// Introspect maps the keyspace's tables and materialized views: partition
// and clustering keys as the primary key, static columns, secondary indexes
// by kind, and per-table settings that matter for deletes and size.
func Introspect(ctx context.Context, s *gocql.Session, keyspace string) (*schema.Model, error) {
	start := time.Now()
	srv, err := describe(ctx, s)
	if err != nil {
		return nil, err
	}
	m := &schema.Model{Engine: srv.Flavor, Schemas: []string{keyspace}, Relations: []schema.Relation{}, Enums: []schema.Enum{}}
	byName := map[string]*schema.Relation{}
	var order []string
	add := func(name string, kind schema.RelKind, comment string) {
		byName[name] = &schema.Relation{Schema: keyspace, Name: name, Kind: kind, Comment: comment, Columns: []schema.Column{}}
		order = append(order, name)
	}

	iter := s.Query("SELECT * FROM system_schema.tables WHERE keyspace_name = ?", keyspace).IterContext(ctx)
	row := map[string]any{}
	for iter.MapScan(row) {
		name := str(row["table_name"])
		add(name, schema.KindTable, tableComment(row))
		if g, ok := row["gc_grace_seconds"].(int); ok && g == 0 {
			byName[name].AddFinding("DBS-CQL-GC-GRACE-ZERO", "warn", "gc_grace_seconds is 0, so tombstones can be purged before every replica has seen the delete, and a repair can bring deleted rows back", "")
		}
		row = map[string]any{}
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("reading tables: %w", err)
	}

	// ScyllaDB backs each global index with a hidden view named <index>_index.
	indexViews := map[string]bool{}
	type idx struct{ table, name, kind, target, class string }
	var idxs []idx
	iter = s.Query("SELECT table_name, index_name, kind, options FROM system_schema.indexes WHERE keyspace_name = ?", keyspace).IterContext(ctx)
	var ix idx
	var opts map[string]string
	for iter.Scan(&ix.table, &ix.name, &ix.kind, &opts) {
		ix.target, ix.class = opts["target"], opts["class_name"]
		idxs = append(idxs, ix)
		indexViews[ix.name+"_index"] = true
		opts = nil
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("reading indexes: %w", err)
	}

	var view, base, where string
	iter = s.Query("SELECT view_name, base_table_name, where_clause FROM system_schema.views WHERE keyspace_name = ?", keyspace).IterContext(ctx)
	for iter.Scan(&view, &base, &where) {
		if indexViews[view] {
			continue
		}
		add(view, schema.KindMatView, "materialized view of "+base+" where "+where)
		if srv.Flavor == "cassandra" {
			byName[view].AddFinding("DBS-CQL-MATERIALIZED-VIEW", "warn", "materialized views are experimental in Cassandra and off by default (materialized_views_enabled); the view can drift from its base table", "")
		}
		byName[view].DependsOn = []schema.QualifiedName{{Schema: keyspace, Name: base}}
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("reading views: %w", err)
	}

	type keyCol struct {
		name string
		pos  int
	}
	parts, clus := map[string][]keyCol{}, map[string][]keyCol{}
	var table, col, kind, order2, typ string
	var pos int
	iter = s.Query("SELECT table_name, column_name, kind, position, clustering_order, type FROM system_schema.columns WHERE keyspace_name = ?", keyspace).IterContext(ctx)
	for iter.Scan(&table, &col, &kind, &pos, &order2, &typ) {
		r := byName[table]
		if r == nil {
			continue // a ScyllaDB index view
		}
		c := schema.Column{Name: col, Type: typ, Nullable: kind == "regular" || kind == "static"}
		switch strings.ToLower(kind) {
		case "partition_key":
			c.Comment = "partition key"
			parts[table] = append(parts[table], keyCol{col, pos})
		case "clustering":
			c.Comment = "clustering key, " + strings.ToLower(order2)
			clus[table] = append(clus[table], keyCol{col, pos})
		case "static":
			c.Comment = "static: one value per partition"
		}
		r.Columns = append(r.Columns, c)
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("reading columns: %w", err)
	}

	for _, name := range order {
		r := byName[name]
		for _, keys := range [][]keyCol{parts[name], clus[name]} {
			sort.Slice(keys, func(i, j int) bool { return keys[i].pos < keys[j].pos })
			for _, k := range keys {
				r.PrimaryKey = append(r.PrimaryKey, k.name)
			}
		}
		rank := map[string]int{}
		for i, k := range r.PrimaryKey {
			rank[k] = i
		}
		sort.SliceStable(r.Columns, func(i, j int) bool {
			ri, iok := rank[r.Columns[i].Name]
			rj, jok := rank[r.Columns[j].Name]
			if iok != jok {
				return iok
			}
			if iok {
				return ri < rj
			}
			return r.Columns[i].Name < r.Columns[j].Name
		})
	}
	for _, ix := range idxs {
		if r := byName[ix.table]; r != nil {
			kind := indexKind(ix.kind, ix.class, srv.Flavor)
			r.Indexes = append(r.Indexes, schema.Index{Name: ix.name, Columns: []string{ix.target}, Method: kind, Valid: true})
			if kind == "sasi" {
				r.AddFinding("DBS-CQL-SASI", "warn", "SASI index "+ix.name+" is experimental in Cassandra and off by default (sasi_indexes_enabled); SAI is its replacement", ix.target)
			}
		}
	}
	for _, name := range order {
		m.Relations = append(m.Relations, *byName[name])
	}
	m.IntrospectedAt, m.DurationMs = start.UTC(), time.Since(start).Milliseconds()
	return m, nil
}

// indexKind names an index the way docs/ENGINES.md does: legacy secondary
// indexes are node-local in Cassandra, SAI is the newer kind, and ScyllaDB's
// secondary indexes are global, backed by a view.
func indexKind(kind, class, flavor string) string {
	switch {
	case kind != "CUSTOM" && flavor == "scylla":
		return "global index"
	case kind != "CUSTOM":
		return "secondary index"
	case strings.EqualFold(class, "sai"), strings.HasSuffix(strings.ToLower(class), "storageattachedindex"):
		return "sai"
	case strings.HasSuffix(class, "SASIIndex"):
		return "sasi"
	}
	return "custom " + class
}

// tableComment summarises the settings that decide how deletes and expiry
// behave: gc_grace_seconds, the default TTL and the compaction strategy.
func tableComment(row map[string]any) string {
	var parts []string
	if c, ok := row["comment"].(string); ok && c != "" {
		parts = append(parts, c)
	}
	if cm, ok := row["compaction"].(map[string]string); ok {
		cls := cm["class"]
		if i := strings.LastIndex(cls, "."); i >= 0 {
			cls = cls[i+1:]
		}
		if cls != "" {
			parts = append(parts, cls)
		}
	}
	if ttl, ok := row["default_time_to_live"].(int); ok && ttl > 0 {
		parts = append(parts, "default TTL "+seconds(ttl))
	}
	if g, ok := row["gc_grace_seconds"].(int); ok {
		parts = append(parts, "gc_grace "+seconds(g))
	}
	return strings.Join(parts, ", ")
}

// seconds writes a whole number of days, hours or minutes when it is one.
func seconds(n int) string {
	for _, u := range []struct {
		size int
		name string
	}{{86400, "day"}, {3600, "hour"}, {60, "minute"}} {
		if n >= u.size && n%u.size == 0 {
			if n == u.size {
				return "1 " + u.name
			}
			return fmt.Sprintf("%d %ss", n/u.size, u.name)
		}
	}
	return fmt.Sprintf("%d s", n)
}

func str(v any) string { s, _ := v.(string); return s }

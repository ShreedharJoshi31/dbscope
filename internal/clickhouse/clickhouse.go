// Package clickhouse maps ClickHouse databases for dbscope (docs/ENGINES.md):
// table engines, sorting, primary and partition keys, sizes, parts and
// table-design risks; and it runs read-only queries under readonly = 1
// with EXPLAIN's index pruning checked first.
package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/schema"
)

// Client is a connection pool and what dbscope learned about the session.
type Client struct {
	Conn     driver.Conn
	Database string

	mu       sync.Mutex
	readonly int // the user's own readonly level, -1 until read
	version  string
}

// Connect opens a native-protocol pool. sslMode disable and prefer are
// plaintext (the native protocol has no "TLS if offered"); require and the
// verify modes use TLS, normally on port 9440.
func Connect(p conn.Profile, password string) (*Client, error) {
	db := p.Database
	if db == "" {
		db = "default"
	}
	user := p.User
	if user == "" {
		user = "default"
	}
	o := &ch.Options{
		Addr:        []string{net.JoinHostPort(p.Host, strconv.Itoa(p.Port))},
		Auth:        ch.Auth{Database: db, Username: user, Password: password},
		DialTimeout: 10 * time.Second, MaxOpenConns: 4, MaxIdleConns: 2,
		ClientInfo: ch.ClientInfo{Products: []struct{ Name, Version string }{{Name: "dbscope", Version: "1"}}},
	}
	if p.SSLMode != conn.SSLDisable && p.SSLMode != conn.SSLPrefer {
		cfgs, err := conn.TLSConfigs(p)
		if err != nil {
			return nil, err
		}
		o.TLS = cfgs[0]
	}
	c, err := ch.Open(o)
	if err != nil {
		return nil, err
	}
	return &Client{Conn: c, Database: db, readonly: -1}, nil
}

// Close releases the pool.
func (c *Client) Close() { c.Conn.Close() }

// detach returns a context without the caller's deadline that is still
// cancelled when the caller's context ends. clickhouse-go turns any deadline
// over a second into a max_execution_time setting, which a readonly = 1
// user may not send; the deadline still applies on the client.
func detach(ctx context.Context) (context.Context, context.CancelFunc) {
	d, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, cancel)
	return d, func() { stop(); cancel() }
}

// session reads the user's own readonly level and the server version once.
func (c *Client) session(ctx context.Context) (int, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readonly < 0 {
		ctx, cancel := detach(ctx)
		defer cancel()
		var ro string
		if err := c.Conn.QueryRow(ctx, "SELECT toString(getSetting('readonly')), version()").Scan(&ro, &c.version); err != nil {
			return 0, "", err
		}
		c.readonly, _ = strconv.Atoi(ro)
	}
	return c.readonly, c.version, nil
}

// readCtx returns a context whose queries run read-only, by the user's own
// readonly level (verified on 26.8):
//   - 0: readonly = 1 and the limits are sent with each query; a query's own
//     SETTINGS readonly = 0 is then refused;
//   - 1: no setting may change, so only the client-side deadline and row cap apply;
//   - 2: readonly itself may not change, but the limits may; level 2 still
//     allows SET, RESTORE and temporary tables, which Check refuses.
func (c *Client) readCtx(ctx context.Context, timeout time.Duration, maxRows int, extra ch.Settings) (context.Context, error) {
	level, _, err := c.session(ctx)
	if err != nil {
		return nil, err
	}
	if level == 1 {
		return ctx, nil // the caller detaches it: no setting may be sent
	}
	s := ch.Settings{"max_execution_time": int(timeout.Seconds()) + 1}
	if level == 0 {
		s["readonly"] = 1
	}
	if maxRows > 0 {
		// Checked per block, so more rows can arrive; the caller caps them too.
		s["max_result_rows"], s["result_overflow_mode"] = maxRows+1, "break"
	}
	for k, v := range extra {
		s[k] = v
	}
	return ch.Context(ctx, ch.WithSettings(s)), nil
}

// writeGrants are privileges in SHOW GRANTS that let a user change data or schema.
var writeGrants = []string{"ALL", "INSERT", "ALTER", "CREATE", "DROP", "TRUNCATE", "OPTIMIZE", "SYSTEM", "ACCESS MANAGEMENT", "BACKUP"}

// Test connects and reports the version and what the user may change.
func Test(ctx context.Context, p conn.Profile, password string) conn.TestResult {
	res := conn.TestResult{Engine: conn.EngineClickHouse, Warnings: []string{}}
	if (p.SSLMode == conn.SSLDisable || p.SSLMode == conn.SSLPrefer) && !conn.Loopback(p.Endpoint().Host) {
		res.Warnings = append(res.Warnings, "the connection is not encrypted (the native protocol has no \"TLS if offered\"), so the password and results travel in plain text; use sslMode require or verify-full, usually on port 9440.")
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
	level, version, err := c.session(ctx)
	if err != nil {
		res.Error = classify(err)
		return res
	}
	ms := time.Since(start).Milliseconds()
	res.OK, res.LatencyMs, res.ServerVersion, res.ServerVersionNum = true, &ms, version, versionNum(version)
	ro := level == 1
	res.DefaultReadOnly = &ro
	dctx, dcancel := detach(ctx)
	defer dcancel()
	rows, err := c.Conn.Query(dctx, "SHOW GRANTS")
	if err != nil {
		res.Warnings = append(res.Warnings, "dbscope could not read this user's grants: "+err.Error())
		return res
	}
	defer rows.Close()
	for rows.Next() {
		var g string
		if rows.Scan(&g) != nil {
			continue
		}
		grant := strings.TrimPrefix(strings.ToUpper(g), "GRANT ")
		for _, w := range writeGrants {
			if strings.HasPrefix(grant, w+" ") || strings.HasPrefix(grant, w+",") || strings.Contains(grant, ", "+w+" ") || strings.Contains(grant, ", "+w+",") {
				if len(res.WritePrivileges) < 20 {
					res.WritePrivileges = append(res.WritePrivileges, strings.TrimSpace(strings.Split(grant, " TO ")[0]))
				}
				break
			}
		}
	}
	return res
}

func classify(err error) *conn.TestError {
	var ex *ch.Exception
	if errors.As(err, &ex) {
		kind := "database"
		switch ex.Code {
		case 516, 192, 193, 194, 497: // AUTHENTICATION_FAILED, UNKNOWN_USER, WRONG_PASSWORD, REQUIRED_PASSWORD, ACCESS_DENIED
			kind = "auth"
		case 159: // TIMEOUT_EXCEEDED
			kind = "timeout"
		}
		return &conn.TestError{Kind: kind, Message: ex.Message, SQLState: strconv.Itoa(int(ex.Code))}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &conn.TestError{Kind: "timeout", Message: err.Error()}
	}
	return conn.Classify(err)
}

// Part thresholds are MergeTree defaults (docs/research-clickhouse.md, 2.1).
const (
	partsToDelayInsert = 1000
	partsToThrowInsert = 3000
	partitionsWarn     = 1000
)

// Introspect maps the profile's database: tables and materialized views
// with their engines and keys, columns with their roles in those keys, and
// risks. Part and partition counts need SELECT on system.parts and pending
// mutations SELECT on system.mutations; without them the map says so.
func Introspect(ctx context.Context, c *Client) (*schema.Model, error) {
	start := time.Now()
	_, version, err := c.session(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := detach(ctx)
	defer cancel()
	m := &schema.Model{Engine: "clickhouse", ServerVersion: versionNum(version), Schemas: []string{c.Database}, Relations: []schema.Relation{}, Enums: []schema.Enum{}}
	type tableInfo struct {
		name, engine, sorting, primary, partition, sampling, target, comment, ttl string
		rows, bytes                                                               *uint64
		activeParts                                                               *uint64
	}
	rows, err := c.Conn.Query(ctx, `SELECT name, engine, sorting_key, primary_key, partition_key, sampling_key,
		extract(create_table_query, 'MATERIALIZED VIEW \\S+ TO (\\S+)'), comment, total_rows, total_bytes, active_parts,
		if(position(create_table_query, ' TTL ') > 0, 'yes', '')
		FROM system.tables WHERE database = ? AND NOT is_temporary ORDER BY name`, c.Database)
	if err != nil {
		return nil, fmt.Errorf("reading tables: %w", err)
	}
	var tables []tableInfo
	for rows.Next() {
		var t tableInfo
		if err := rows.Scan(&t.name, &t.engine, &t.sorting, &t.primary, &t.partition, &t.sampling, &t.target, &t.comment, &t.rows, &t.bytes, &t.activeParts, &t.ttl); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, t)
	}
	rows.Close()

	byName := map[string]*schema.Relation{}
	for _, t := range tables {
		r := &schema.Relation{Schema: c.Database, Name: t.name, Kind: schema.KindTable, Columns: []schema.Column{}}
		switch {
		case t.engine == "MaterializedView":
			r.Kind = schema.KindMatView
			// The TO target comes from the CREATE statement: system.tables.target_table is new in 26.x.
			if t.target != "" {
				target := strings.Trim(t.target[strings.LastIndexByte(t.target, '.')+1:], "`")
				r.DependsOn = []schema.QualifiedName{{Schema: c.Database, Name: target}}
				r.Comment = "materialized view writing to " + target
			}
		case t.engine == "View":
			r.Kind = schema.KindView
		}
		if t.rows != nil {
			n := int64(*t.rows)
			r.RowEstimate = &n
		}
		if r.Kind == schema.KindTable {
			r.PrimaryKey = splitKey(t.primary)
			r.PartitionKey = t.partition
			r.Comment = tableComment(t.engine, t.sorting, t.primary, t.partition, t.bytes, t.activeParts, t.ttl != "")
			if strings.Contains(t.engine, "MergeTree") && (t.sorting == "" || t.sorting == "tuple()") {
				r.AddFinding("DBS-CH-NO-PRIMARY-INDEX", "warn", "ORDER BY tuple() has no primary index, so every query reads the whole table", "")
			}
		}
		byName[t.name] = r
	}

	rows, err = c.Conn.Query(ctx, `SELECT table, name, type, default_kind, default_expression, compression_codec,
		is_in_sorting_key, is_in_primary_key, is_in_partition_key, comment
		FROM system.columns WHERE database = ? ORDER BY table, position`, c.Database)
	if err != nil {
		return nil, fmt.Errorf("reading columns: %w", err)
	}
	for rows.Next() {
		var table, name, typ, kind, expr, codec, comment string
		var inSort, inPrimary, inPartition uint8 // inPrimary is implied by the key expression, read below
		if err := rows.Scan(&table, &name, &typ, &kind, &expr, &codec, &inSort, &inPrimary, &inPartition, &comment); err != nil {
			rows.Close()
			return nil, err
		}
		r := byName[table]
		if r == nil {
			continue
		}
		col := schema.Column{Name: name, Type: typ, Nullable: strings.HasPrefix(typ, "Nullable(")}
		var notes []string
		switch kind {
		case "DEFAULT":
			col.Default = expr
		case "MATERIALIZED", "ALIAS", "EPHEMERAL":
			col.Generated, col.GeneratedKind = expr, strings.ToLower(kind)
		}
		if inSort == 1 {
			notes = append(notes, "sorting key")
			if col.Nullable {
				r.AddFinding("DBS-CH-NULLABLE-KEY", "warn", name+" is Nullable in the sorting key, which needs allow_nullable_key and adds a null mask to every key read", name)
			}
		}
		if inPartition == 1 {
			notes = append(notes, "partition key")
		}
		if codec != "" {
			notes = append(notes, codec)
		}
		if comment != "" {
			notes = append(notes, comment)
		}
		col.Comment = strings.Join(notes, ", ")
		r.Columns = append(r.Columns, col)
	}
	rows.Close()

	c.addPartitions(ctx, byName)
	c.addMutations(ctx, byName)
	c.addSkipIndexes(ctx, byName)
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		m.Relations = append(m.Relations, *byName[n])
	}
	m.IntrospectedAt, m.DurationMs = start.UTC(), time.Since(start).Milliseconds()
	return m, nil
}

// addPartitions counts active partitions and the parts in the fullest one;
// a partition above parts_to_delay_insert slows inserts, above
// parts_to_throw_insert refuses them.
func (c *Client) addPartitions(ctx context.Context, byName map[string]*schema.Relation) {
	rows, err := c.Conn.Query(ctx, `SELECT table, uniqExact(partition_id), max(n) FROM (
		SELECT table, partition_id, count() AS n FROM system.parts WHERE database = ? AND active GROUP BY table, partition_id)
		GROUP BY table`, c.Database)
	if err != nil {
		note(byName, "partition and part counts need SELECT on system.parts")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var table string
		var partitions, maxParts uint64
		if rows.Scan(&table, &partitions, &maxParts) != nil || byName[table] == nil {
			continue
		}
		r := byName[table]
		r.Comment += fmt.Sprintf("; %d partitions, up to %d parts in one", partitions, maxParts)
		switch {
		case maxParts > partsToThrowInsert:
			r.AddFinding("DBS-CH-TOO-MANY-PARTS", "danger", fmt.Sprintf("%d parts in one partition, above parts_to_throw_insert (3000): inserts fail with Too many parts", maxParts), "")
		case maxParts > partsToDelayInsert:
			r.AddFinding("DBS-CH-MANY-PARTS", "warn", fmt.Sprintf("%d parts in one partition, above parts_to_delay_insert (1000): inserts are slowed", maxParts), "")
		}
		if partitions > partitionsWarn {
			r.AddFinding("DBS-CH-TOO-MANY-PARTITIONS", "warn", fmt.Sprintf("%d partitions; parts never merge across partitions, so a coarser partition key (under 1,000 values) usually works better", partitions), "")
		}
	}
}

// addMutations reports mutations still running or failing.
func (c *Client) addMutations(ctx context.Context, byName map[string]*schema.Relation) {
	rows, err := c.Conn.Query(ctx, `SELECT table, count(), countIf(latest_fail_reason != ''), anyIf(latest_fail_reason, latest_fail_reason != ''), min(create_time)
		FROM system.mutations WHERE database = ? AND NOT is_done GROUP BY table`, c.Database)
	if err != nil {
		return // no grant on system.mutations: silently unknown, parts already noted
	}
	defer rows.Close()
	for rows.Next() {
		var table, reason string
		var n, failed uint64
		var since time.Time
		if rows.Scan(&table, &n, &failed, &reason, &since) != nil || byName[table] == nil {
			continue
		}
		r := byName[table]
		if failed > 0 {
			r.AddFinding("DBS-CH-MUTATION-FAILING", "danger", fmt.Sprintf("%d of %d pending mutations are failing: %s", failed, n, reason), "")
		} else {
			r.AddFinding("DBS-CH-MUTATIONS-PENDING", "warn", fmt.Sprintf("%d mutations pending since %s; they rewrite parts in the background", n, since.UTC().Format(time.RFC3339)), "")
		}
	}
}

// addSkipIndexes lists data-skipping indexes as indexes.
func (c *Client) addSkipIndexes(ctx context.Context, byName map[string]*schema.Relation) {
	rows, err := c.Conn.Query(ctx, `SELECT table, name, type_full, expr FROM system.data_skipping_indices WHERE database = ? ORDER BY table, name`, c.Database)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var table, name, typ, expr string
		if rows.Scan(&table, &name, &typ, &expr) != nil || byName[table] == nil {
			continue
		}
		byName[table].Indexes = append(byName[table].Indexes, schema.Index{Name: name, Columns: []string{expr}, Method: "skip " + typ, Valid: true})
	}
}

// splitKey splits a key expression such as "kind, user_id, toDate(at)" at
// its top-level commas, keeping the order the server sorts by.
func splitKey(expr string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range expr {
		switch r {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(expr[start:i]))
				start = i + 1
			}
		}
	}
	if last := strings.TrimSpace(expr[start:]); last != "" {
		out = append(out, last)
	}
	return out
}

func note(byName map[string]*schema.Relation, msg string) {
	for _, r := range byName {
		if r.Kind == schema.KindTable {
			r.Comment += "; " + msg
		}
	}
}

// tableComment describes the engine, keys and size.
func tableComment(engine, sorting, primary, partition string, bytes, parts *uint64, ttl bool) string {
	parts0 := []string{engine}
	if sorting != "" {
		parts0 = append(parts0, "ORDER BY ("+sorting+")")
	}
	if primary != "" && primary != sorting {
		parts0 = append(parts0, "PRIMARY KEY ("+primary+")")
	}
	if partition != "" {
		parts0 = append(parts0, "PARTITION BY "+partition)
	}
	if bytes != nil {
		parts0 = append(parts0, size(*bytes)+" compressed")
	}
	if parts != nil {
		parts0 = append(parts0, fmt.Sprintf("%d active parts", *parts))
	}
	if ttl {
		parts0 = append(parts0, "TTL set")
	}
	return strings.Join(parts0, ", ")
}

func size(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// versionNum is major*10000 + minor*100 + patch for 26.8.36.3.
func versionNum(v string) int {
	var a, b, c int
	fmt.Sscanf(v, "%d.%d.%d", &a, &b, &c)
	return a*10000 + b*100 + c
}

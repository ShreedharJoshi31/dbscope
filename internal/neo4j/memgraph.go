package neo4j

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	drv "github.com/neo4j/neo4j-go-driver/v6/neo4j"

	"github.com/dbscope/dbscope/internal/health"
	"github.com/dbscope/dbscope/internal/query/guard"
	"github.com/dbscope/dbscope/internal/query/plan"
	"github.com/dbscope/dbscope/internal/schema"
)

// Memgraph speaks Bolt and Cypher, so it shares this package, but its
// catalog differs: no db.labels(), a schema module, SHOW ... INFO commands
// that refuse explicit transactions, and an EXPLAIN that returns text and
// calls every query read-write. Verified on Memgraph 3.13.

// memgraphSample bounds the nodes and relationships read to find labels and
// the label pairs relationship types join; a type seen only beyond it is missed.
const memgraphSample = 100_000

// auto runs one of dbscope's own SHOW ... INFO commands, which Memgraph
// refuses inside an explicit transaction. Only fixed text runs this way:
// an auto-commit READ session does not stop writes on Memgraph.
func (c *Client) auto(ctx context.Context, timeout time.Duration, cypher string) ([]*drv.Record, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	s := c.Driver.NewSession(ctx, drv.SessionConfig{AccessMode: drv.AccessModeRead, DatabaseName: c.sessionDB()})
	defer s.Close(ctx)
	r, err := s.Run(ctx, cypher, nil, drv.WithTxTimeout(timeout))
	if err != nil {
		return nil, err
	}
	return r.Collect(ctx)
}

func describeMemgraph(ctx context.Context, c *Client) (Server, error) {
	recs, err := c.auto(ctx, 10*time.Second, "SHOW VERSION")
	if err != nil {
		return Server{}, err
	}
	srv := Server{Memgraph: true}
	if len(recs) > 0 {
		srv.Version, _ = recs[0].Values[0].(string)
	}
	return srv, nil
}

// storageInfo is SHOW STORAGE INFO as a map of its rows.
func (c *Client) storageInfo(ctx context.Context) (map[string]string, error) {
	recs, err := c.auto(ctx, 10*time.Second, "SHOW STORAGE INFO")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range recs {
		k, _ := r.Values[0].(string)
		out[k] = fmt.Sprint(r.Values[1])
	}
	return out, nil
}

// testMemgraph adds what the connection test says about a Memgraph server.
// Memgraph refuses writes in the READ transactions dbscope uses, except in
// analytical storage mode, which has no transactions to roll back.
func testMemgraph(ctx context.Context, c *Client) []string {
	warnings := []string{"Memgraph refuses writes inside dbscope's READ transactions; the user itself may still write from other tools unless its role forbids it"}
	info, err := c.storageInfo(ctx)
	if err == nil && info["global_storage_mode"] == "IN_MEMORY_ANALYTICAL" {
		warnings = append(warnings, "storage mode is IN_MEMORY_ANALYTICAL, which has no transactions to roll back; dbscope's checks still refuse writing Cypher")
	}
	return warnings
}

// memgraphTypes maps the schema module's type names to Cypher's.
var memgraphTypes = map[string]string{"Int": "integer", "Bool": "boolean", "Float": "float", "Null": "null", "List": "list",
	"Map": "map", "ZonedDateTime": "zoned datetime", "Point2D": "point", "Point3D": "point", "Enum": "enum"}

func memgraphTypeText(types []string) string {
	out := make([]string, 0, len(types))
	for _, t := range types {
		if m, ok := memgraphTypes[t]; ok {
			t = m
		} else if j, ok := javaTypes[t]; ok {
			t = j
		}
		out = append(out, strings.ToLower(t))
	}
	return strings.Join(out, " | ")
}

func introspectMemgraph(ctx context.Context, c *Client, start time.Time, srv Server) (*schema.Model, error) {
	db := c.displayDB()
	m := &schema.Model{Engine: "neo4j", Schemas: []string{db}, Relations: []schema.Relation{}, Enums: []schema.Enum{}}
	byLabel := map[string]*schema.Relation{}
	label := func(l string) *schema.Relation {
		if byLabel[l] == nil {
			byLabel[l] = &schema.Relation{Schema: db, Name: l, Kind: schema.KindNodeLabel, Columns: []schema.Column{}}
		}
		return byLabel[l]
	}
	recs, _, err := c.read(ctx, 30*time.Second, "MATCH (n) WITH n LIMIT $sample UNWIND labels(n) AS l RETURN DISTINCT l", map[string]any{"sample": int64(memgraphSample)})
	if err != nil {
		return nil, fmt.Errorf("reading labels: %w", err)
	}
	for _, r := range recs {
		if l, ok := r.Values[0].(string); ok {
			label(l)
		}
	}
	recs, _, err = c.read(ctx, 30*time.Second, "CALL schema.node_type_properties() YIELD nodeLabels, propertyName, propertyTypes, mandatory RETURN nodeLabels, propertyName, propertyTypes, mandatory", nil)
	if err != nil {
		return nil, fmt.Errorf("reading node properties: %w", err)
	}
	for _, rec := range recs {
		ls, _ := rec.Get("nodeLabels")
		name, _ := rec.Get("propertyName")
		types, _ := rec.Get("propertyTypes")
		mandatory, _ := rec.Get("mandatory")
		prop, _ := name.(string)
		for _, l := range toStrings(ls) {
			r := label(l)
			if prop != "" {
				addProperty(r, prop, memgraphTypeText(toStrings(types)), mandatory == true)
			}
		}
	}
	for l, r := range byLabel {
		if n, err := c.count(ctx, "MATCH (n:"+quote(l)+") RETURN count(n)"); err == nil {
			r.RowEstimate = &n
		}
	}
	if err := c.addMemgraphRelationships(ctx, byLabel); err != nil {
		return nil, err
	}
	c.addMemgraphIndexes(ctx, byLabel)
	labels := make([]string, 0, len(byLabel))
	for l := range byLabel {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	for _, l := range labels {
		c.addFindings(ctx, byLabel[l])
		m.Relations = append(m.Relations, *byLabel[l])
	}
	m.ServerVersion = versionNum(srv.Version)
	m.IntrospectedAt, m.DurationMs = start.UTC(), time.Since(start).Milliseconds()
	return m, nil
}

// addMemgraphRelationships finds the label pairs each relationship type joins
// in a sample of relationships; Memgraph keeps no count store to ask.
func (c *Client) addMemgraphRelationships(ctx context.Context, byLabel map[string]*schema.Relation) error {
	props := map[string][]string{}
	recs, _, err := c.read(ctx, 30*time.Second, "CALL schema.rel_type_properties() YIELD relType, propertyName, propertyTypes RETURN relType, propertyName, propertyTypes", nil)
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
			props[t] = append(props[t], n+" "+memgraphTypeText(toStrings(ts)))
		}
	}
	recs, _, err = c.read(ctx, 30*time.Second, `MATCH (a)-[r]->(b) WITH a, r, b LIMIT $sample
		UNWIND labels(a) AS f UNWIND labels(b) AS t RETURN DISTINCT f, type(r) AS typ, t ORDER BY f, typ, t`, map[string]any{"sample": int64(memgraphSample)})
	if err != nil {
		return fmt.Errorf("reading relationship types: %w", err)
	}
	for _, rec := range recs {
		f, _ := rec.Values[0].(string)
		typ, _ := rec.Values[1].(string)
		t, _ := rec.Values[2].(string)
		from, to := byLabel[f], byLabel[t]
		if from == nil || to == nil {
			continue
		}
		col := "-[:" + typ + "]-> " + t
		comment := "relationship to " + t
		if n, err := c.count(ctx, "MATCH (:"+quote(f)+")-[r:"+quote(typ)+"]->(:"+quote(t)+") RETURN count(r)"); err == nil {
			comment += fmt.Sprintf(", %d from %s", n, f)
		}
		if len(props[typ]) > 0 {
			comment += "; properties: " + strings.Join(props[typ], ", ")
		}
		from.Columns = append(from.Columns, schema.Column{Name: col, Type: t, Nullable: true, Comment: comment})
		from.ForeignKeys = append(from.ForeignKeys, schema.ForeignKey{Name: typ + " to " + t, Columns: []string{col},
			Ref: schema.QualifiedName{Schema: from.Schema, Name: t}, RefCols: []string{""}, Validated: true})
	}
	return nil
}

// addMemgraphIndexes attaches label-property indexes and unique and existence
// constraints; a user without the privilege to read them gets a map without.
func (c *Client) addMemgraphIndexes(ctx context.Context, byLabel map[string]*schema.Relation) {
	if recs, err := c.auto(ctx, 10*time.Second, "SHOW INDEX INFO"); err == nil {
		for _, rec := range recs {
			typ, _ := rec.Values[0].(string)
			l, _ := rec.Values[1].(string)
			props := toStrings(rec.Values[2])
			if r := byLabel[l]; r != nil && len(props) > 0 {
				r.Indexes = append(r.Indexes, schema.Index{Name: typ + " " + l + "(" + strings.Join(props, ", ") + ")", Columns: props, Method: typ, Valid: true})
			}
		}
	}
	if recs, err := c.auto(ctx, 10*time.Second, "SHOW CONSTRAINT INFO"); err == nil {
		for _, rec := range recs {
			typ, _ := rec.Values[0].(string)
			l, _ := rec.Values[1].(string)
			props := toStrings(rec.Values[2])
			r := byLabel[l]
			if r == nil {
				continue
			}
			switch typ {
			case "unique":
				r.Uniques = append(r.Uniques, props)
			case "exists":
				for _, p := range props {
					if col := r.Column(p); col != nil {
						col.Nullable = false
					}
				}
			}
		}
	}
}

// writeOperators are Memgraph plan operators that change data.
var writeOperators = map[string]bool{"CreateNode": true, "CreateExpand": true, "SetProperty": true, "SetProperties": true,
	"SetLabels": true, "RemoveProperty": true, "RemoveLabels": true, "Delete": true, "Merge": true, "LoadCsv": true}

// planLine is "* Operator details" after its depth markers ("| ").
var planLine = regexp.MustCompile(`^((?:\|\s*)*)\*\s+(\S+)\s*(.*)$`)

// memgraphPlan reads Memgraph's text plan into dbscope's tree and refuses
// one with a write operator. Each line at a depth is the child of the line
// before it at that depth; a deeper line starts a branch under the last
// operator one level up.
func memgraphPlan(ctx context.Context, c *Client, stmt string, timeout time.Duration) (*plan.Node, error) {
	recs, _, err := c.read(ctx, timeout, "EXPLAIN "+stmt, nil)
	if err != nil {
		return nil, err
	}
	root := &plan.Node{NodeType: "Query", Flags: []plan.Flag{}, Children: []*plan.Node{}, StepsOnly: true}
	last := map[int]*plan.Node{-1: root}
	for _, rec := range recs {
		line, _ := rec.Values[0].(string)
		m := planLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		depth := strings.Count(m[1], "|")
		op, details := m[2], m[3]
		if writeOperators[op] {
			return nil, &guard.Error{Reason: guard.WritesData, Message: "Memgraph plans this with " + op + ", which changes data; only read-only Cypher runs here"}
		}
		n := &plan.Node{NodeType: op, Condition: details, Flags: []plan.Flag{}, Children: []*plan.Node{}}
		flag := func(level, code, msg string) { n.Flags = append(n.Flags, plan.Flag{Level: level, Code: code, Message: msg}) }
		switch {
		case op == "ScanAll":
			flag("danger", "all_nodes_scan", "Reads every node in the database; start from a label and an indexed property")
		case op == "ScanAllByLabel":
			flag("info", "label_scan", "Reads every "+details+" node; a label-property index on the filtered property would seek instead")
		case op == "Cartesian":
			flag("warn", "cartesian_product", "Pairs every row of one side with every row of the other; connect the patterns or match them separately")
		case strings.HasPrefix(op, "ExpandVariable") && unbounded(stmt):
			// Memgraph's plan omits the bounds, so the statement's own pattern decides.
			flag("danger", "unbounded_path", "A variable-length pattern with no upper bound can walk the whole graph; add one, such as *1..5")
		}
		parent := last[depth]
		if parent == nil {
			parent = last[depth-1]
		}
		if parent == nil {
			parent = root
		}
		parent.Children = append(parent.Children, n)
		last[depth] = n
		for d := range last {
			if d > depth {
				delete(last, d)
			}
		}
	}
	return root, nil
}

// mgReadOnly are the mg.* procedures that only read. Memgraph marks every
// mg.* procedure as not writing, though mg.create_module_file and its kin
// write query modules (code) to the server and mg.load reloads them.
var mgReadOnly = map[string]bool{"mg.procedures": true, "mg.functions": true, "mg.transformations": true,
	"mg.get_module_files": true, "mg.get_module_file": true, "mg.kafka_stream_info": true, "mg.pulsar_stream_info": true}

// checkMemgraphProcedures allows read-only mg.* procedures and others Memgraph marks as not writing.
func (c *Client) checkMemgraphProcedures(ctx context.Context, names []string) error {
	for _, n := range names {
		if l := strings.ToLower(n); strings.HasPrefix(l, "mg.") && !mgReadOnly[l] {
			return &guard.Error{Reason: guard.FunctionNotAllowed, Message: fmt.Sprintf("Procedure %s manages the server (query modules or streams); it is not run through dbscope", n)}
		}
		recs, _, err := c.read(ctx, 10*time.Second, "CALL mg.procedures() YIELD name, is_write WITH name, is_write WHERE toLower(name) = toLower($n) RETURN is_write", map[string]any{"n": n})
		if err != nil {
			return err
		}
		if len(recs) > 0 && recs[0].Values[0] == true {
			return &guard.Error{Reason: guard.FunctionNotAllowed, Message: fmt.Sprintf("Procedure %s writes; only read-only procedures run here", n)}
		}
	}
	return nil
}

// memgraphHealth checks memory against Memgraph's limit and the storage mode.
func memgraphHealth(ctx context.Context, c *Client, srv Server) (*health.Report, error) {
	r := &health.Report{Engine: "memgraph", Server: "Memgraph " + srv.Version, Checks: []health.Check{}}
	info, err := c.storageInfo(ctx)
	if err != nil {
		r.Unavailable("memgraph.memory", "Memory", err.Error())
		return r, nil
	}
	used, limit := sizeBytes(info["memory_res"]), sizeBytes(info["memory_limit"])
	if used > 0 && limit > 0 {
		pct := 100 * float64(used) / float64(limit)
		chk := health.Check{ID: "memgraph.memory", Title: "Memory", Status: health.Level(pct, 80, 95),
			Value: fmt.Sprintf("%s of %s (%.0f%%)", health.Bytes(used), health.Bytes(limit), pct)}
		if chk.Status != health.OK {
			chk.Detail = "Memgraph keeps the whole graph in memory and refuses writes, then queries, as it nears --memory-limit"
			chk.Fix = "Raise --memory-limit or the host's memory, or remove data no longer needed"
		}
		r.Add(chk)
	} else {
		r.Unavailable("memgraph.memory", "Memory", "SHOW STORAGE INFO reported no memory_res or memory_limit")
	}
	mode := info["global_storage_mode"]
	chk := health.Check{ID: "memgraph.storage_mode", Title: "Storage mode", Status: health.OK, Value: mode}
	if mode == "IN_MEMORY_ANALYTICAL" {
		chk.Status = health.Warn
		chk.Detail = "Analytical mode has no transactions: writes are not isolated or rolled back, and a crash loses what was not snapshotted"
		chk.Fix = "Switch back with STORAGE MODE IN_MEMORY_TRANSACTIONAL once bulk loading is done"
	}
	if mode != "" {
		r.Add(chk)
	}
	return r, nil
}

// sizeBytes reads Memgraph's sizes such as "101.68MiB" or "3.83GiB".
func sizeBytes(s string) int64 {
	units := []struct {
		suffix string
		mult   float64
	}{{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1}}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			f, err := strconv.ParseFloat(strings.TrimSuffix(s, u.suffix), 64)
			if err != nil {
				return 0
			}
			return int64(f * u.mult)
		}
	}
	return 0
}

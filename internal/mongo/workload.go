package mongo

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"github.com/dbscope/dbscope/internal/schema"
	"github.com/dbscope/dbscope/internal/workload"
)

// Limits on what is read from system.profile and kept of each text.
const (
	maxProfiled = 2000
	maxTextLen  = 1000
)

// appName is what Connect names dbscope's connections, so the profiler's entries say which are its own.
const appName = "dbscope"

// Workload reads per-collection counters from $collStats and $indexStats
// and, only when an admin has already turned the database profiler on, the
// operations it recorded in system.profile. dbscope never changes the
// profiling level. Commands follow the client's read preference, so on a
// replica set they read a secondary's counters: what ran on that member
// since it started. With several secondaries each command may reach another.
func Workload(ctx context.Context, c *mongo.Client, m *schema.Model) (*workload.Snapshot, error) {
	if len(m.Schemas) != 1 {
		return nil, fmt.Errorf("workload: the model names %d databases, not one", len(m.Schemas))
	}
	dbName := m.Schemas[0]
	snap := &workload.Snapshot{GeneratedAt: time.Now().UTC(), Tables: map[string]*workload.Table{}}
	if dbName == "admin" || dbName == "local" || dbName == "config" {
		snap.Statements.Status = workload.StatusUnavailable
		snap.Statements.Message = "dbscope does not read workload for MongoDB's own " + dbName + " database"
		return snap, nil
	}
	db := c.Database(dbName)
	// RunCommand defaults to the primary; these follow the client's reads instead.
	cmdOpts := options.RunCmd().SetReadPreference(readpref.SecondaryPreferred())
	var hello struct {
		Secondary bool `bson:"secondary"`
	}
	if err := db.RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}, cmdOpts).Decode(&hello); err != nil {
		return nil, fmt.Errorf("workload: %w", err)
	}
	snap.InRecovery = hello.Secondary
	var status struct {
		LocalTime    time.Time `bson:"localTime"`
		UptimeMillis int64     `bson:"uptimeMillis"`
	}
	// Counters start with mongod; reading its uptime needs clusterMonitor, so without it the start is unknown.
	if c.Database("admin").RunCommand(ctx, bson.D{{Key: "serverStatus", Value: 1}}, cmdOpts).Decode(&status) == nil && status.UptimeMillis > 0 {
		start := status.LocalTime.Add(-time.Duration(status.UptimeMillis) * time.Millisecond).UTC()
		snap.TableStatsReset = &start
	}

	var missing []string // privileges this user lacks, in the order the message names them
	var otherErr error
	for i := range m.Relations {
		r := &m.Relations[i]
		if r.Kind == schema.KindView || strings.HasPrefix(r.Name, "system.") {
			continue
		}
		t, lacks, err := collectionCounters(ctx, db.Collection(r.Name))
		var ce mongo.CommandError
		switch {
		case ctx.Err() != nil:
			return nil, fmt.Errorf("workload: %w", ctx.Err())
		case lacks != "":
			if !slices.Contains(missing, lacks) {
				missing = append(missing, lacks)
			}
		case errors.As(err, &ce) && ce.Code == 26: // NamespaceNotFound: dropped since the model was read
		case err != nil && otherErr == nil:
			otherErr = fmt.Errorf("%s: %w", r.Name, err)
		}
		if t != nil {
			snap.Tables[r.QName().String()] = t
		}
	}

	profilerMissing := readProfile(ctx, db, m, snap, cmdOpts)
	if profilerMissing {
		missing = append(missing, "find on "+dbName+".system.profile")
	}
	var notes []string
	if snap.Statements.Message != "" {
		notes = append(notes, snap.Statements.Message)
	}
	if len(missing) > 0 {
		// grantRolesToUser runs in the database the user is defined in.
		user, userDB := "<user>", "admin"
		var cs struct {
			AuthInfo struct {
				Users []struct {
					User string `bson:"user"`
					DB   string `bson:"db"`
				} `bson:"authenticatedUsers"`
			} `bson:"authInfo"`
		}
		if db.RunCommand(ctx, bson.D{{Key: "connectionStatus", Value: 1}}, cmdOpts).Decode(&cs) == nil && len(cs.AuthInfo.Users) > 0 {
			user, userDB = cs.AuthInfo.Users[0].User, cs.AuthInfo.Users[0].DB
		}
		notes = append(notes, fmt.Sprintf("This user lacks %s. An admin grants them, without access to the collections' documents, with "+
			`db.getSiblingDB(%s).grantRolesToUser(%s, [{ role: "clusterMonitor", db: "admin" }])`,
			strings.Join(missing, " and "), strconv.Quote(userDB), strconv.Quote(user)))
	}
	if otherErr != nil {
		notes = append(notes, "Some collections' counters could not be read: "+otherErr.Error())
	}
	snap.Statements.Message = strings.Join(notes, " ")
	return snap, nil
}

func isUnauthorized(err error) bool {
	var ce mongo.CommandError
	return errors.As(err, &ce) && (ce.Code == 13 || ce.Name == "Unauthorized")
}

// collectionCounters reads one collection's latency and index counters, and
// names the privilege it lacks when the server refuses one of them. The
// collection's own counters include dbscope's reads: this $collStats counts
// as two reads and $indexStats as one, and the schema sample as more.
// MongoDB keeps no counter of collection scans, so SeqScans stays 0.
func collectionCounters(ctx context.Context, coll *mongo.Collection) (*workload.Table, string, error) {
	cur, err := coll.Aggregate(ctx, mongo.Pipeline{{{Key: "$collStats", Value: bson.D{
		{Key: "latencyStats", Value: bson.D{{Key: "histograms", Value: false}}},
		{Key: "storageStats", Value: bson.D{}},
	}}}})
	if isUnauthorized(err) {
		return nil, "the collStats action", nil
	}
	if err != nil {
		return nil, "", err
	}
	var stats struct {
		Latency struct {
			Reads  latency `bson:"reads"`
			Writes latency `bson:"writes"`
		} `bson:"latencyStats"`
		Storage struct {
			Count int64 `bson:"count"`
		} `bson:"storageStats"`
	}
	ok := cur.Next(ctx)
	if ok {
		err = cur.Decode(&stats)
	}
	cur.Close(ctx)
	if err == nil {
		err = cur.Err()
	}
	if err != nil || !ok {
		return nil, "", err
	}
	t := &workload.Table{
		QueryCalls:  stats.Latency.Reads.Ops + stats.Latency.Writes.Ops,
		QueryTimeMs: float64(stats.Latency.Reads.Micros+stats.Latency.Writes.Micros) / 1000,
		// Write operations, not documents: an insertMany of 50 is one.
		RowsWritten: stats.Latency.Writes.Ops,
		LiveRows:    stats.Storage.Count,
	}
	cur, err = coll.Aggregate(ctx, mongo.Pipeline{{{Key: "$indexStats", Value: bson.D{}}}})
	if isUnauthorized(err) {
		return t, "the indexStats action", nil
	}
	if err != nil {
		return t, "", err
	}
	defer cur.Close(ctx)
	for cur.Next(ctx) {
		var ix struct {
			Name     string `bson:"name"`
			Accesses struct {
				Ops int64 `bson:"ops"`
			} `bson:"accesses"`
		}
		if err := cur.Decode(&ix); err != nil {
			return t, "", err
		}
		// _id lookups are left out: they are point reads, dbscope's reference checks among them.
		if ix.Name != "_id_" {
			t.IdxScans += ix.Accesses.Ops
		}
	}
	return t, "", cur.Err()
}

type latency struct {
	Micros int64 `bson:"latency"`
	Ops    int64 `bson:"ops"`
}

// systemStages start pipelines that read the server's own state, not data.
var systemStages = map[string]bool{
	"$collStats": true, "$indexStats": true, "$planCacheStats": true, "$currentOp": true, "$listSessions": true,
	"$listLocalSessions": true, "$listSearchIndexes": true, "$queryStats": true, "$querySettings": true,
	"$listCatalog": true, "$shardedDataDistribution": true,
}

// group is one command shape on one collection.
type group struct {
	query  workload.Query
	tables []string
}

// readProfile fills snap.Statements from system.profile when the profiler is
// already on. Tables come from structured fields, not text: the entry's ns,
// plus the collections a pipeline's $lookup, $graphLookup, $unionWith, $out
// and $merge name. It returns whether this user may not read the profiler.
func readProfile(ctx context.Context, db *mongo.Database, m *schema.Model, snap *workload.Snapshot, cmdOpts *options.RunCmdOptionsBuilder) bool {
	info := &snap.Statements
	var prof struct {
		Was        int64    `bson:"was"`
		Slowms     int64    `bson:"slowms"`
		SampleRate float64  `bson:"sampleRate"`
		Filter     bson.Raw `bson:"filter"`
	}
	fail := func(err error) bool {
		info.Status, info.Message = workload.StatusUnavailable, err.Error()
		if isUnauthorized(err) {
			info.Message = "Statements need the database profiler's records."
		}
		return isUnauthorized(err)
	}
	// profile -1 only reads the level; it needs find on system.profile.
	if err := db.RunCommand(ctx, bson.D{{Key: "profile", Value: -1}}, cmdOpts).Decode(&prof); err != nil {
		return fail(err)
	}
	if prof.Was == 0 {
		info.Status = workload.StatusNotLoaded
		info.Message = fmt.Sprintf("The database profiler is off for %s, so there are no statements; collection counters come from $collStats. "+
			`An admin turns it on with db.getSiblingDB(%s).setProfilingLevel(1, { slowms: %d }), on each member dbscope reads: `+
			"mongod then also writes a document for every operation slower than %d ms into the capped collection %s.system.profile, "+
			"one extra write per recorded operation. Level 2 records every operation and costs the most. dbscope only reads the profiler.",
			db.Name(), strconv.Quote(db.Name()), prof.Slowms, prof.Slowms, db.Name())
		return false
	}

	filter := bson.D{
		{Key: "ns", Value: bson.D{{Key: "$not", Value: bson.Regex{Pattern: "^" + regexp.QuoteMeta(db.Name()) + `\.system\.`}}}},
		// dbscope's own operations, its schema sample and these reads among them.
		{Key: "appName", Value: bson.D{{Key: "$ne", Value: appName}}},
	}
	cur, err := db.Collection("system.profile").Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "$natural", Value: -1}}).SetLimit(maxProfiled).
		SetProjection(bson.D{{Key: "op", Value: 1}, {Key: "ns", Value: 1}, {Key: "command", Value: 1}, {Key: "originatingCommand", Value: 1},
			{Key: "millis", Value: 1}, {Key: "ts", Value: 1}, {Key: "planCacheShapeHash", Value: 1}, {Key: "queryHash", Value: 1}}))
	if err != nil {
		return fail(err)
	}
	defer cur.Close(ctx)
	collections := map[string]bool{}
	for _, r := range m.Relations {
		collections[r.QName().String()] = true
	}
	groups := map[string]*group{}
	var oldest time.Time
	for cur.Next(ctx) {
		var e struct {
			Op                 string    `bson:"op"`
			NS                 string    `bson:"ns"`
			Command            bson.Raw  `bson:"command"`
			Originating        bson.Raw  `bson:"originatingCommand"`
			Millis             int64     `bson:"millis"`
			TS                 time.Time `bson:"ts"`
			PlanCacheShapeHash string    `bson:"planCacheShapeHash"`
			QueryHash          string    `bson:"queryHash"`
		}
		if err := cur.Decode(&e); err != nil {
			continue
		}
		info.Read++
		oldest = e.TS
		op, cmd, calls := e.Op, e.Command, int64(1)
		if op == "getmore" {
			// A getMore continues its command: its time counts, not as another call.
			op, cmd, calls = "command", e.Originating, 0
			if first(cmd) == "find" {
				op = "query"
			}
		}
		kind := commandKind(op, cmd)
		if kind == "" {
			continue // catalog and administrative commands
		}
		coll := strings.TrimPrefix(e.NS, db.Name()+".")
		text := commandText(coll, kind, cmd)
		shape := cmp.Or(e.PlanCacheShapeHash, e.QueryHash, text)
		key := e.NS + "\x00" + kind + "\x00" + shape
		g := groups[key]
		if g == nil {
			sum := sha256.Sum256([]byte(key))
			g = &group{query: workload.Query{QueryID: hex.EncodeToString(sum[:8]), Text: text}}
			for _, ns := range append([]string{e.NS}, pipelineCollections(db.Name(), cmd)...) {
				if collections[ns] && !slices.Contains(g.tables, ns) {
					g.tables = append(g.tables, ns)
				}
			}
			groups[key] = g
		}
		g.query.Calls += calls
		g.query.TotalMs += float64(e.Millis)
		info.TotalCalls += calls
		info.TotalTimeMs += float64(e.Millis)
	}
	if err := cur.Err(); err != nil {
		return fail(err)
	}
	info.Status = workload.StatusOK
	// The most recent entries were read, so the window since the oldest is
	// complete and nothing in it is unread.
	if !oldest.IsZero() {
		since := oldest.UTC()
		info.Since = &since
	}
	sorted := make([]*group, 0, len(groups))
	for _, g := range groups {
		if g.query.Calls == 0 {
			continue // getMores of a command older than the window
		}
		g.query.MeanMs = g.query.TotalMs / float64(g.query.Calls)
		sorted = append(sorted, g)
	}
	// Least first: AddTopQuery puts a tie ahead of what is already there.
	slices.SortFunc(sorted, func(a, b *group) int {
		return cmp.Or(cmp.Compare(a.query.TotalMs, b.query.TotalMs), cmp.Compare(a.query.Calls, b.query.Calls),
			strings.Compare(b.query.QueryID, a.query.QueryID))
	})
	for _, g := range sorted {
		if len(g.tables) == 0 {
			info.UnattributedCalls += g.query.Calls
		}
		for _, key := range g.tables {
			t := snap.Tables[key]
			if t == nil {
				t = &workload.Table{}
				snap.Tables[key] = t
			}
			workload.AddTopQuery(t, g.query)
		}
	}
	switch {
	case prof.Was == 1:
		info.Message = fmt.Sprintf("The profiler records only operations slower than %d ms", prof.Slowms)
		if prof.SampleRate < 1 {
			info.Message += fmt.Sprintf(", %.0f%% of them", prof.SampleRate*100)
		}
		info.Message += "."
	case prof.SampleRate < 1:
		info.Message = fmt.Sprintf("The profiler records %.0f%% of operations.", prof.SampleRate*100)
	}
	if len(prof.Filter) > 0 {
		info.Message = strings.TrimSpace(info.Message + " It records only operations that match its filter.")
	}
	return false
}

// commandKind names an application operation, or "" for anything else.
func commandKind(op string, cmd bson.Raw) string {
	switch op {
	case "query":
		if first(cmd) == "find" {
			return "find"
		}
	case "insert", "update", "remove":
		return op
	case "command":
		switch name := first(cmd); name {
		case "aggregate":
			if stages, ok := cmd.Lookup("pipeline").ArrayOK(); ok {
				if v, err := stages.IndexErr(0); err == nil {
					if st, ok := v.DocumentOK(); ok && systemStages[first(st)] {
						return ""
					}
				}
			}
			return name
		case "count", "distinct", "findAndModify":
			return name
		case "findandmodify":
			return "findAndModify"
		}
	}
	return ""
}

func first(d bson.Raw) string {
	if e, err := d.IndexErr(0); err == nil {
		return e.Key()
	}
	return ""
}

// commandText is a mongosh-like text of the command with its values replaced by ?.
func commandText(coll, kind string, cmd bson.Raw) string {
	var b strings.Builder
	arg := func(key string) {
		if v, err := cmd.LookupErr(key); err == nil {
			shape(&b, key, v, false)
		} else {
			b.WriteString("{}")
		}
	}
	b.WriteString("db." + coll + "." + kind + "(")
	switch kind {
	case "find":
		arg("filter")
		b.WriteString(")")
		if _, err := cmd.LookupErr("sort"); err == nil {
			b.WriteString(".sort(")
			arg("sort")
			b.WriteString(")")
		}
	case "aggregate":
		if v, err := cmd.LookupErr("pipeline"); err == nil {
			shape(&b, "pipeline", v, true)
		}
		b.WriteString(")")
	case "count":
		arg("query")
		b.WriteString(")")
	case "distinct":
		key, _ := cmd.Lookup("key").StringValueOK()
		b.WriteString(strconv.Quote(key) + ", ")
		arg("query")
		b.WriteString(")")
	case "findAndModify":
		arg("query")
		if _, err := cmd.LookupErr("update"); err == nil {
			b.WriteString(", ")
			arg("update")
		}
		b.WriteString(")")
	case "update":
		arg("q")
		b.WriteString(", ")
		arg("u")
		b.WriteString(")")
	case "remove":
		arg("q")
		b.WriteString(")")
	default:
		b.WriteString(")")
	}
	s := b.String()
	if len(s) > maxTextLen {
		i := maxTextLen
		for i > 0 && !utf8.RuneStart(s[i]) {
			i--
		}
		s = s[:i] + "…"
	}
	return s
}

// namingKeys hold collection and field names in a pipeline, which are kept.
var namingKeys = map[string]bool{"from": true, "as": true, "localField": true, "foreignField": true, "coll": true,
	"into": true, "db": true, "connectFromField": true, "connectToField": true, "$out": true, "$unionWith": true, "$count": true}

// shape writes v with literals replaced by ?. In a pipeline (stage true)
// field paths ("$name") and names under namingKeys stay, except inside $match.
func shape(b *strings.Builder, key string, v bson.RawValue, stage bool) {
	if b.Len() > maxTextLen {
		return
	}
	switch v.Type {
	case bson.TypeEmbeddedDocument:
		elems, _ := v.Document().Elements()
		b.WriteString("{")
		for i, e := range elems {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(strconv.Quote(e.Key()) + ": ")
			shape(b, e.Key(), e.Value(), stage && e.Key() != "$match")
		}
		b.WriteString("}")
	case bson.TypeArray:
		vals, _ := v.Array().Values()
		if !stage && !slices.ContainsFunc(vals, func(v bson.RawValue) bool {
			return v.Type == bson.TypeEmbeddedDocument || v.Type == bson.TypeArray
		}) {
			b.WriteString("[?]")
			return
		}
		b.WriteString("[")
		for i, e := range vals {
			if i > 0 {
				b.WriteString(", ")
			}
			shape(b, key, e, stage)
		}
		b.WriteString("]")
	case bson.TypeString:
		if s := v.StringValue(); stage && (strings.HasPrefix(s, "$") || namingKeys[key]) {
			b.WriteString(strconv.Quote(s))
			return
		}
		b.WriteString("?")
	default:
		b.WriteString("?")
	}
}

// pipelineCollections lists the "db.collection" names an aggregate's
// pipeline reads or writes besides its own, nested pipelines included.
func pipelineCollections(db string, cmd bson.Raw) []string {
	var out []string
	add := func(dbv bson.RawValue, coll string) {
		d := db
		if s, ok := dbv.StringValueOK(); ok {
			d = s
		}
		if coll != "" {
			out = append(out, d+"."+coll)
		}
	}
	var walk func(v bson.RawValue)
	walk = func(v bson.RawValue) {
		var elems []bson.RawElement
		switch v.Type {
		case bson.TypeEmbeddedDocument:
			elems, _ = v.Document().Elements()
		case bson.TypeArray:
			vals, _ := v.Array().Values()
			for _, e := range vals {
				walk(e)
			}
			return
		default:
			return
		}
		for _, e := range elems {
			val := e.Value()
			switch e.Key() {
			case "$lookup", "$graphLookup":
				if d, ok := val.DocumentOK(); ok {
					from := d.Lookup("from")
					if s, ok := from.StringValueOK(); ok {
						add(bson.RawValue{}, s)
					} else if fd, ok := from.DocumentOK(); ok {
						add(fd.Lookup("db"), str(fd.Lookup("coll")))
					}
				}
			case "$unionWith", "$out":
				if s, ok := val.StringValueOK(); ok {
					add(bson.RawValue{}, s)
				} else if d, ok := val.DocumentOK(); ok {
					add(d.Lookup("db"), str(d.Lookup("coll")))
				}
			case "$merge":
				into := val
				if d, ok := val.DocumentOK(); ok {
					into = d.Lookup("into")
				}
				if s, ok := into.StringValueOK(); ok {
					add(bson.RawValue{}, s)
				} else if d, ok := into.DocumentOK(); ok {
					add(d.Lookup("db"), str(d.Lookup("coll")))
				}
			}
			walk(val)
		}
	}
	if p, err := cmd.LookupErr("pipeline"); err == nil {
		walk(p)
	}
	return out
}

func str(v bson.RawValue) string {
	s, _ := v.StringValueOK()
	return s
}

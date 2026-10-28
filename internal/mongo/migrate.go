package mongo

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/apply"
	"github.com/dbscope/dbscope/internal/migrate/impact"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/score"
)

// Base is the database migrations are checked against.
type Base struct {
	Server      Server
	Collections map[string]*CollectionInfo
}

// CollectionInfo is what the rules need to know about one collection.
type CollectionInfo struct {
	Count   int64
	Bytes   int64 // storage size; 0 when unknown
	Indexes []IndexInfo
}

// ReadBase reads each collection's estimated count, storage size and indexes
// (the read role allows listIndexes and $collStats).
func ReadBase(ctx context.Context, c *mongo.Client, db string) (*Base, error) {
	srv, err := describe(ctx, c, db)
	if err != nil {
		return nil, err
	}
	b := &Base{Server: srv, Collections: map[string]*CollectionInfo{}}
	d := c.Database(db)
	names, err := d.ListCollectionNames(ctx, bson.D{{Key: "type", Value: "collection"}})
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		if strings.HasPrefix(n, "system.") {
			continue
		}
		coll := d.Collection(n)
		info := &CollectionInfo{}
		info.Count, _ = coll.EstimatedDocumentCount(ctx)
		if info.Indexes, err = indexes(ctx, coll); err != nil {
			return nil, err
		}
		if cur, err := coll.Aggregate(ctx, bson.A{bson.D{{Key: "$collStats", Value: bson.D{{Key: "storageStats", Value: bson.D{}}}}}}); err == nil {
			var row struct {
				StorageStats struct {
					Size int64 `bson:"size"`
				} `bson:"storageStats"`
			}
			if cur.Next(ctx) && cur.Decode(&row) == nil {
				info.Bytes = row.StorageStats.Size
			}
			cur.Close(ctx)
		}
		b.Collections[n] = info
	}
	return b, nil
}

// Analyze rates the database calls in JavaScript migrations (mongosh
// scripts, migrate-mongo's up functions) against base. Index builds since
// MongoDB 4.2 lock the collection only at their start and end, so the risks
// are what the server refuses (checked on 8.0), work proportional to the
// collection, documents that TTL indexes delete at once, and lost data
// (docs/ENGINES.md, "MongoDB").
func Analyze(base *Base, files []ingest.Input) *migrate.Report {
	rep := &migrate.Report{Engine: "mongodb", PGVersion: base.Server.Num, Rating: score.Low, Statements: []migrate.Statement{},
		Impact: map[string][]impact.Item{}, Unanalyzable: []migrate.Unanalyzable{}, Warnings: []string{}, Base: "live",
		Diff: apply.Diff{Added: []string{}, Dropped: []string{}, Altered: []string{}, Columns: []apply.ColumnChange{}}}
	if len(base.Collections) == 0 {
		rep.Base = "none"
	}
	a := &mongoAnalysis{base: base, rep: rep, altered: map[string]bool{}}
	for _, f := range files {
		calls := findCalls(f.SQL)
		if len(calls) == 0 && strings.TrimSpace(f.SQL) != "" {
			rep.Unanalyzable = append(rep.Unanalyzable, migrate.Unanalyzable{File: f.Name, Line: 1,
				Message: "no database calls found: dbscope reads db.<collection>.<method>(...), db.collection(\"name\").<method>(...) and db.runCommand(...)"})
		}
		for _, call := range calls {
			st := a.statement(f.Name, call)
			rep.Statements = append(rep.Statements, st)
			rep.Rating = score.Max(rep.Rating, st.Rating)
		}
	}
	for k := range a.altered {
		rep.Diff.Altered = append(rep.Diff.Altered, k)
	}
	sort.Strings(rep.Diff.Added)
	sort.Strings(rep.Diff.Dropped)
	sort.Strings(rep.Diff.Altered)
	return rep
}

type mongoAnalysis struct {
	base    *Base
	rep     *migrate.Report
	altered map[string]bool
}

// call is one database call found in a script.
type call struct {
	Collection string // "" for database-level calls
	Method     string
	Args       []jsValue
	Text       string
	Line, End  int
}

func (a *mongoAnalysis) statement(file string, c call) (out migrate.Statement) {
	out = migrate.Statement{File: file, Line: c.Line, Column: 1, EndLine: c.End, SQL: c.Text, Kind: c.Method,
		Tx: "none", Locks: []migrate.Lock{}, Work: "none", Rating: score.Low, Findings: []migrate.Finding{}}
	add := func(id string, r score.Rating, msg, alt string) {
		out.Findings = append(out.Findings, migrate.Finding{RuleID: id, Rating: r, Message: msg, SafeAlternative: alt})
		out.Rating = score.Max(out.Rating, r)
	}
	defer func() {
		out.Score = map[score.Rating]int{score.Low: 0, score.Medium: 4, score.High: 8, score.Critical: 12}[out.Rating]
	}()
	fails := func(msg, alt string) { add("DBS-MONGO-FAILS", score.High, msg, alt) }
	info := a.base.Collections[c.Collection]
	arg := func(i int) jsValue {
		if i < len(c.Args) {
			return c.Args[i]
		}
		return nil
	}
	if c.Collection != "" {
		a.altered[c.Collection] = true
	}

	switch c.Method {
	case "createIndex", "ensureIndex":
		a.createIndex(&out, add, fails, c.Collection, info, arg(0), arg(1))
	case "createIndexes":
		if specs, ok := arg(0).([]jsValue); ok {
			for _, s := range specs {
				a.createIndex(&out, add, fails, c.Collection, info, s, arg(1))
			}
		}
	case "dropIndex", "dropIndexes":
		name := indexName(arg(0))
		switch {
		case name == "_id_":
			fails("The _id index cannot be dropped", "")
		case name != "" && name != "*" && info != nil && findIndex(info, name, "") == nil:
			fails("No index named "+name+" exists on "+c.Collection, "")
		default:
			add("DBS-MONGO-DROP-INDEX", score.Medium, "Queries that use the index fall back to other indexes or collection scans", "Hide it first (collMod hidden: true), watch query latency, then drop it")
		}
	case "drop":
		a.rep.Diff.Dropped = append(a.rep.Diff.Dropped, c.Collection)
		add("DBS-MONGO-DROPS-DATA", score.High, fmt.Sprintf("drop() removes %s and its %s", c.Collection, docsText(info)), "Take a backup first; mongodump can export the collection")
	case "dropDatabase":
		add("DBS-MONGO-DROPS-DATA", score.High, "dropDatabase() removes every collection of the database", "")
	case "deleteMany", "remove", "findOneAndDelete", "deleteOne":
		add("DBS-MONGO-DROPS-DATA", map[bool]score.Rating{true: score.High, false: score.Medium}[c.Method == "deleteMany" || c.Method == "remove"],
			c.Method+" deletes matching documents of "+c.Collection, "Check the filter with countDocuments first")
	case "updateMany", "update", "replaceOne", "updateOne", "bulkWrite":
		if c.Method == "updateMany" || c.Method == "update" || c.Method == "bulkWrite" {
			out.Work = "rewrite"
			add("DBS-MONGO-BULK-UPDATE", a.tier(info), fmt.Sprintf("%s rewrites every matching document of %s (%s, %s), and each write goes through the oplog to every secondary",
				c.Method, c.Collection, docsText(info), sizeText(info)), "Update in batches by _id range, pausing between batches so secondaries keep up")
		}
	case "renameCollection":
		to, _ := arg(0).(string)
		dropTarget, _ := arg(1).(bool)
		_, exists := a.base.Collections[to]
		a.rep.Diff.Dropped = append(a.rep.Diff.Dropped, c.Collection)
		a.rep.Diff.Added = append(a.rep.Diff.Added, to)
		switch {
		case exists && !dropTarget:
			fails("renameCollection fails: "+to+" already exists", "Pass dropTarget: true only if its data may go")
		case exists:
			add("DBS-MONGO-DROPS-DATA", score.High, "dropTarget replaces "+to+", dropping its documents", "")
		default:
			add("DBS-MONGO-RENAME", score.Medium, "Clients that still use "+c.Collection+" break; a rename across databases copies every document", "")
		}
	case "convertToCapped":
		out.Work = "rewrite"
		add("DBS-MONGO-CAPPED", score.High, "convertToCapped copies "+c.Collection+" under an exclusive lock, blocking reads and writes, and drops documents beyond the size", "")
	case "createCollection":
		name, _ := arg(0).(string)
		a.rep.Diff.Added = append(a.rep.Diff.Added, name)
	case "runCommand", "adminCommand", "command":
		a.command(&out, add, fails, arg(0))
	}
	return out
}

// createIndex rates one index spec: a conflict with an existing index fails,
// a unique index fails on duplicates, a TTL index deletes old documents at
// once, and the build reads the whole collection.
func (a *mongoAnalysis) createIndex(out *migrate.Statement, add func(string, score.Rating, string, string), fails func(string, string),
	coll string, info *CollectionInfo, spec, opts jsValue) {
	keys, options := spec, opts
	if o, ok := spec.(jsObject); ok {
		if k := o.get("key"); k != nil { // createIndexes form: {key: {...}, name: ...}
			keys, options = k, spec
		}
	}
	keyText := keyList(keys)
	if len(keyText) == 0 {
		return
	}
	o, _ := options.(jsObject)
	name, _ := o.get("name").(string)
	if name == "" {
		name = defaultIndexName(keyText)
	}
	unique := o.get("unique") == true
	sparse := o.get("sparse") == true
	if info != nil {
		byName, byKeys := findIndex(info, name, ""), findIndex(info, "", strings.Join(keyText, ","))
		switch {
		case byName != nil && strings.Join(byName.Keys, ",") != strings.Join(keyText, ","):
			fails(fmt.Sprintf("An index named %s already exists on %s with other keys (%s)", name, coll, strings.Join(byName.Keys, ", ")), "Choose another name")
			return
		case byKeys != nil && byKeys.Name != name:
			fails(fmt.Sprintf("%s already has an index on these keys, named %s", coll, byKeys.Name), "Drop it first, or reuse its name")
			return
		case byName != nil && (byName.Unique != unique || sparse || o.get("partialFilterExpression") != nil && byName.Partial == ""):
			fails(fmt.Sprintf("An index named %s already exists on %s with other options", name, coll), "Drop it first, or create the new one under another name")
			return
		case byName != nil:
			add("DBS-MONGO-INDEX-EXISTS", score.Low, "An identical index already exists, so the call does nothing", "")
			return
		}
	}
	out.Work = "index_build"
	add("DBS-MONGO-INDEX-BUILD", a.tier(info), fmt.Sprintf("Building %s reads all of %s (%s, %s); reads and writes continue except at its start and end",
		name, coll, docsText(info), sizeText(info)), "Build it at a quiet time; on a replica set every member builds at once")
	if unique && info != nil && info.Count > 0 {
		add("DBS-MONGO-UNIQUE", score.Medium, "A unique index build fails if any two existing documents share the key (E11000)",
			"Check first with an aggregation that groups by the key and keeps counts above 1")
	}
	if ttl := o.get("expireAfterSeconds"); ttl != nil && info != nil && info.Count > 0 {
		add("DBS-MONGO-TTL", score.High, fmt.Sprintf("The TTL index deletes every document of %s already older than %s within a minute of the build", coll, secondsText(ttl)),
			"Make sure no existing document is past the TTL, or delete old ones in batches first")
	}
}

// command rates runCommand documents: collMod validators and TTL changes,
// renames and drops.
func (a *mongoAnalysis) command(out *migrate.Statement, add func(string, score.Rating, string, string), fails func(string, string), v jsValue) {
	o, ok := v.(jsObject)
	if !ok || len(o) == 0 {
		return
	}
	out.Kind = o[0].Key
	switch o[0].Key {
	case "collMod":
		coll, _ := o[0].Value.(string)
		a.altered[coll] = true
		if o.get("validator") != nil {
			level, _ := o.get("validationLevel").(string)
			action, _ := o.get("validationAction").(string)
			if action != "warn" && level != "off" {
				add("DBS-MONGO-VALIDATOR", score.Medium, "The validator applies to inserts and updates from now on; existing documents that break it stay, and their next update fails",
					"Find violators first with find({$nor: [validator]}), or start with validationAction: \"warn\"")
			}
		}
		if idx, ok := o.get("index").(jsObject); ok && idx.get("expireAfterSeconds") != nil {
			add("DBS-MONGO-TTL", score.High, "Lowering a TTL deletes every document past the new limit within a minute", "")
		}
	case "drop":
		coll, _ := o[0].Value.(string)
		a.rep.Diff.Dropped = append(a.rep.Diff.Dropped, coll)
		add("DBS-MONGO-DROPS-DATA", score.High, "drop removes "+coll+" and its documents", "")
	case "dropDatabase":
		add("DBS-MONGO-DROPS-DATA", score.High, "dropDatabase removes every collection of the database", "")
	case "renameCollection":
		to, _ := o.get("to").(string)
		_, exists := a.base.Collections[afterDot(to)]
		if exists && o.get("dropTarget") != true {
			fails("renameCollection fails: "+to+" already exists", "")
		} else if exists {
			add("DBS-MONGO-DROPS-DATA", score.High, "dropTarget replaces "+to+", dropping its documents", "")
		}
	case "convertToCapped":
		out.Work = "rewrite"
		add("DBS-MONGO-CAPPED", score.High, "convertToCapped copies the collection under an exclusive lock, blocking reads and writes", "")
	case "shardCollection", "reshardCollection":
		add("DBS-MONGO-SHARD", score.High, o[0].Key+" moves data between shards for hours on a large collection; balancer migrations add load", "")
	}
}

// secondsText spells a literal number of seconds: "2592000 seconds (30 days)".
func secondsText(v jsValue) string {
	f, ok := v.(float64)
	if !ok {
		return fmt.Sprint(v) + " seconds"
	}
	s := strconv.FormatFloat(f, 'f', -1, 64) + " seconds"
	switch days := f / 86400; {
	case days == 1:
		s += " (1 day)"
	case days > 1 && days == float64(int64(days)):
		s += fmt.Sprintf(" (%d days)", int64(days))
	}
	return s
}

func afterDot(s string) string {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return s[i+1:]
	}
	return s
}

func findIndex(info *CollectionInfo, name, keys string) *IndexInfo {
	for i := range info.Indexes {
		ix := &info.Indexes[i]
		if name != "" && ix.Name == name || keys != "" && strings.Join(ix.Keys, ",") == keys {
			return ix
		}
	}
	return nil
}

// keyList spells an index key document as IndexInfo.Keys does: "a", "b:-1", "c:text".
func keyList(v jsValue) []string {
	o, ok := v.(jsObject)
	if !ok {
		return nil
	}
	var out []string
	for _, kv := range o {
		switch x := kv.Value.(type) {
		case string:
			out = append(out, kv.Key+":"+x)
		case float64:
			if x < 0 {
				out = append(out, kv.Key+":-1")
			} else {
				out = append(out, kv.Key)
			}
		default:
			out = append(out, kv.Key)
		}
	}
	return out
}

// defaultIndexName is the server's name for an index: field_direction joined by "_".
func defaultIndexName(keys []string) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		f, dir, ok := strings.Cut(k, ":")
		if !ok {
			dir = "1"
		}
		parts[i] = f + "_" + dir
	}
	return strings.Join(parts, "_")
}

func indexName(v jsValue) string {
	switch x := v.(type) {
	case string:
		return x
	case jsObject:
		return defaultIndexName(keyList(x))
	}
	return ""
}

func (a *mongoAnalysis) tier(info *CollectionInfo) score.Rating {
	switch {
	case info == nil || info.Bytes == 0 && info.Count == 0:
		return score.Medium
	case info.Bytes >= 100<<30 || info.Count >= 100_000_000:
		return score.High
	case info.Bytes >= 1<<30 || info.Count >= 1_000_000:
		return score.Medium
	}
	return score.Low
}

func docsText(info *CollectionInfo) string {
	if info == nil {
		return "documents of unknown count"
	}
	return fmt.Sprintf("about %d documents", info.Count)
}

func sizeText(info *CollectionInfo) string {
	if info == nil || info.Bytes == 0 {
		return "size unknown"
	}
	if info.Bytes >= 1<<30 {
		return fmt.Sprintf("%.1f GiB", float64(info.Bytes)/(1<<30))
	}
	return fmt.Sprintf("%.1f MiB", float64(info.Bytes)/(1<<20))
}

// ---- reading JavaScript ----

// jsValue is a parsed JavaScript literal: jsObject, []jsValue, string,
// float64, bool, nil, or jsOpaque for anything else (calls, variables).
type jsValue any

type jsKV struct {
	Key   string
	Value jsValue
}

// jsObject keeps keys in order, as index keys need.
type jsObject []jsKV

func (o jsObject) get(k string) jsValue {
	for _, kv := range o {
		if kv.Key == k {
			return kv.Value
		}
	}
	return nil
}

type jsOpaque string

type jsToken struct {
	text string
	str  *string // the value of a string literal
	line int
}

// jsTokens splits JavaScript into identifiers, numbers, strings and
// punctuation, dropping comments; template literals become one string.
func jsTokens(s string) []jsToken {
	var out []jsToken
	line := 1
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\n':
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case strings.HasPrefix(s[i:], "//"):
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case strings.HasPrefix(s[i:], "/*"):
			end := strings.Index(s[i+2:], "*/")
			stop := len(s)
			if end >= 0 {
				stop = i + 2 + end + 2
			}
			line += strings.Count(s[i:stop], "\n")
			i = stop
		case c == '\'' || c == '"' || c == '`':
			start := line
			var b strings.Builder
			j := i + 1
			for j < len(s) && s[j] != c {
				if s[j] == '\\' && j+1 < len(s) {
					j++
				}
				if s[j] == '\n' {
					line++
				}
				b.WriteByte(s[j])
				j++
			}
			v := b.String()
			out = append(out, jsToken{text: s[i:min(j+1, len(s))], str: &v, line: start})
			i = min(j+1, len(s))
		case c == '_' || c == '$' || unicode.IsLetter(rune(c)) || unicode.IsDigit(rune(c)) || c == '-' && i+1 < len(s) && unicode.IsDigit(rune(s[i+1])):
			j := i + 1
			for j < len(s) && (s[j] == '_' || s[j] == '$' || s[j] == '.' && unicode.IsDigit(rune(s[i])) || unicode.IsLetter(rune(s[j])) || unicode.IsDigit(rune(s[j]))) {
				j++
			}
			out = append(out, jsToken{text: s[i:j], line: line})
			i = j
		default:
			out = append(out, jsToken{text: string(c), line: line})
			i++
		}
	}
	return out
}

// dbMethods are database methods, not collection names, after "db.".
var dbMethods = map[string]bool{"createCollection": true, "runCommand": true, "adminCommand": true, "command": true, "dropDatabase": true,
	"getCollection": true, "collection": true, "getSiblingDB": true, "getName": true, "listCollections": true}

// findCalls finds db.<coll>.<method>(...), <x>.collection("c").<method>(...),
// <x>.getCollection("c").<method>(...) and db-level calls in a script.
func findCalls(src string) []call {
	toks := jsTokens(src)
	var out []call
	is := func(i int, s string) bool { return i < len(toks) && toks[i].text == s }
	ident := func(i int) bool {
		return i < len(toks) && toks[i].str == nil && toks[i].text != "" && (unicode.IsLetter(rune(toks[i].text[0])) || toks[i].text[0] == '_' || toks[i].text[0] == '$')
	}
	for i := 0; i < len(toks); i++ {
		if !ident(i) || !is(i+1, ".") || !ident(i+2) {
			continue
		}
		var coll, method string
		var open int
		switch name := toks[i+2].text; {
		case (name == "collection" || name == "getCollection") && is(i+3, "(") && i+4 < len(toks) && toks[i+4].str != nil && is(i+5, ")") && is(i+6, ".") && ident(i+7) && is(i+8, "("):
			coll, method, open = *toks[i+4].str, toks[i+7].text, i+8
		case toks[i].text == "db" && dbMethods[name] && is(i+3, "("):
			method, open = name, i+3
		case toks[i].text == "db" && !dbMethods[name] && is(i+3, ".") && ident(i+4) && is(i+5, "("):
			coll, method, open = name, toks[i+4].text, i+5
		default:
			continue
		}
		args, end := callArgs(toks, open)
		first := toks[i].line
		var text []string
		for k := i; k <= end && k < len(toks); k++ {
			text = append(text, toks[k].text)
		}
		out = append(out, call{Collection: coll, Method: method, Args: args, Text: joinTokens(text), Line: first, End: toks[min(end, len(toks)-1)].line})
		i = end
	}
	return out
}

// callArgs parses the arguments of the call whose "(" is toks[open] and
// returns them with the index of the closing ")".
func callArgs(toks []jsToken, open int) ([]jsValue, int) {
	var args []jsValue
	i := open + 1
	for i < len(toks) && toks[i].text != ")" {
		v, next := parseJS(toks, i)
		args = append(args, v)
		i = next
		for i < len(toks) && toks[i].text != "," && toks[i].text != ")" {
			_, i = parseJS(toks, i) // skip what the literal reader did not take, such as "new Date()"
		}
		if i < len(toks) && toks[i].text == "," {
			i++
		}
	}
	return args, i
}

// parseJS reads one literal at toks[i]; anything else is opaque up to the
// next top-level comma or closing bracket.
func parseJS(toks []jsToken, i int) (jsValue, int) {
	if i >= len(toks) {
		return nil, i
	}
	t := toks[i]
	switch {
	case t.str != nil:
		return *t.str, i + 1
	case t.text == "{":
		var o jsObject
		i++
		for i < len(toks) && toks[i].text != "}" {
			key := toks[i].text
			if toks[i].str != nil {
				key = *toks[i].str
			}
			i++
			if i < len(toks) && toks[i].text == ":" {
				var v jsValue
				v, i = parseJS(toks, i+1)
				o = append(o, jsKV{Key: key, Value: v})
			}
			for i < len(toks) && toks[i].text != "," && toks[i].text != "}" {
				_, i = parseJS(toks, i)
			}
			if i < len(toks) && toks[i].text == "," {
				i++
			}
		}
		return o, i + 1
	case t.text == "[":
		var a []jsValue
		i++
		for i < len(toks) && toks[i].text != "]" {
			var v jsValue
			v, i = parseJS(toks, i)
			a = append(a, v)
			for i < len(toks) && toks[i].text != "," && toks[i].text != "]" {
				_, i = parseJS(toks, i)
			}
			if i < len(toks) && toks[i].text == "," {
				i++
			}
		}
		return a, i + 1
	case t.text == "true" || t.text == "false":
		return t.text == "true", i + 1
	case t.text == "null" || t.text == "undefined":
		return nil, i + 1
	case t.text == "(":
		depth := 0
		j := i
		for ; j < len(toks); j++ {
			if toks[j].text == "(" {
				depth++
			} else if toks[j].text == ")" {
				if depth--; depth == 0 {
					break
				}
			}
		}
		return jsOpaque("(...)"), j + 1
	}
	if f, err := strconv.ParseFloat(t.text, 64); err == nil {
		return f, i + 1
	}
	return jsOpaque(t.text), i + 1
}

// joinTokens rebuilds readable text from tokens for the report.
func joinTokens(ts []string) string {
	var b strings.Builder
	for i, t := range ts {
		if i > 0 {
			prev := ts[i-1]
			if !(t == "." || t == "(" || t == ")" || t == "," || t == ":" || t == "]" || t == "}" || prev == "." || prev == "(" || prev == "[" || prev == "{") {
				b.WriteByte(' ')
			}
		}
		b.WriteString(t)
	}
	return b.String()
}

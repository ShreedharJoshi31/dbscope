package elastic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/apply"
	"github.com/dbscope/dbscope/internal/migrate/impact"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/score"
	"github.com/dbscope/dbscope/internal/schema"
)

// Base is the cluster migrations are checked against.
type Base struct {
	Server Server
	// Indices is keyed by index name.
	Indices map[string]*IndexInfo
	// Aliases maps an alias or data stream to its indices.
	Aliases map[string][]string
}

// IndexInfo is what the rules need to know about one index.
type IndexInfo struct {
	// Fields maps a dotted field path to its type.
	Fields     map[string]string
	FieldCount int
	FieldLimit int
	Shards     int
	Replicas   int
	// Bytes is the primary store size.
	Bytes      int64
	WriteBlock bool
}

// ReadBase reads mappings, settings and store sizes of the indices matching
// the client's pattern (the same reads as Introspect).
func ReadBase(ctx context.Context, c *Client) (*Base, error) {
	srv, err := c.describe(ctx)
	if err != nil {
		return nil, err
	}
	b := &Base{Server: srv, Indices: map[string]*IndexInfo{}, Aliases: map[string][]string{}}
	pattern := url.PathEscape(c.Pattern)
	var resolved struct {
		Indices []struct {
			Name string `json:"name"`
		} `json:"indices"`
		Aliases []struct {
			Name    string   `json:"name"`
			Indices []string `json:"indices"`
		} `json:"aliases"`
		DataStreams []struct {
			Name           string   `json:"name"`
			BackingIndices []string `json:"backing_indices"`
		} `json:"data_streams"`
	}
	if err := c.do(ctx, "GET", "/_resolve/index/"+pattern, nil, &resolved); err != nil {
		return nil, fmt.Errorf("listing indices: %w", err)
	}
	if len(resolved.Indices) == 0 {
		return b, nil
	}
	for _, a := range resolved.Aliases {
		b.Aliases[a.Name] = a.Indices
	}
	for _, ds := range resolved.DataStreams {
		b.Aliases[ds.Name] = ds.BackingIndices
	}
	var mappings map[string]struct {
		Mappings map[string]any `json:"mappings"`
	}
	if err := c.do(ctx, "GET", "/"+pattern+"/_mapping", nil, &mappings); err != nil {
		return nil, fmt.Errorf("reading mappings: %w", err)
	}
	var settings map[string]struct {
		Settings map[string]string `json:"settings"`
	}
	if err := c.do(ctx, "GET", "/"+pattern+"/_settings?flat_settings=true&include_defaults=false", nil, &settings); err != nil {
		return nil, fmt.Errorf("reading settings: %w", err)
	}
	var stats struct {
		Indices map[string]struct {
			Primaries struct {
				Store struct {
					Size int64 `json:"size_in_bytes"`
				} `json:"store"`
			} `json:"primaries"`
		} `json:"indices"`
	}
	c.do(ctx, "GET", "/"+pattern+"/_stats/store", nil, &stats) // sizes need monitor; without it they stay unknown
	for _, ix := range resolved.Indices {
		r := schema.Relation{}
		n, _ := flatten(mappings[ix.Name].Mappings, &r)
		info := &IndexInfo{Fields: map[string]string{}, FieldCount: n, FieldLimit: 1000, Shards: 1, Replicas: 1,
			Bytes: stats.Indices[ix.Name].Primaries.Store.Size}
		for _, col := range r.Columns {
			info.Fields[col.Name] = col.Type
		}
		s := settings[ix.Name].Settings
		atoi := func(k string, dflt int) int {
			if v, err := strconv.Atoi(s[k]); err == nil {
				return v
			}
			return dflt
		}
		info.Shards, info.Replicas = atoi("index.number_of_shards", 1), atoi("index.number_of_replicas", 1)
		info.FieldLimit = atoi("index.mapping.total_fields.limit", 1000)
		info.WriteBlock = s["index.blocks.write"] == "true" || s["index.blocks.read_only"] == "true"
		b.Indices[ix.Name] = info
	}
	return b, nil
}

// request is one console-format request of a migration file: a method and
// path line, then an optional JSON body.
type request struct {
	Method, Path string
	Query        url.Values
	Body         map[string]any
	BodyErr      error
	Text         string
	Line, End    int
}

var requestLine = regexp.MustCompile(`^(GET|PUT|POST|DELETE|HEAD|PATCH)\s+(\S+)\s*$`)

// splitRequests reads Kibana Dev Tools console text, the form
// elasticsearch-evolution migration files use: "PUT /index/_mapping" then
// the body. Header lines (Content-Type: ...) and # or // comments are skipped.
func splitRequests(text string) []request {
	var out []request
	var cur *request
	var body []string
	flush := func() {
		if cur == nil {
			return
		}
		raw := strings.TrimSpace(strings.Join(body, "\n"))
		if raw != "" {
			dec := json.NewDecoder(strings.NewReader(raw))
			dec.UseNumber()
			cur.BodyErr = dec.Decode(&cur.Body)
		}
		cur.Text = strings.TrimSpace(cur.Method + " " + cur.Path + "\n" + raw)
		out = append(out, *cur)
		cur, body = nil, nil
	}
	for i, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if m := requestLine.FindStringSubmatch(trimmed); m != nil {
			flush()
			p, q, _ := strings.Cut(m[2], "?")
			query, _ := url.ParseQuery(q)
			cur = &request{Method: m[1], Path: "/" + strings.TrimPrefix(p, "/"), Query: query, Line: i + 1, End: i + 1}
			continue
		}
		if cur == nil || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") ||
			len(body) == 0 && strings.Contains(trimmed, ":") && !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "\"") {
			continue
		}
		body = append(body, line)
		if trimmed != "" {
			cur.End = i + 1
		}
	}
	flush()
	return out
}

// staticSettings can be set only at index creation or on a closed index.
var staticSettings = []string{"number_of_shards", "number_of_routing_shards", "codec", "routing_partition_size", "soft_deletes",
	"store.type", "shard.check_on_startup", "analysis", "sort", "mapping.source.mode", "load_fixed_bitset_filters_eagerly"}

// Analyze rates console-format migration requests against base. Mapping and
// setting changes apply without blocking; the risks are requests the
// cluster refuses (checked on Elasticsearch 9.5, 8.19 and OpenSearch 3.9,
// 2.19), whole-index rewrites, data loss and indices made unavailable
// (docs/ENGINES.md, "Elasticsearch and OpenSearch").
func Analyze(base *Base, files []ingest.Input) *migrate.Report {
	rep := &migrate.Report{Engine: base.Server.Product, PGVersion: versionNum(base.Server.Version), Rating: score.Low, Statements: []migrate.Statement{},
		Impact: map[string][]impact.Item{}, Unanalyzable: []migrate.Unanalyzable{}, Warnings: []string{}, Base: "live",
		Diff: apply.Diff{Added: []string{}, Dropped: []string{}, Altered: []string{}, Columns: []apply.ColumnChange{}}}
	if len(base.Indices) == 0 {
		rep.Base = "none"
	}
	a := &esAnalysis{base: base, rep: rep, altered: map[string]bool{}, blocked: map[string]bool{}}
	for _, f := range files {
		reqs := splitRequests(f.SQL)
		if len(reqs) == 0 && strings.TrimSpace(f.SQL) != "" {
			rep.Unanalyzable = append(rep.Unanalyzable, migrate.Unanalyzable{File: f.Name, Line: 1,
				Message: "no requests found: write each as a method and path line (PUT /index/_mapping) followed by its JSON body"})
		}
		for _, r := range reqs {
			st := a.statement(f.Name, r)
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

type esAnalysis struct {
	base    *Base
	rep     *migrate.Report
	altered map[string]bool
	// blocked are indices an earlier request in the script put a write block on.
	blocked map[string]bool
}

// targets resolves an index expression (comma-separated names, wildcards,
// aliases and data streams) to the known indices it names.
func (a *esAnalysis) targets(expr string) []string {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(expr, ",") {
		part = strings.TrimSpace(part)
		if ix, ok := a.base.Aliases[part]; ok {
			for _, i := range ix {
				if !seen[i] {
					seen[i] = true
					out = append(out, i)
				}
			}
			continue
		}
		for name := range a.base.Indices {
			if ok, _ := path.Match(part, name); (ok || part == "_all") && !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (a *esAnalysis) bytes(indices []string) (int64, bool) {
	var n int64
	known := len(indices) > 0
	for _, i := range indices {
		n += a.base.Indices[i].Bytes
		known = known && a.base.Indices[i].Bytes > 0
	}
	return n, known
}

func (a *esAnalysis) statement(file string, r request) (out migrate.Statement) {
	out = migrate.Statement{File: file, Line: r.Line, Column: 1, EndLine: r.End, SQL: r.Text,
		Tx: "none", Locks: []migrate.Lock{}, Work: "none", Rating: score.Low, Findings: []migrate.Finding{}}
	add := func(id string, rt score.Rating, msg, alt string) {
		out.Findings = append(out.Findings, migrate.Finding{RuleID: id, Rating: rt, Message: msg, SafeAlternative: alt})
		out.Rating = score.Max(out.Rating, rt)
	}
	defer func() {
		out.Score = map[score.Rating]int{score.Low: 0, score.Medium: 4, score.High: 8, score.Critical: 12}[out.Rating]
	}()
	fails := func(msg, alt string) { add("DBS-ES-FAILS", score.High, msg, alt) }
	segs := strings.Split(strings.TrimPrefix(r.Path, "/"), "/")
	target, api := segs[0], ""
	if strings.HasPrefix(target, "_") {
		target, api = "", segs[0]
	} else if len(segs) > 1 {
		api = segs[1]
	}
	out.Kind = strings.TrimSpace(r.Method + " " + api)
	if api == "" {
		out.Kind = r.Method + " index"
	}
	if r.BodyErr != nil {
		add("DBS-ES-BODY", score.High, "The body is not valid JSON, so the request fails: "+r.BodyErr.Error(), "")
		return out
	}
	indices := a.targets(target)
	sizeOf := func(ix []string) string {
		n, known := a.bytes(ix)
		if !known {
			return "size unknown"
		}
		return size(n)
	}
	tier := func(ix []string) score.Rating {
		n, known := a.bytes(ix)
		switch {
		case !known:
			return score.Medium
		case n >= 100<<30:
			return score.High
		case n >= 1<<30:
			return score.Medium
		}
		return score.Low
	}
	named := target
	if named == "" {
		named = "the cluster"
	}

	switch {
	case r.Method == "PUT" && api == "" && target != "":
		out.Kind = "PUT index"
		if _, exists := a.base.Indices[target]; exists {
			fails("Index "+target+" already exists, so creating it fails", "Change its mapping with PUT /"+target+"/_mapping, or create a new index and reindex")
			return out
		}
		a.rep.Diff.Added = append(a.rep.Diff.Added, target)
	case r.Method == "DELETE" && api == "":
		out.Kind = "DELETE index"
		a.rep.Diff.Dropped = append(a.rep.Diff.Dropped, indices...)
		add("DBS-ES-DROPS-DATA", score.High, fmt.Sprintf("DELETE %s removes %d indices and their documents (%s)", named, len(indices), sizeOf(indices)),
			"Take a snapshot first; wildcard deletes are refused when action.destructive_requires_name is true")
	case api == "_mapping" && (r.Method == "PUT" || r.Method == "POST"):
		a.mapping(indices, named, r.Body, add, fails)
	case api == "_settings" && r.Method == "PUT":
		a.settings(indices, named, r.Body, add, fails, tier, sizeOf)
	case api == "_reindex":
		src, dst := reindexIndices(r.Body)
		from := a.targets(src)
		out.Work = "rewrite"
		add("DBS-ES-REINDEX", score.Max(score.Medium, tier(from)), fmt.Sprintf("_reindex copies every document of %s (%s) into %s, indexing each one again", src, sizeOf(from), dst),
			"Run it with wait_for_completion=false and slices=auto, then switch an alias to the new index")
		if _, ok := a.base.Indices[dst]; !ok && !a.created(dst) {
			add("DBS-ES-DYNAMIC-DEST", score.Medium, "The destination "+dst+" does not exist yet, so it is created with dynamic mappings guessed from the documents",
				"Create "+dst+" with its mappings before reindexing")
		}
	case api == "_update_by_query":
		out.Work = "rewrite"
		add("DBS-ES-UPDATE-BY-QUERY", score.Max(score.Medium, tier(indices)), fmt.Sprintf("_update_by_query reindexes every matching document of %s (%s)", named, sizeOf(indices)),
			"Run it with wait_for_completion=false, slices=auto and conflicts=proceed")
	case api == "_delete_by_query":
		add("DBS-ES-DROPS-DATA", score.High, "_delete_by_query deletes the matching documents of "+named, "Take a snapshot first, or check the query with _count")
	case api == "_close":
		add("DBS-ES-CLOSE", score.High, "Closing "+named+" makes it unavailable to reads and writes until it is opened", "")
	case api == "_forcemerge":
		out.Work = "rewrite"
		add("DBS-ES-FORCEMERGE", score.Max(score.Medium, tier(indices)), fmt.Sprintf("_forcemerge rewrites the segments of %s (%s); on an index still written to it leaves segments that never merge again", named, sizeOf(indices)),
			"Force-merge only indices no longer written to")
	case api == "_split" || api == "_shrink" || api == "_clone":
		out.Work = "rewrite"
		for _, i := range indices {
			if !a.base.Indices[i].WriteBlock && !a.blocked[i] {
				fails(api+" requires a write block on "+i+" first", "PUT /"+i+"/_settings {\"index.blocks.write\": true} before it")
			}
		}
		if len(out.Findings) == 0 {
			add("DBS-ES-RESIZE", tier(indices), fmt.Sprintf("%s builds a new index from %s (%s) by hard-linking or copying segments", api, named, sizeOf(indices)), "")
		}
	case strings.HasSuffix(api, "_template"):
		add("DBS-ES-TEMPLATE", score.Low, "Templates apply to indices created from now on; existing indices keep their mappings and settings", "")
	}
	return out
}

func (a *esAnalysis) created(index string) bool {
	for _, n := range a.rep.Diff.Added {
		if n == index {
			return true
		}
	}
	return false
}

// mapping checks a PUT _mapping body's fields against the indices' mappings.
func (a *esAnalysis) mapping(indices []string, named string, body map[string]any, add func(string, score.Rating, string, string), fails func(string, string)) {
	r := schema.Relation{}
	n, _ := flatten(body, &r)
	added := 0
	for _, i := range indices {
		info := a.base.Indices[i]
		a.altered[i] = true
		newFields := 0
		for _, col := range r.Columns {
			old, exists := info.Fields[col.Name]
			switch {
			case !exists:
				newFields++
			case old != col.Type && col.Type != "object" && old != "object":
				fails(fmt.Sprintf("%s.%s is mapped as %s and cannot become %s: a field's type never changes in place", i, col.Name, old, col.Type),
					"Create a new index with the new mapping, _reindex into it, and switch an alias")
			}
		}
		if info.FieldCount+newFields > info.FieldLimit {
			fails(fmt.Sprintf("%s would have %d fields, above its limit of %d (index.mapping.total_fields.limit)", i, info.FieldCount+newFields, info.FieldLimit), "")
		}
		added = max(added, newFields)
	}
	for _, col := range r.Columns {
		for _, i := range indices {
			if _, ok := a.base.Indices[i].Fields[col.Name]; !ok {
				a.rep.Diff.Columns = append(a.rep.Diff.Columns, apply.ColumnChange{Relation: i, Column: col.Name, Change: "added"})
			}
		}
	}
	if added > 0 && n > 0 {
		add("DBS-ES-NEW-FIELDS", score.Low, fmt.Sprintf("New fields apply to documents indexed from now on; existing documents in %s are not searchable by them until reindexed or updated", named),
			"Run _update_by_query on the index after the mapping change if old documents must match")
	}
}

// settings checks a PUT _settings body: static settings fail on an open
// index, replica changes move data, and blocks stop writes.
func (a *esAnalysis) settings(indices []string, named string, body map[string]any, add func(string, score.Rating, string, string), fails func(string, string),
	tier func([]string) score.Rating, sizeOf func([]string) string) {
	flat := map[string]string{}
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, v := range m {
			if sub, ok := v.(map[string]any); ok {
				walk(prefix+k+".", sub)
				continue
			}
			flat[strings.TrimPrefix(prefix+k, "index.")] = fmt.Sprint(v)
		}
	}
	walk("", body)
	for k, v := range flat {
		for _, s := range staticSettings {
			if k == s || strings.HasPrefix(k, s+".") {
				fails(k+" is a static setting: it can be set only when the index is created or while it is closed", "Create a new index with it and _reindex")
			}
		}
		switch k {
		case "number_of_replicas":
			n, _ := strconv.Atoi(v)
			if n == 0 {
				add("DBS-ES-NO-REPLICAS", score.Medium, "With 0 replicas, losing one node loses the shards it held", "")
			}
			for _, i := range indices {
				if n > a.base.Indices[i].Replicas {
					add("DBS-ES-REPLICAS", tier(indices), fmt.Sprintf("Adding replicas copies %s (%s) over the network to other nodes", named, sizeOf(indices)), "")
					break
				}
			}
		case "blocks.write", "blocks.read_only":
			if v == "true" {
				for _, i := range indices {
					a.blocked[i] = true
				}
				add("DBS-ES-WRITE-BLOCK", score.High, "Writes to "+named+" fail until the block is removed", "Remove it in the same migration once the operation that needed it is done")
			}
		}
	}
	for _, i := range indices {
		a.altered[i] = true
	}
}

// reindexIndices reads source.index and dest.index of a _reindex body.
func reindexIndices(body map[string]any) (src, dst string) {
	if s, ok := body["source"].(map[string]any); ok {
		switch v := s["index"].(type) {
		case string:
			src = v
		case []any:
			var parts []string
			for _, x := range v {
				parts = append(parts, fmt.Sprint(x))
			}
			src = strings.Join(parts, ",")
		}
	}
	if d, ok := body["dest"].(map[string]any); ok {
		dst, _ = d["index"].(string)
	}
	return src, dst
}

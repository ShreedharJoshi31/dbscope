// Package elastic maps Elasticsearch and OpenSearch indices for dbscope
// (docs/ENGINES.md): fields from mappings, shards, replicas and size, the
// fields an alias maps differently across its indices, and mapping risks;
// and it runs read-only searches and SQL with their cost checked first.
// It speaks plain HTTP: Elastic's Go client refuses OpenSearch servers.
package elastic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/schema"
)

// Client talks to one cluster.
type Client struct {
	base     string
	http     *http.Client
	user     string
	password string // with no user, an API key ("encoded" from the create API key response)
	Pattern  string // the indices dbscope reads, from Profile.Database

	mu      sync.Mutex
	product string // learned from the first GET /
}

// Product is elasticsearch or opensearch, asked of the server once.
func (c *Client) Product(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.product == "" {
		srv, err := c.describe(ctx)
		if err != nil {
			return "", err
		}
		c.product = srv.Product
	}
	return c.product, nil
}

// Connect builds a client. sslMode disable and prefer use http (HTTP has no
// "TLS if offered"); require trusts any certificate; verify-ca and
// verify-full check it.
func Connect(p conn.Profile, password string) (*Client, error) {
	scheme := "http"
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if p.SSLMode != conn.SSLDisable && p.SSLMode != conn.SSLPrefer {
		scheme = "https"
		cfgs, err := conn.TLSConfigs(p)
		if err != nil {
			return nil, err
		}
		tr.TLSClientConfig = cfgs[0]
	}
	pattern := p.Database
	if pattern == "" {
		pattern = "*"
	}
	return &Client{base: fmt.Sprintf("%s://%s", scheme, netHost(p)), http: &http.Client{Transport: tr, Timeout: 2 * time.Minute},
		user: p.User, password: password, Pattern: pattern}, nil
}

func netHost(p conn.Profile) string {
	h := p.Host
	if strings.Contains(h, ":") {
		h = "[" + h + "]"
	}
	return h + ":" + strconv.Itoa(p.Port)
}

// Error is an error response from the server.
type Error struct {
	Status int
	Type   string // error.type, such as security_exception
	Reason string
}

func (e *Error) Error() string {
	if e.Type != "" {
		return fmt.Sprintf("%s: %s", e.Type, e.Reason)
	}
	return e.Reason
}

// do sends a request and decodes a JSON response into out (when not nil).
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	switch {
	case c.user != "":
		req.SetBasicAuth(c.user, c.password)
	case c.password != "":
		req.Header.Set("Authorization", "ApiKey "+c.password)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return errorFrom(resp.StatusCode, data)
	}
	if out != nil {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber() // documents' integers keep every digit
		if err := dec.Decode(out); err != nil {
			return fmt.Errorf("unexpected response to %s %s: %w", method, path, err)
		}
	}
	return nil
}

func errorFrom(status int, data []byte) error {
	var e struct {
		Error json.RawMessage `json:"error"`
	}
	json.Unmarshal(data, &e)
	var detail struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
		Root   []struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		} `json:"root_cause"`
		Details string `json:"details"` // OpenSearch SQL
	}
	if json.Unmarshal(e.Error, &detail) != nil {
		var s string
		if json.Unmarshal(e.Error, &s) == nil {
			return &Error{Status: status, Reason: s}
		}
		return &Error{Status: status, Reason: strings.TrimSpace(string(data))}
	}
	if len(detail.Root) > 0 && detail.Reason == "" {
		detail.Type, detail.Reason = detail.Root[0].Type, detail.Root[0].Reason
	}
	return &Error{Status: status, Type: detail.Type, Reason: detail.Reason}
}

// Server is the product and version.
type Server struct {
	Product string // elasticsearch or opensearch
	Version string
}

func (c *Client) describe(ctx context.Context) (Server, error) {
	var root struct {
		Version struct {
			Number       string `json:"number"`
			Distribution string `json:"distribution"`
		} `json:"version"`
	}
	if err := c.do(ctx, "GET", "/", nil, &root); err != nil {
		return Server{}, err
	}
	s := Server{Product: "elasticsearch", Version: root.Version.Number}
	if root.Version.Distribution == "opensearch" {
		s.Product = "opensearch"
	}
	return s, nil
}

// readOnlyRoles are OpenSearch's built-in roles that cannot write.
var readOnlyRoles = map[string]bool{"readall": true, "readall_and_monitor": true}

// Test connects and reports the product and what the user may change.
// Writes are refused only by the cluster's security: dbscope sends nothing
// but reads, but a cluster with security off lets anyone write.
func Test(ctx context.Context, p conn.Profile, password string) conn.TestResult {
	res := conn.TestResult{Engine: conn.EngineElasticsearch, Warnings: []string{}}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, err := Connect(p, password)
	if err != nil {
		res.Error = classify(err)
		return res
	}
	if !strings.HasPrefix(c.base, "https") && p.Host != "localhost" && p.Host != "127.0.0.1" && p.Host != "::1" {
		res.Warnings = append(res.Warnings, "the connection uses http, so the password and results travel in plain text; use sslMode require or verify-full.")
	}
	start := time.Now()
	srv, err := c.describe(ctx)
	if err != nil {
		res.Error = classify(err)
		return res
	}
	ms := time.Since(start).Milliseconds()
	res.OK, res.LatencyMs, res.ServerVersion, res.ServerVersionNum = true, &ms, srv.Version, versionNum(srv.Version)
	if srv.Product == "opensearch" {
		res.Engine = conn.EngineOpenSearch
		var info struct {
			Roles []string `json:"roles"`
		}
		if err := c.do(ctx, "GET", "/_plugins/_security/authinfo", nil, &info); err != nil {
			res.Warnings = append(res.Warnings, securityOff(err, "the security plugin"))
			return res
		}
		for _, r := range info.Roles {
			switch {
			case readOnlyRoles[r] || strings.HasPrefix(r, "dbscope"):
			case r == "own_index": // the 2.x demo configuration gives it to every user
				res.WritePrivileges = append(res.WritePrivileges, "role own_index (writes only to an index named after the user)")
			default:
				res.WritePrivileges = append(res.WritePrivileges, "role "+r)
			}
		}
		return res
	}
	var priv struct {
		Cluster map[string]bool            `json:"cluster"`
		Index   map[string]map[string]bool `json:"index"`
	}
	err = c.do(ctx, "POST", "/_security/user/_has_privileges", map[string]any{
		"cluster": []string{"all", "manage"},
		"index":   []map[string]any{{"names": []string{c.Pattern}, "privileges": []string{"write", "delete", "create_index", "manage"}}},
	}, &priv)
	if err != nil {
		res.Warnings = append(res.Warnings, securityOff(err, "Elasticsearch security"))
		return res
	}
	for _, k := range []string{"all", "manage"} {
		if priv.Cluster[k] {
			res.WritePrivileges = append(res.WritePrivileges, "cluster "+k)
		}
	}
	for _, privs := range priv.Index {
		for _, k := range []string{"write", "delete", "create_index", "manage"} {
			if privs[k] {
				res.WritePrivileges = append(res.WritePrivileges, k+" on "+c.Pattern)
			}
		}
	}
	return res
}

func securityOff(err error, what string) string {
	var e *Error
	if errors.As(err, &e) && (e.Status == 404 || e.Status == 400 || e.Status == 500) {
		return "the cluster answered without " + what + ", so any client can write; dbscope sends only reads, but a user with only read privileges is the real guard (docs/ENGINES.md)"
	}
	return "dbscope could not check this user's privileges: " + err.Error()
}

func classify(err error) *conn.TestError {
	var e *Error
	if errors.As(err, &e) {
		kind := "database"
		if e.Status == 401 || e.Status == 403 {
			kind = "auth"
		}
		return &conn.TestError{Kind: kind, Message: e.Error()}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &conn.TestError{Kind: "timeout", Message: err.Error()}
	}
	return conn.Classify(err)
}

// Mapping limits (docs/research-elasticsearch.md, 2): total_fields is the same
// on both products; risks are flagged from 80% of the limit.
const defaultTotalFields = 1000

// Introspect maps every open index the pattern matches, except dot-prefixed
// system and backing indices; aliases become views over their indices whose
// columns show where the indices disagree on a field's type.
func Introspect(ctx context.Context, c *Client) (*schema.Model, error) {
	start := time.Now()
	srv, err := c.describe(ctx)
	if err != nil {
		return nil, err
	}
	pattern := url.PathEscape(c.Pattern)
	var resolved struct {
		Indices []struct {
			Name    string   `json:"name"`
			Aliases []string `json:"aliases"`
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
	m := &schema.Model{Engine: srv.Product, ServerVersion: versionNum(srv.Version), Schemas: []string{"indices"}, Relations: []schema.Relation{}, Enums: []schema.Enum{}}
	visible := func(name string) bool { return !strings.HasPrefix(name, ".") || strings.HasPrefix(c.Pattern, ".") }

	var mappings map[string]struct {
		Mappings map[string]any `json:"mappings"`
	}
	if err := c.do(ctx, "GET", "/"+pattern+"/_mapping", nil, &mappings); err != nil {
		return nil, fmt.Errorf("reading mappings: %w", err)
	}
	var settings map[string]struct {
		Settings map[string]string `json:"settings"`
	}
	if err := c.do(ctx, "GET", "/"+pattern+"/_settings?flat_settings=true", nil, &settings); err != nil {
		return nil, fmt.Errorf("reading settings: %w", err)
	}
	var stats struct {
		Indices map[string]struct {
			Primaries struct {
				Docs  struct{ Count int64 } `json:"docs"`
				Store struct {
					Size int64 `json:"size_in_bytes"`
				} `json:"store"`
			} `json:"primaries"`
			Total struct {
				Store struct {
					Size int64 `json:"size_in_bytes"`
				} `json:"store"`
			} `json:"total"`
		} `json:"indices"`
	}
	if err := c.do(ctx, "GET", "/"+pattern+"/_stats/docs,store", nil, &stats); err != nil {
		return nil, fmt.Errorf("reading index stats: %w", err)
	}

	docs, err := c.topLevelDocs(ctx, pattern)
	if err != nil {
		return nil, fmt.Errorf("counting documents: %w", err)
	}
	for _, ix := range resolved.Indices {
		if !visible(ix.Name) {
			continue
		}
		r := schema.Relation{Schema: "indices", Name: ix.Name, Kind: schema.KindSearchIndex, Columns: []schema.Column{}}
		fields, dynamic := flatten(mappings[ix.Name].Mappings, &r)
		st := stats.Indices[ix.Name]
		n := docs[ix.Name]
		r.RowEstimate = &n
		r.Comment = indexComment(settings[ix.Name].Settings, st.Primaries.Store.Size, n, fields, dynamic, ix.Aliases)
		m.Relations = append(m.Relations, r)
	}
	for _, ds := range resolved.DataStreams {
		if !visible(ds.Name) || len(ds.BackingIndices) == 0 {
			continue
		}
		write := ds.BackingIndices[len(ds.BackingIndices)-1]
		r := schema.Relation{Schema: "indices", Name: ds.Name, Kind: schema.KindSearchIndex, Columns: []schema.Column{}}
		fields, dynamic := flatten(mappings[write].Mappings, &r)
		var n, size int64
		for _, b := range ds.BackingIndices {
			n += docs[b]
			size += stats.Indices[b].Primaries.Store.Size
		}
		r.RowEstimate = &n
		r.Comment = fmt.Sprintf("data stream over %d backing indices; fields of the write index %s; ", len(ds.BackingIndices), write) +
			indexComment(settings[write].Settings, size, n, fields, dynamic, nil)
		m.Relations = append(m.Relations, r)
	}
	for i, a := range resolved.Aliases {
		if !visible(a.Name) || i >= 50 {
			continue
		}
		r, err := c.aliasRelation(ctx, a.Name, a.Indices)
		if err != nil {
			return nil, fmt.Errorf("reading fields of alias %s: %w", a.Name, err)
		}
		m.Relations = append(m.Relations, r)
	}
	sort.SliceStable(m.Relations, func(i, j int) bool { return m.Relations[i].Name < m.Relations[j].Name })
	m.IntrospectedAt, m.DurationMs = start.UTC(), time.Since(start).Milliseconds()
	return m, nil
}

// topLevelDocs counts each index's documents. Index stats count every
// nested object as a hidden document too, so this asks a search instead.
func (c *Client) topLevelDocs(ctx context.Context, pattern string) (map[string]int64, error) {
	var out struct {
		Aggregations struct {
			Indices struct {
				Buckets []struct {
					Key   string `json:"key"`
					Count int64  `json:"doc_count"`
				} `json:"buckets"`
			} `json:"indices"`
		} `json:"aggregations"`
	}
	body := map[string]any{"size": 0, "aggs": map[string]any{"indices": map[string]any{"terms": map[string]any{"field": "_index", "size": 10000}}}}
	if err := c.do(ctx, "POST", "/"+pattern+"/_search?timeout=10s", body, &out); err != nil {
		return nil, err
	}
	m := map[string]int64{}
	for _, b := range out.Aggregations.Indices.Buckets {
		m[b.Key] = b.Count
	}
	return m, nil
}

// aliasRelation reads an alias's fields with _field_caps, which lists a
// field under each type it has; more than one type is a conflict that breaks
// sorting, aggregations and some queries across the alias.
func (c *Client) aliasRelation(ctx context.Context, alias string, indices []string) (schema.Relation, error) {
	var caps struct {
		Fields map[string]map[string]struct {
			Indices []string `json:"indices"`
		} `json:"fields"`
	}
	r := schema.Relation{Schema: "indices", Name: alias, Kind: schema.KindView, Columns: []schema.Column{},
		Comment: "alias of " + strings.Join(indices, ", ")}
	for _, ix := range indices {
		r.DependsOn = append(r.DependsOn, schema.QualifiedName{Schema: "indices", Name: ix})
	}
	if err := c.do(ctx, "GET", "/"+url.PathEscape(alias)+"/_field_caps?fields=*", nil, &caps); err != nil {
		return r, err
	}
	names := make([]string, 0, len(caps.Fields))
	for n := range caps.Fields {
		if !strings.HasPrefix(n, "_") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	conflicts := 0
	for _, n := range names {
		types := caps.Fields[n]
		var ts []string
		for t := range types {
			ts = append(ts, t)
		}
		sort.Strings(ts)
		if len(ts) == 1 && ts[0] == "object" {
			continue
		}
		col := schema.Column{Name: n, Type: strings.Join(ts, " | "), Nullable: true}
		if len(ts) > 1 {
			conflicts++
			var parts []string
			for _, t := range ts {
				in := types[t].Indices
				if len(in) == 0 {
					in = []string{"the other indices"}
				}
				parts = append(parts, t+" in "+strings.Join(in, ", "))
			}
			col.Comment = "type conflict: " + strings.Join(parts, "; ")
		}
		r.Columns = append(r.Columns, col)
	}
	if conflicts > 0 {
		r.Comment += fmt.Sprintf("; %d fields mapped differently across its indices", conflicts)
	}
	return r, nil
}

// flatten adds a column per field: objects' leaves as dotted paths, nested
// fields with their type, multi-fields under their parent. It returns the
// field count as Elasticsearch counts it against total_fields.limit (every
// object level, leaf, multi-field, alias and runtime field) and the root's
// dynamic setting.
func flatten(mapping map[string]any, r *schema.Relation) (int, string) {
	dynamic := "true"
	if d, ok := mapping["dynamic"]; ok {
		dynamic = fmt.Sprint(d)
	}
	count := 0
	var walk func(prefix string, props map[string]any)
	walk = func(prefix string, props map[string]any) {
		names := make([]string, 0, len(props))
		for n := range props {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			f, _ := props[n].(map[string]any)
			path := prefix + n
			count++
			typ, _ := f["type"].(string)
			sub, hasProps := f["properties"].(map[string]any)
			switch {
			case hasProps && (typ == "" || typ == "object"):
				walk(path+".", sub)
				continue
			case typ == "nested":
				r.Columns = append(r.Columns, schema.Column{Name: path, Type: "nested", Nullable: true, Comment: "nested: each object is a hidden document"})
				walk(path+".", sub)
				continue
			}
			if typ == "" {
				typ = "object"
			}
			r.Columns = append(r.Columns, schema.Column{Name: path, Type: typ, Nullable: true})
			if mf, ok := f["fields"].(map[string]any); ok {
				subs := make([]string, 0, len(mf))
				for s := range mf {
					subs = append(subs, s)
				}
				sort.Strings(subs)
				for _, s := range subs {
					count++
					st, _ := mf[s].(map[string]any)
					t, _ := st["type"].(string)
					r.Columns = append(r.Columns, schema.Column{Name: path + "." + s, Type: t, Nullable: true, Comment: "multi-field of " + path})
				}
			}
		}
	}
	props, _ := mapping["properties"].(map[string]any)
	walk("", props)
	if rt, ok := mapping["runtime"].(map[string]any); ok {
		for n, v := range rt {
			count++
			f, _ := v.(map[string]any)
			t, _ := f["type"].(string)
			r.Columns = append(r.Columns, schema.Column{Name: n, Type: t, Nullable: true, Comment: "runtime field: computed at query time"})
		}
	}
	return count, dynamic
}

// indexComment summarises the index and its risks.
func indexComment(s map[string]string, bytes, docs int64, fields int, dynamic string, aliases []string) string {
	shards, _ := strconv.Atoi(s["index.number_of_shards"])
	parts := []string{fmt.Sprintf("%d shards, %s replicas, %s", shards, s["index.number_of_replicas"], size(bytes))}
	limit := defaultTotalFields
	if v, err := strconv.Atoi(s["index.mapping.total_fields.limit"]); err == nil {
		limit = v
	}
	parts = append(parts, fmt.Sprintf("%d of %d fields", fields, limit), "dynamic: "+dynamic)
	if len(aliases) > 0 {
		parts = append(parts, "aliases: "+strings.Join(aliases, ", "))
	}
	var risks []string
	if fields*5 >= limit*4 {
		risks = append(risks, fmt.Sprintf("near the field limit (%d of %d): new fields will be refused", fields, limit))
	}
	if s["index.number_of_replicas"] == "0" {
		risks = append(risks, "no replicas: a lost node loses its shards")
	}
	for _, b := range []string{"read_only", "read_only_allow_delete", "write", "read", "metadata"} {
		if s["index.blocks."+b] == "true" {
			msg := "block " + b + " is set"
			if b == "read_only_allow_delete" {
				msg += " (usually the disk flood-stage watermark)"
			}
			risks = append(risks, msg)
		}
	}
	if shards > 0 && bytes/int64(shards) > 50<<30 {
		risks = append(risks, fmt.Sprintf("shards average %s, above the 50 GB guidance", size(bytes/int64(shards))))
	}
	if shards > 0 && docs/int64(shards) > 200_000_000 {
		risks = append(risks, "more than 200M documents per shard")
	}
	out := strings.Join(parts, ", ")
	if len(risks) > 0 {
		out += "; risk: " + strings.Join(risks, "; ")
	}
	return out
}

func size(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func versionNum(v string) int {
	var a, b, c int
	fmt.Sscanf(v, "%d.%d.%d", &a, &b, &c)
	return a*10000 + b*100 + c
}

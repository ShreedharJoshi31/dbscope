package elastic

import (
	"context"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/migrate"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
	"github.com/dbscope/dbscope/internal/migrate/score"
	"github.com/dbscope/dbscope/internal/mytest"
)

func hasRule(st migrate.Statement, id string) bool {
	for _, f := range st.Findings {
		if f.RuleID == id {
			return true
		}
	}
	return false
}

func TestSplitRequests(t *testing.T) {
	rs := splitRequests(`# add a field
PUT /logs/_mapping
Content-Type: application/json

{
  "properties": {"host": {"type": "keyword"}}
}

POST _reindex?slices=auto
{"source": {"index": "logs"}, "dest": {"index": "logs-v2"}}
DELETE /old-*`)
	if len(rs) != 3 || rs[0].Line != 2 || rs[0].End != 7 || rs[0].Body["properties"] == nil || rs[1].Query.Get("slices") != "auto" ||
		rs[1].Path != "/_reindex" || rs[2].Method != "DELETE" || rs[2].Body != nil {
		t.Errorf("requests %+v", rs)
	}
}

func TestAnalyzeRequests(t *testing.T) {
	base := &Base{Server: Server{Product: "elasticsearch", Version: "9.5.5"}, Aliases: map[string][]string{"logs": {"logs-1"}},
		Indices: map[string]*IndexInfo{"logs-1": {Fields: map[string]string{"status": "long"}, FieldCount: 1, FieldLimit: 1000, Shards: 1, Replicas: 1, Bytes: 2 << 30}}}
	rep := Analyze(base, []ingest.Input{{Name: "V1__x.http", SQL: `PUT /logs/_mapping
{"properties": {"status": {"type": "keyword"}}}
PUT /logs/_settings
{"index": {"number_of_replicas": 0}}
POST _reindex
{"source": {"index": "logs"}, "dest": {"index": "logs-2"}}
DELETE /logs-1
PUT /logs/_mapping
{"properties": `}})
	want := []struct {
		rating score.Rating
		rule   string
	}{{score.High, "DBS-ES-FAILS"}, {score.Medium, "DBS-ES-NO-REPLICAS"}, {score.Medium, "DBS-ES-DYNAMIC-DEST"}, {score.High, "DBS-ES-DROPS-DATA"}, {score.High, "DBS-ES-BODY"}}
	if len(rep.Statements) != len(want) {
		t.Fatalf("statements %+v", rep.Statements)
	}
	for i, w := range want {
		if st := rep.Statements[i]; st.Rating != w.rating || !hasRule(st, w.rule) || st.Score != map[score.Rating]int{score.Medium: 4, score.High: 8}[w.rating] {
			t.Errorf("%s: %s %+v", st.SQL, st.Rating, st.Findings)
		}
	}
	if !strings.Contains(rep.Statements[2].Findings[0].Message, "2.0 GB") && !strings.Contains(rep.Statements[2].Findings[0].Message, "GiB") {
		t.Errorf("reindex size %+v", rep.Statements[2].Findings)
	}
}

// TestMigratePredictions sends each request to real clusters and checks
// dbscope's "this fails" against what the cluster refused.
func TestMigratePredictions(t *testing.T) {
	cases := []string{
		"PUT /logs/_mapping\n{\"properties\": {\"status\": {\"type\": \"keyword\"}}}",
		"PUT /logs/_mapping\n{\"properties\": {\"status\": {\"type\": \"long\"}}}",
		"PUT /logs/_mapping\n{\"properties\": {\"host\": {\"type\": \"keyword\"}}}",
		"PUT /logs/_mapping\n{\"properties\": {\"msg\": {\"type\": \"text\", \"fields\": {\"raw\": {\"type\": \"keyword\"}}}}}",
		"PUT /logs/_mapping\n{\"properties\": {\"a1\": {\"type\": \"keyword\"}, \"a2\": {\"type\": \"keyword\"}, \"a3\": {\"type\": \"keyword\"}, \"a4\": {\"type\": \"keyword\"}}}",
		"PUT /logs/_settings\n{\"index\": {\"number_of_shards\": 4}}",
		"PUT /logs/_settings\n{\"index\": {\"number_of_replicas\": 1}}",
		"PUT /logs/_settings\n{\"index\": {\"refresh_interval\": \"30s\"}}",
		"PUT /logs/_settings\n{\"analysis\": {\"analyzer\": {\"a\": {\"type\": \"standard\"}}}}",
		"PUT /logs\n{}",
		"PUT /logs-new\n{}",
		"POST /logs/_split/logs-split\n{\"settings\": {\"index.number_of_shards\": 4}}",
		"PUT /logs/_settings\n{\"index.blocks.write\": true}\nPOST /logs/_split/logs-split\n{\"settings\": {\"index.number_of_shards\": 4}}",
		"POST /logs/_close",
		"POST _reindex\n{\"source\": {\"index\": \"logs\"}, \"dest\": {\"index\": \"logs-copy\"}}",
	}
	for _, name := range []string{"es9", "es8", "os3", "os2"} {
		t.Run(name, func(t *testing.T) {
			p := mytest.Elastic(t, name)
			c, err := Connect(p, mytest.ElasticPassword)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			for _, tc := range cases {
				for _, ix := range []string{"logs", "logs-new", "logs-split", "logs-copy"} {
					c.do(ctx, "DELETE", "/"+ix+"?ignore_unavailable=true", nil, nil)
				}
				err := c.do(ctx, "PUT", "/logs", map[string]any{
					"settings": map[string]any{"number_of_shards": 2, "number_of_replicas": 0, "mapping.total_fields.limit": 5},
					"mappings": map[string]any{"properties": map[string]any{"status": map[string]any{"type": "long"}, "msg": map[string]any{"type": "text"}}}}, nil)
				if err != nil {
					t.Fatal(err)
				}
				c.do(ctx, "PUT", "/logs/_doc/1?refresh=true", map[string]any{"status": 200, "msg": "hello"}, nil)
				base, err := ReadBase(ctx, c)
				if err != nil {
					t.Fatal(err)
				}
				rep := Analyze(base, []ingest.Input{{Name: "m.http", SQL: tc}})
				reqs := splitRequests(tc)
				for i, r := range reqs {
					target := r.Path
					if q := r.Query.Encode(); q != "" {
						target += "?" + q
					}
					var body any
					if r.Body != nil {
						body = r.Body
					}
					err = c.do(ctx, r.Method, target, body, nil)
					if i < len(reqs)-1 && err != nil {
						t.Fatalf("%s: setup request failed: %v", tc, err)
					}
				}
				st := rep.Statements[len(rep.Statements)-1]
				if predicted := hasRule(st, "DBS-ES-FAILS"); predicted != (err != nil) {
					t.Errorf("%s: dbscope predicted fails=%v; the cluster said %v (%+v)", tc, predicted, err, st.Findings)
				}
			}
		})
	}
}

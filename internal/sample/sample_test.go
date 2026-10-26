package sample

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/dbscope/dbscope/internal/infer"
	"github.com/dbscope/dbscope/internal/introspect"
	"github.com/dbscope/dbscope/internal/pgtest"
	"github.com/dbscope/dbscope/internal/schema"
)

var update = flag.Bool("update", false, "regenerate model.json from sample.sql")

func TestMain(m *testing.M) { pgtest.Main(m) }

func encode(t *testing.T, m *schema.Model) []byte {
	t.Helper()
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

// TestModelJSON loads sample.sql into PostgreSQL 17 and checks that
// introspecting it gives exactly the embedded model.json (or rewrites the
// file with -update).
func TestModelJSON(t *testing.T) {
	srv := pgtest.Servers(t, "17")[0]
	if srv.Major != 17 {
		t.Skipf("model.json is generated on PostgreSQL 17; DBSCOPE_TEST_DSN is %d", srv.Major)
	}
	d := srv.NewDB(t)
	d.Exec(t, SQL)
	m, err := introspect.Introspect(t.Context(), d.Admin, Schemas, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	Normalize(m)
	got := encode(t, m)
	if *update {
		if err := os.WriteFile("model.json", got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote model.json (%d bytes, %d relations)", len(got), len(m.Relations))
		return
	}
	if !bytes.Equal(got, modelJSON) {
		os.WriteFile(t.TempDir()+"/model.json", got, 0o644)
		t.Fatal("model.json is stale; run: go test ./internal/sample -run TestModelJSON -update")
	}
}

func TestEmbeddedModel(t *testing.T) {
	m, err := Model()
	if err != nil {
		t.Fatal(err)
	}
	if m.ServerVersion != 170000 || !slices.Equal(m.Schemas, Schemas) || !m.IntrospectedAt.Equal(IntrospectedAt) {
		t.Fatalf("header: %d %v %v", m.ServerVersion, m.Schemas, m.IntrospectedAt)
	}
	kinds := map[schema.RelKind]int{}
	composite := false
	for _, r := range m.Relations {
		kinds[r.Kind]++
		if r.Stub {
			t.Errorf("unexpected stub %s", r.QName())
		}
		for _, fk := range r.ForeignKeys {
			composite = composite || len(fk.Columns) > 1
		}
	}
	if tables := kinds[schema.KindTable] + kinds[schema.KindPartitionedTable]; tables < 38 || tables > 45 ||
		kinds[schema.KindView] != 1 || kinds[schema.KindMatView] != 1 || kinds[schema.KindPartitionedTable] != 1 {
		t.Fatalf("relation kinds %v", kinds)
	}
	if !composite || len(m.Enums) == 0 {
		t.Fatal("sample needs a composite FK and an enum")
	}
	audit := m.Relation(schema.QualifiedName{Schema: "app", Name: "audit_log"})
	if audit == nil || len(audit.Partitions) != 3 || audit.RowEstimate == nil || *audit.RowEstimate != 1203311+1350972+1129045 {
		t.Fatalf("audit_log %+v", audit)
	}
	if o := m.Relation(schema.QualifiedName{Schema: "shop", Name: "orders"}); o == nil || o.RowEstimate == nil || *o.RowEstimate != 1204331 || o.Comment == "" {
		t.Fatalf("orders %+v", o)
	}

	var keys []string
	for _, c := range infer.Infer(m) {
		keys = append(keys, c.FK.Name)
	}
	want := []string{
		"app.audit_log(user_id)->app.users(id)",
		"app.events(user_id)->app.users(id)",
		"billing.invoice_lines(subscription_id)->billing.subscriptions(id)",
		"shop.refunds(payment_id)->shop.payments(id)",
	}
	if !slices.Equal(keys, want) {
		t.Fatalf("inferred %v, want %v", keys, want)
	}
}

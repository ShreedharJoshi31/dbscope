package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/share"
)

func TestSharedStateExportPreviewImport(t *testing.T) {
	e := keyringEnv(t)
	// Port 1 refuses at once: previews work without the database, unchecked.
	w := e.do("POST", "/api/profiles", `{"name":"Shop db","host":"127.0.0.1","port":1,"database":"app","user":"reader","env":"local"}`)
	expect(t, w, 201, "")
	var p struct{ ID string }
	json.Unmarshal(w.Body.Bytes(), &p)
	base := "/api/profiles/" + p.ID
	expect(t, e.do("PUT", base+"/layout", `{"key":"public","positions":{"public.users":{"x":10.4,"y":20}},"collapsed":["public.audit"]}`), 204, "")
	expect(t, e.do("PUT", base+"/glossary", `[{"term":"mrr","definition":"monthly recurring revenue","sql":""}]`), 204, "")
	expect(t, e.do("PUT", base+"/inferred-fks", `{"key":"public.a(b_id)->public.b(id)","status":"accepted"}`), 204, "")

	w = e.do("GET", base+"/shared", "")
	expect(t, w, 200, "")
	if cd := w.Header().Get("Content-Disposition"); cd != `attachment; filename="Shop-db.dbscope.json"` {
		t.Fatalf("Content-Disposition %q", cd)
	}
	exported := w.Body.String()
	f, err := share.Parse([]byte(exported))
	if err != nil {
		t.Fatal(err)
	}
	if f.Layouts["public"].Positions["public.users"] != [2]int{10, 20} || f.ForeignKeys["public.a(b_id)->public.b(id)"] != "accepted" || f.Glossary["mrr"].Definition != "monthly recurring revenue" {
		t.Fatalf("exported %s", exported)
	}

	teammate := strings.Replace(exported, `"monthly recurring revenue"`, `"MRR, excluding refunds"`, 1)
	w = e.do("POST", base+"/shared/preview", teammate)
	expect(t, w, 200, "")
	var pv share.Preview
	json.Unmarshal(w.Body.Bytes(), &pv)
	if pv.Mode != share.Merge || pv.SchemaChecked || pv.Glossary.Counts[share.Changed] != 1 || pv.Tables.Counts[share.Unchanged] != 2 {
		t.Fatalf("preview %s", w.Body.String())
	}
	if got := e.do("GET", base+"/shared", "").Body.String(); got != exported {
		t.Fatal("a preview changed the state")
	}

	w = e.do("POST", base+"/shared/import?mode=replace", `{"kind":"dbscope-shared-state","formatVersion":1,"glossary":{"arr":{"definition":"annual"}}}`)
	expect(t, w, 200, "")
	json.Unmarshal(w.Body.Bytes(), &pv)
	if pv.Tables.Counts[share.Removed] != 2 || pv.Glossary.Counts[share.Added] != 1 || pv.ForeignKeys.Counts[share.Removed] != 1 {
		t.Fatalf("import %s", w.Body.String())
	}
	if g := e.do("GET", base+"/glossary", "").Body.String(); !strings.Contains(g, `"arr"`) || strings.Contains(g, "mrr") {
		t.Fatalf("glossary after replace %s", g)
	}
	expect(t, e.do("GET", base+"/layout?key=public", ""), 404, "not_found")

	// Undo: importing the earlier export with replace restores it exactly.
	expect(t, e.do("POST", base+"/shared/import?mode=replace", exported), 200, "")
	if got := e.do("GET", base+"/shared", "").Body.String(); got != exported {
		t.Fatalf("undo did not restore the state:\n%s", got)
	}

	// A merge that would leave more terms than a glossary holds is refused, and nothing changes.
	var terms []string
	for i := range 500 {
		terms = append(terms, fmt.Sprintf(`{"term":"t%d","definition":"","sql":""}`, i))
	}
	expect(t, e.do("PUT", base+"/glossary", "["+strings.Join(terms, ",")+"]"), 204, "")
	w = e.do("POST", base+"/shared/import", `{"kind":"dbscope-shared-state","formatVersion":1,"glossary":{"one more":{"definition":""}}}`)
	expect(t, w, 400, "validation")
	if !strings.Contains(w.Body.String(), "501 glossary terms") {
		t.Fatalf("limit message %s", w.Body.String())
	}
	if g := e.do("GET", base+"/glossary", "").Body.String(); strings.Count(g, `"term"`) != 500 || strings.Contains(g, "one more") {
		t.Fatal("a refused import changed the glossary")
	}
	expect(t, e.do("POST", base+"/shared/import", strings.Repeat(" ", share.MaxBytes+1)), 413, "validation")

	expect(t, e.do("POST", base+"/shared/import", `{}`), 400, "validation")
	expect(t, e.do("POST", base+"/shared/preview?mode=bogus", exported), 400, "validation")
	expect(t, e.do("GET", "/api/profiles/missing/shared", ""), 404, "not_found")
}

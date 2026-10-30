package ingest

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func forwardNames(p Paired) []string {
	var out []string
	for _, in := range p.Forward {
		out = append(out, in.Name)
	}
	return out
}

func TestPairFiles(t *testing.T) {
	p := Pair([]Input{
		{Name: "db/000001_a.up.sql", SQL: "up1"}, {Name: "db/1_a.down.sql", SQL: "down1"},
		{Name: "db/2_b.up.sql", SQL: "up2"}, {Name: "db/3_c.down.sql", SQL: "orphan"},
		{Name: "other/1_a.down.sql", SQL: "other dir"},
	})
	if got := forwardNames(p); !slices.Equal(got, []string{"db/000001_a.up.sql", "db/2_b.up.sql"}) {
		t.Fatalf("forward %v", got)
	}
	if d := p.Downs["db/000001_a.up.sql"]; d == nil || d.Missing || d.Name != "db/1_a.down.sql" || d.Text != "down1" || d.Tool != ToolGolangMigrate {
		t.Errorf("pair %+v", d)
	}
	if d := p.Downs["db/2_b.up.sql"]; d == nil || !d.Missing {
		t.Errorf("missing %+v", d)
	}
	if len(p.Warnings) != 2 || !strings.Contains(p.Warnings[0], "db/3_c.down.sql has no matching up") || !strings.Contains(p.Warnings[1], "other/1_a.down.sql") {
		t.Errorf("warnings %v", p.Warnings)
	}

	// Flyway compares versions numerically, so U1.0 undoes V1.
	p = Pair([]Input{{Name: "V1__a.sql"}, {Name: "U1.0__a.sql", SQL: "undo"}, {Name: "V1.1__b.sql"}, {Name: "R__views.sql"}})
	if d := p.Downs["V1__a.sql"]; d == nil || d.Missing || d.Text != "undo" {
		t.Errorf("flyway %+v", d)
	}
	if d := p.Downs["V1.1__b.sql"]; d == nil || !d.Missing {
		t.Errorf("flyway missing %+v", d)
	}
	if p.Downs["R__views.sql"] != nil {
		t.Error("repeatable migrations have no undo")
	}
	if p = Pair([]Input{{Name: "V1__a.sql"}}); len(p.Downs) != 0 {
		t.Errorf("flyway without undo files %+v", p.Downs)
	}
	if p = Pair([]Input{{Name: "prisma/migrations/1_x/migration.sql"}, {Name: "001_plain.sql", SQL: "SELECT 1"}}); len(p.Downs) != 0 {
		t.Errorf("no rollbacks %+v", p.Downs)
	}
}

func TestPairSections(t *testing.T) {
	goose := "-- +goose Up\nCREATE TABLE a (id int);\n\n-- +goose Down\nDROP TABLE a;\n"
	p := Pair([]Input{{Name: "1_a.sql", SQL: goose}})
	f, d := p.Forward[0].SQL, p.Downs["1_a.sql"]
	if strings.Contains(f, "DROP") || strings.Count(f, "\n") != strings.Count(goose, "\n") || len(f) != len(goose) {
		t.Errorf("forward %q", f)
	}
	if d == nil || d.Missing || d.Tool != ToolGoose || strings.Contains(d.Text, "CREATE") || !strings.HasPrefix(strings.Split(d.Text, "\n")[4], "DROP TABLE a;") {
		t.Errorf("down %+v", d)
	}
	p = Pair([]Input{{Name: "2_b.sql", SQL: "-- migrate:up transaction:false\nCREATE INDEX CONCURRENTLY i ON a (id);\n"}})
	if d := p.Downs["2_b.sql"]; d == nil || !d.Missing || d.Tool != ToolDbmate {
		t.Errorf("dbmate %+v", d)
	}
}

func TestPairMigrateMongo(t *testing.T) {
	cases := []struct {
		name, src, up, down string
	}{
		{"methods", "module.exports = {\n  async up(db, client) {\n    await db.collection('x').createIndex({a: 1}); // down(\n  },\n  async down(db, client) {\n    await db.collection('x').dropIndex('a_1');\n  }\n};\n",
			"createIndex", "dropIndex('a_1')"},
		{"properties", "module.exports = {\n  up: async (db) => { await db.collection('down').insertOne({s: '}'}); },\n  down: (db) => db.collection('down').drop(),\n};\n",
			"insertOne", ".drop()"},
		{"exports", "exports.up = function (db) { return db.collection('y').createIndex({b: 1}); };\nexports.down = function (db) { return db.collection('y').dropIndex('b_1'); };\n",
			"createIndex", "dropIndex"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Pair([]Input{{Name: "m.js", SQL: c.src}})
			d := p.Downs["m.js"]
			if d == nil || d.Missing || d.Tool != ToolMigrateMongo {
				t.Fatalf("down %+v", d)
			}
			f := p.Forward[0].SQL
			if !strings.Contains(f, c.up) || strings.Contains(f, c.down) || len(f) != len(c.src) {
				t.Errorf("forward %q", f)
			}
			if !strings.Contains(d.Text, c.down) || strings.Contains(d.Text, c.up) {
				t.Errorf("down text %q", d.Text)
			}
		})
	}
	if p := Pair([]Input{{Name: "m.js", SQL: "module.exports = { async up(db) {} };"}}); p.Downs["m.js"] == nil || !p.Downs["m.js"].Missing {
		t.Error("up without down")
	}
	if p := Pair([]Input{{Name: "s.js", SQL: "db.users.createIndex({a: 1}); db.backup.drop();"}}); len(p.Downs) != 0 {
		t.Error("a mongosh script has no rollback")
	}
}

func TestReadPathsBringsDownSibling(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"0001_a.up.sql": "a", "1_a.down.sql": "b", "2_b.up.sql": "c", "V3__c.sql": "d", "U3__c.sql": "e"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	in, err := ReadPaths([]string{filepath.Join(dir, "0001_a.up.sql"), filepath.Join(dir, "V3__c.sql")}, dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range in {
		got = append(got, f.Name)
	}
	if !slices.Equal(got, []string{"0001_a.up.sql", "1_a.down.sql", "V3__c.sql", "U3__c.sql"}) {
		t.Fatalf("%v", got)
	}
	in, err = ReadPaths([]string{filepath.Join(dir, "0001_a.up.sql"), filepath.Join(dir, "1_a.down.sql")}, dir)
	if err != nil || len(in) != 2 {
		t.Fatalf("named twice: %v %v", in, err)
	}
}

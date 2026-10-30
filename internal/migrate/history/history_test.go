package history

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/dbscope/dbscope/internal/migrate/ingest"
)

// The expected values were computed separately with Python's zlib.crc32.
func TestChecksums(t *testing.T) {
	script := "CREATE TABLE a (id int);\n-- note\nINSERT INTO a VALUES (1);\n"
	for _, s := range []string{script, strings.ReplaceAll(script, "\n", "\r\n"), "\ufeff" + script, strings.TrimSuffix(script, "\n")} {
		if got := FlywayChecksum(s); got != -1696688092 {
			t.Errorf("FlywayChecksum(%q) = %d", s, got)
		}
	}
	if FlywayChecksum(script+"x") == -1696688092 {
		t.Error("an edit kept the Flyway checksum")
	}
	cypher := "CREATE CONSTRAINT a IF NOT EXISTS FOR (n:A) REQUIRE n.id IS UNIQUE;\nCREATE INDEX b IF NOT EXISTS FOR (n:B) ON (n.x);\n"
	if got := Neo4jChecksum(cypher); got != "922512040" {
		t.Errorf("Neo4jChecksum = %s", got)
	}

	sum := sha256.Sum256([]byte("CREATE TABLE a ();\n"))
	full := hex.EncodeToString(sum[:])
	var legacy strings.Builder
	for _, b := range sum {
		fmt.Fprintf(&legacy, "%x", b)
	}
	for name, c := range map[string]struct {
		script, sum string
		want        bool
	}{
		"same":            {"CREATE TABLE a ();\n", full, true},
		"crlf checkout":   {"CREATE TABLE a ();\r\n", full, true},
		"unpadded legacy": {"CREATE TABLE a ();\n", legacy.String(), true},
		"edited":          {"CREATE TABLE b ();\n", full, false},
		"newline added":   {"CREATE TABLE a ();\n\n", full, false},
	} {
		if got := PrismaChecksumMatches(c.script, c.sum); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestVersion(t *testing.T) {
	for in, want := range map[string]string{"1": "1", "1_1": "1.1", "01.10": "1.10", "1.0": "1", "0001": "1", "0": "0", "2.0.0": "2"} {
		if got := version(in); got != want {
			t.Errorf("version(%q) = %q, want %q", in, got, want)
		}
	}
	if cmpVersion("1.10", "1.9") <= 0 || cmpVersion("1", "1") != 0 || cmpVersion("0.9", "1") >= 0 {
		t.Error("cmpVersion")
	}
}

func TestKeepAndUnmatched(t *testing.T) {
	files := []ingest.Input{{Name: "m/V1__a.sql"}, {Name: "m/V1__a.sql.conf"}, {Name: "m/V2__b.sql"}, {Name: "m/V3__c.sql"}}
	r := classify("flyway", "flyway_schema_history", files, func(f ingest.Input) state {
		switch base(f.Name) {
		case "V1__a.sql":
			return applied
		case "V2__b.sql":
			return changed
		case "V3__c.sql":
			return pending
		}
		return other
	})
	if r.Pending == nil || !slices.Equal(r.Pending.Applied, []string{"m/V1__a.sql"}) || !slices.Equal(r.Pending.Changed, []string{"m/V2__b.sql"}) {
		t.Fatalf("%+v", r.Pending)
	}
	kept := r.Keep(files)
	if len(kept) != 3 || slices.ContainsFunc(kept, func(f ingest.Input) bool { return f.Name == "m/V1__a.sql" }) {
		t.Fatalf("kept %v", kept)
	}

	r = classify("flyway", "flyway_schema_history", files, func(ingest.Input) state { return other })
	if r.Pending != nil || len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "no file is named the way flyway names migrations") {
		t.Fatalf("%+v", r)
	}
	if got := r.Keep(files); len(got) != len(files) {
		t.Fatal("no history must keep every file")
	}
}

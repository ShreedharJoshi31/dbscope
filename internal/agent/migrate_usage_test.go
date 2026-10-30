package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dbscope/dbscope/internal/migrate"
)

func TestProfileMigrationAnalyzeUsages(t *testing.T) {
	e, _ := fakeEnv(t)
	root := t.TempDir()
	files := map[string]string{
		"app/models/post.rb":         "class Post < ApplicationRecord\n  belongs_to :user, foreign_key: :user_id\nend\n",
		"app/models/comment.rb":      "class Comment\n  attr :user_id\nend\n", // another table's user_id
		"db/migrate/1_init.up.sql":   "ALTER TABLE public.posts DROP COLUMN user_id;",
		"node_modules/x/posts.js":    "posts.user_id\n",
		"web/src/posts.tsx":          "export const byUser = (posts: Post[], id: string) => posts.filter(p => p.user_id === id);\n",
		"scripts/old/1_init.up.sql":  "-- a copy of the analysed file is skipped by name: posts user_id\n",
		"tools/report.py":            "SELECT title FROM posts\n", // names the table, not the column
		"tools/user_id_generator.go": "package tools // user_id_generator\n",
	}
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	body, _ := json.Marshal(map[string]any{"name": "app", "host": "db.example", "database": "app", "user": "reader", "env": "staging",
		"projectDir": root, "migrationsDir": filepath.Join(root, "db/migrate")})
	p := e.createProfile(string(body))
	if p.ProjectDir == "" {
		t.Fatalf("profile %+v", p)
	}
	w := e.do("POST", "/api/profiles/"+p.ID+"/migrations/analyze",
		analyzeBodyJSON(t, map[string]string{"1_init.up.sql": "ALTER TABLE public.posts DROP COLUMN user_id;"}, nil))
	expect(t, w, 200, "")
	rep := decodeBody[migrate.Report](t, w)
	want := []migrate.Usage{
		{Name: "public.posts.user_id", File: "app/models/post.rb", Line: 2, Text: "belongs_to :user, foreign_key: :user_id"},
		{Name: "public.posts.user_id", File: "web/src/posts.tsx", Line: 1, Text: "export const byUser = (posts: Post[], id: string) => posts.filter(p => p.user_id === id);"},
	}
	if len(rep.Usages) != len(want) || rep.Usages[0] != want[0] || rep.Usages[1] != want[1] {
		t.Fatalf("usages %+v", rep.Usages)
	}
	f := rep.Statements[0].Findings[len(rep.Statements[0].Findings)-1]
	if f.RuleID != "DBS-STILL-USED" || f.Rating != "high" ||
		f.Message != "public.posts.user_id is still named in 2 places in the project, e.g. app/models/post.rb:2" {
		t.Fatalf("finding %+v", f)
	}

	// Without a project folder the report has no usages and no such finding.
	q := e.createProfile(validBody)
	rep = decodeBody[migrate.Report](t, e.do("POST", "/api/profiles/"+q.ID+"/migrations/analyze",
		analyzeBodyJSON(t, map[string]string{"1_init.up.sql": "ALTER TABLE public.posts DROP COLUMN user_id;"}, nil)))
	for _, f := range rep.Statements[0].Findings {
		if f.RuleID == "DBS-STILL-USED" || rep.Usages != nil {
			t.Fatalf("searched without a project folder: %+v", rep)
		}
	}
}

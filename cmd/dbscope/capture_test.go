package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestCaptureUsage(t *testing.T) {
	out := t.TempDir()
	os.WriteFile(out+"/old.sql", []byte("SELECT 1;"), 0o644)
	for _, c := range []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"capture", "--shadow", "postgres://x/y"}, exitUsage, "give the migrate command after --"},
		{[]string{"capture", "--", "rails", "db:migrate"}, exitUsage, "--shadow is required"},
		{[]string{"capture", "--shadow", "postgres://x/y", "stray", "--", "true"}, exitUsage, "unexpected argument stray"},
		// --out is checked before the shadow is touched or anything runs.
		{[]string{"capture", "--shadow", "postgres://127.0.0.1:1/none", "--out", out, "--", "true"}, exitUnavailable, "is not empty"},
	} {
		var stdout, stderr bytes.Buffer
		if code := migrateCmd(c.args, &stdout, &stderr); code != c.code || !strings.Contains(stderr.String(), c.msg) {
			t.Errorf("%v: code %d, stderr %q", c.args, code, stderr.String())
		}
	}
}

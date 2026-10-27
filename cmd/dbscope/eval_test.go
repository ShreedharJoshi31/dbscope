package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvalUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"sql"}, {"nl"}, {"nl", "--dataset", "x"}, {"nl", "--dsn", "postgres://h/db"},
		{"nl", "--dataset", "x", "--dsn", "y", "--limit", "-1"}, {"nl", "--dataset", "x", "--dsn", "y", "extra"}} {
		var out, errb bytes.Buffer
		if code := evalCmd(args, &out, &errb); code != 2 || !strings.Contains(errb.String(), "usage:") {
			t.Errorf("%v: exit %d, stderr %q", args, code, errb.String())
		}
	}
}

func TestEvalErrorsDoNotEchoDSN(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "questions.yaml"), []byte("name: x\nquestions: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	const dsn = "postgres://u:hunter2-secret@[bad"
	if code := evalCmd([]string{"nl", "--dataset", dir, "--dsn", dsn, "--gold-only"}, &out, &errb); code != 1 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if strings.Contains(errb.String(), "hunter2") || !strings.Contains(errb.String(), "--dsn does not parse") {
		t.Fatalf("stderr %q", errb.String())
	}
}

// A local Ollama needs no key, so a locked passphrase vault must not block it;
// Anthropic still needs one.
func TestEvalLockedVaultOnlyBlocksAnthropic(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	t.Setenv("DBSCOPE_SECRETS", "file")
	p, err := evalProvider(evalOptions{provider: "openai_compat", baseURL: "http://127.0.0.1:11434/v1/", model: "m"})
	if err != nil || p.Model() != "m" {
		t.Fatalf("openai_compat with a locked vault: %v %v", p, err)
	}
	if _, err := evalProvider(evalOptions{provider: "anthropic"}); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("anthropic with a locked vault: %v", err)
	}
}

func TestEvalAPIKeyEnvTakesAName(t *testing.T) {
	if _, err := evalProvider(evalOptions{provider: "anthropic", keyEnv: "sk-ant=abc"}); err == nil || !strings.Contains(err.Error(), "variable name") {
		t.Fatalf("value accepted: %v", err)
	}
	t.Setenv("DBSCOPE_TEST_EMPTY_KEY", "")
	if _, err := evalProvider(evalOptions{provider: "anthropic", keyEnv: "DBSCOPE_TEST_EMPTY_KEY"}); err == nil {
		t.Fatal("empty key accepted")
	}
	t.Setenv("DBSCOPE_TEST_KEY", "k")
	if _, err := evalProvider(evalOptions{provider: "anthropic", keyEnv: "DBSCOPE_TEST_KEY", baseURL: "http://evil/"}); err == nil {
		t.Fatal("anthropic key sent to a custom base URL")
	}
	p, err := evalProvider(evalOptions{provider: "anthropic", keyEnv: "DBSCOPE_TEST_KEY"})
	if err != nil || p.Model() != "claude-opus-5-5" {
		t.Fatalf("%v %v", p, err)
	}
	if _, err := evalProvider(evalOptions{provider: "openai_compat", keyEnv: "DBSCOPE_TEST_KEY"}); err == nil {
		t.Fatal("openai_compat without base URL accepted")
	}
}

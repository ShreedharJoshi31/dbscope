package logx

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestPasswordNeverLogged(t *testing.T) {
	const pw = "hunter2-very-secret"
	var buf bytes.Buffer
	log := New(&buf, slog.LevelDebug)

	log.Info("connect", "password", pw)
	log.Info("connect", "Password", pw, "DSN", "postgres://u:"+pw+"@h/db")
	log.Info("connect", slog.Group("conn", slog.String("PASSWORD", pw), slog.String("api_key", pw)))
	log.Info("connect", "value", Secret(pw), "token", pw, "Authorization", "Bearer "+pw, "secret", pw)

	out := buf.String()
	if strings.Contains(out, pw) {
		t.Fatalf("secret leaked into log output:\n%s", out)
	}
	if got := strings.Count(out, redacted); got != 9 {
		t.Fatalf("want 9 redactions, got %d:\n%s", got, out)
	}
}

func TestOrdinaryAttrsKept(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, slog.LevelInfo).Info("started", "port", 4555, "host", "127.0.0.1")
	if !strings.Contains(buf.String(), "port=4555") || !strings.Contains(buf.String(), "host=127.0.0.1") {
		t.Fatalf("unexpected output: %s", buf.String())
	}
}

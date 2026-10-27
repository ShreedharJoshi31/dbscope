// Package logx configures slog so credentials cannot reach the log output.
package logx

import (
	"io"
	"log/slog"
	"strings"
)

const redacted = "[REDACTED]"

// Secret wraps a sensitive string so it logs as [REDACTED] under any key.
type Secret string

func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

var sensitiveKeys = []string{"password", "dsn", "token", "authorization", "api_key", "secret",
	// LLM API keys: the JSON field, the HTTP header and the vault names.
	"apiKey", "x-api-key", "llm:anthropic", "llm:openai_compat"}

// ReplaceAttr redacts attributes whose key names a credential, as a backstop
// for values that were not wrapped in Secret.
func ReplaceAttr(_ []string, a slog.Attr) slog.Attr {
	for _, k := range sensitiveKeys {
		if strings.EqualFold(a.Key, k) {
			return slog.String(a.Key, redacted)
		}
	}
	return a
}

func New(w io.Writer, level slog.Leveler) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level, ReplaceAttr: ReplaceAttr}))
}

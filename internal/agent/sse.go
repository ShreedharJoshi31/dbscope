package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// WriteEvent sends one Server-Sent Events frame with data JSON-encoded and
// flushes it so the browser sees it immediately. json.Marshal never emits a
// raw newline, so the payload always fits on a single data: line.
func WriteEvent(w http.ResponseWriter, event string, data any) error {
	if strings.ContainsAny(event, "\r\n") {
		return errors.New("sse: event name must not contain a newline")
	}
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	h := w.Header()
	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return err
	}
	return http.NewResponseController(w).Flush()
}

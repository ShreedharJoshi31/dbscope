package agent

import (
	"net/http/httptest"
	"testing"
)

func TestWriteEvent(t *testing.T) {
	w := httptest.NewRecorder()
	if err := WriteEvent(w, "delta", map[string]string{"text": "line1\nline2"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteEvent(w, "done", true); err != nil {
		t.Fatal(err)
	}
	want := "event: delta\ndata: {\"text\":\"line1\\nline2\"}\n\nevent: done\ndata: true\n\n"
	if got := w.Body.String(); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if !w.Flushed {
		t.Fatal("not flushed")
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type %q", ct)
	}
	if err := WriteEvent(w, "bad\nname", nil); err == nil {
		t.Fatal("newline in event name accepted")
	}
}

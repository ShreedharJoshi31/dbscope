package redis

import "testing"

func TestPatternOf(t *testing.T) {
	for in, want := range map[string]string{
		"user:1042:cart": "user:*:cart",
		"session:3f2b8c1e-9a4d-4e2b-8f1a-0c9d7e6b5a43": "session:*",
		"cache/v2/product/77/page":                     "cache/v2/product/*/page",
		"orders:2026:10":                               "orders:*:*",
		"config":                                       "config",
		"token:Zm9vYmFyYmF6cXV4cXV1eHh5eg":             "token:*",
		"queue:emails":                                 "queue:emails",
		"h:deadbeefcafe:x":                             "h:*:x",
	} {
		if got := PatternOf(in); got != want {
			t.Errorf("PatternOf(%q) = %q, want %q", in, got, want)
		}
	}
}

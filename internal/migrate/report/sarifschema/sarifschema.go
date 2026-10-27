// Package sarifschema validates a document against the official SARIF 2.1.0
// JSON schema (vendored in testdata, see its notice). It implements the
// JSON Schema draft-04 keywords that schema uses: $ref, type, properties,
// additionalProperties, required, items, minItems, uniqueItems, minimum,
// maximum, pattern, enum, anyOf, oneOf, and the uri / uri-reference /
// date-time formats. It exists for tests; no command imports it.
package sarifschema

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
)

//go:embed testdata/sarif-schema-2.1.0.json
var schemaJSON []byte

// Validate checks doc (raw JSON) and returns every violation found.
func Validate(doc []byte) []string {
	var schema, v any
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		return []string{"schema: " + err.Error()}
	}
	dec := json.NewDecoder(strings.NewReader(string(doc)))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return []string{"document: " + err.Error()}
	}
	c := &checker{root: schema.(map[string]any), patterns: map[string]*regexp.Regexp{}}
	c.check(schema.(map[string]any), v, "$")
	sort.Strings(c.errs)
	return c.errs
}

type checker struct {
	root     map[string]any
	errs     []string
	patterns map[string]*regexp.Regexp
}

func (c *checker) errorf(path, format string, args ...any) {
	c.errs = append(c.errs, path+": "+fmt.Sprintf(format, args...))
}

func (c *checker) resolve(s map[string]any) map[string]any {
	for {
		ref, ok := s["$ref"].(string)
		if !ok {
			return s
		}
		name, found := strings.CutPrefix(ref, "#/definitions/")
		if !found {
			c.errorf("schema", "unsupported $ref %s", ref)
			return map[string]any{}
		}
		s = c.root["definitions"].(map[string]any)[name].(map[string]any)
	}
}

// valid reports whether v matches s without recording errors.
func (c *checker) valid(s map[string]any, v any, path string) bool {
	sub := &checker{root: c.root, patterns: c.patterns}
	sub.check(s, v, path)
	return len(sub.errs) == 0
}

func typeOf(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case json.Number:
		if f, err := x.Float64(); err == nil && f == math.Trunc(f) && !strings.ContainsAny(x.String(), ".eE") {
			return "integer"
		}
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "unknown"
}

func typeMatches(want string, v any) bool {
	got := typeOf(v)
	return got == want || (want == "number" && got == "integer")
}

func (c *checker) check(s map[string]any, v any, path string) {
	s = c.resolve(s)
	if t, ok := s["type"]; ok {
		switch t := t.(type) {
		case string:
			if !typeMatches(t, v) {
				c.errorf(path, "type %s, want %s", typeOf(v), t)
				return
			}
		case []any:
			match := false
			for _, x := range t {
				match = match || typeMatches(x.(string), v)
			}
			if !match {
				c.errorf(path, "type %s, want one of %v", typeOf(v), t)
				return
			}
		}
	}
	if e, ok := s["enum"].([]any); ok {
		found := false
		for _, x := range e {
			found = found || reflect.DeepEqual(norm(x), norm(v))
		}
		if !found {
			c.errorf(path, "value %v is not in the enum", v)
		}
	}
	if alts, ok := s["anyOf"].([]any); ok {
		match := false
		for _, x := range alts {
			match = match || c.valid(x.(map[string]any), v, path)
		}
		if !match {
			c.errorf(path, "matches none of anyOf")
		}
	}
	if one, ok := s["oneOf"].([]any); ok {
		n := 0
		for _, x := range one {
			if c.valid(x.(map[string]any), v, path) {
				n++
			}
		}
		if n != 1 {
			c.errorf(path, "matches %d of oneOf, want exactly 1", n)
		}
	}
	switch x := v.(type) {
	case string:
		c.checkString(s, x, path)
	case json.Number:
		f, _ := x.Float64()
		if m, ok := s["minimum"].(float64); ok && f < m {
			c.errorf(path, "%v is below the minimum %v", f, m)
		}
		if m, ok := s["maximum"].(float64); ok && f > m {
			c.errorf(path, "%v is above the maximum %v", f, m)
		}
	case []any:
		if m, ok := s["minItems"].(float64); ok && float64(len(x)) < m {
			c.errorf(path, "%d items, want at least %v", len(x), m)
		}
		if u, ok := s["uniqueItems"].(bool); ok && u {
			for i := range x {
				for j := i + 1; j < len(x); j++ {
					if reflect.DeepEqual(x[i], x[j]) {
						c.errorf(path, "items %d and %d are equal", i, j)
					}
				}
			}
		}
		if items, ok := s["items"].(map[string]any); ok {
			for i, it := range x {
				c.check(items, it, fmt.Sprintf("%s[%d]", path, i))
			}
		}
	case map[string]any:
		props, _ := s["properties"].(map[string]any)
		if req, ok := s["required"].([]any); ok {
			for _, r := range req {
				if _, present := x[r.(string)]; !present {
					c.errorf(path, "missing required property %q", r)
				}
			}
		}
		for k, val := range x {
			if ps, ok := props[k].(map[string]any); ok {
				c.check(ps, val, path+"."+k)
				continue
			}
			switch ap := s["additionalProperties"].(type) {
			case bool:
				if !ap {
					c.errorf(path, "property %q is not allowed", k)
				}
			case map[string]any:
				c.check(ap, val, path+"."+k)
			}
		}
	}
}

func norm(v any) any {
	if n, ok := v.(json.Number); ok {
		f, _ := n.Float64()
		return f
	}
	return v
}

func (c *checker) checkString(s map[string]any, x, path string) {
	if p, ok := s["pattern"].(string); ok {
		re := c.patterns[p]
		if re == nil {
			var err error
			if re, err = regexp.Compile(p); err != nil {
				c.errorf(path, "schema pattern %q does not compile: %v", p, err)
				return
			}
			c.patterns[p] = re
		}
		if !re.MatchString(x) {
			c.errorf(path, "%q does not match %s", x, p)
		}
	}
	switch s["format"] {
	case "uri":
		if u, err := url.Parse(x); err != nil || u.Scheme == "" {
			c.errorf(path, "%q is not an absolute URI", x)
		}
	case "uri-reference":
		if _, err := url.Parse(x); err != nil {
			c.errorf(path, "%q is not a URI reference", x)
		}
	case "date-time":
		if _, err := time.Parse(time.RFC3339, x); err != nil {
			c.errorf(path, "%q is not an RFC 3339 date-time", x)
		}
	}
}

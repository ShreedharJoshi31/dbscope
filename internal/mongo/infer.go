// Package mongo reads MongoDB for dbscope (docs/ENGINES.md): collections
// have no declared schema, so each one's fields are inferred from a sample
// of its documents and shown in the shared relational model.
package mongo

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Field is one field path seen in a collection's sample.
type Field struct {
	Path string
	// Types counts documents by the BSON type the path held, e.g. {"string": 98, "null": 2}.
	Types map[string]int
	// Present counts the sampled documents that have the path at all.
	Present int
	// ArrayLens holds, for each sampled document where the path is an
	// array, its length (the longest when the path repeats inside an array).
	ArrayLens []int
}

// Shape is what a sample says about a collection.
type Shape struct {
	Sampled int
	Fields  []Field // ordered by first appearance, _id first
}

// maxDepth bounds how far into subdocuments paths are followed.
const maxDepth = 4

// Infer summarizes sampled documents. Subdocuments become dotted paths
// ("address.city"), array elements a "[]" path ("tags[]"); each path is
// counted once per document.
func Infer(docs []bson.Raw) Shape {
	s := Shape{Sampled: len(docs)}
	index := map[string]int{}
	for _, d := range docs {
		// Each path counts once per document, with every type it held there.
		var order []string
		seen := map[string]map[string]bool{}
		lens := map[string]int{}
		walk(d, "", 0, func(path, typ string, n int) {
			if seen[path] == nil {
				seen[path] = map[string]bool{}
				order = append(order, path)
			}
			seen[path][typ] = true
			if n >= 0 {
				lens[path] = max(lens[path], n)
			}
		})
		for _, path := range order {
			i, known := index[path]
			if !known {
				i = len(s.Fields)
				index[path] = i
				s.Fields = append(s.Fields, Field{Path: path, Types: map[string]int{}})
			}
			s.Fields[i].Present++
			for t := range seen[path] {
				s.Fields[i].Types[t]++
			}
			if n, ok := lens[path]; ok {
				s.Fields[i].ArrayLens = append(s.Fields[i].ArrayLens, n)
			}
		}
	}
	return s
}

// walk calls f for every path with its type and, for an array, its length (else -1).
func walk(d bson.Raw, prefix string, depth int, f func(path, typ string, n int)) {
	elems, err := d.Elements()
	if err != nil {
		return
	}
	for _, e := range elems {
		walkValue(prefix+e.Key(), e.Value(), depth, f)
	}
}

func walkValue(path string, v bson.RawValue, depth int, f func(path, typ string, n int)) {
	if v.Type != bson.TypeArray {
		f(path, typeName(v.Type), -1)
		if v.Type == bson.TypeEmbeddedDocument && depth < maxDepth {
			walk(v.Document(), path+".", depth+1, f)
		}
		return
	}
	vals, err := v.Array().Values()
	if err != nil {
		f(path, "array", -1)
		return
	}
	f(path, "array", len(vals))
	if depth >= maxDepth {
		return
	}
	for _, x := range vals {
		walkValue(path+"[]", x, depth+1, f)
	}
}

// typeName is the $type alias of a BSON type ("string", "objectId", "int", ...).
func typeName(t bson.Type) string {
	switch t {
	case bson.TypeDouble:
		return "double"
	case bson.TypeString:
		return "string"
	case bson.TypeEmbeddedDocument:
		return "object"
	case bson.TypeArray:
		return "array"
	case bson.TypeBinary:
		return "binData"
	case bson.TypeObjectID:
		return "objectId"
	case bson.TypeBoolean:
		return "bool"
	case bson.TypeDateTime:
		return "date"
	case bson.TypeNull:
		return "null"
	case bson.TypeRegex:
		return "regex"
	case bson.TypeInt32:
		return "int"
	case bson.TypeTimestamp:
		return "timestamp"
	case bson.TypeInt64:
		return "long"
	case bson.TypeDecimal128:
		return "decimal"
	}
	return strings.ToLower(t.String())
}

// TypeText describes a field's types for display: "string" when uniform,
// else the types by frequency with their share, "string 96% | null 4%".
func (f Field) TypeText() string {
	type tc struct {
		t string
		n int
	}
	var list []tc
	total := 0
	for t, n := range f.Types {
		list = append(list, tc{t, n})
		total += n
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].t < list[j].t
	})
	if len(list) == 1 {
		return list[0].t
	}
	parts := make([]string, len(list))
	for i, x := range list {
		parts[i] = fmt.Sprintf("%s %d%%", x.t, (x.n*100+total/2)/total)
	}
	return strings.Join(parts, " | ")
}

// Coverage is the share of sampled documents that have the field, 0-100.
func (s Shape) Coverage(f Field) int {
	if s.Sampled == 0 {
		return 0
	}
	return (f.Present*100 + s.Sampled/2) / s.Sampled
}

// topLevel reports a path that is a document's own field, not nested.
func topLevel(path string) bool { return !strings.ContainsAny(path, ".[") }

// scalarTypes are the types a reference to another document's _id can have.
var scalarTypes = []string{"objectId", "string", "int", "long", "binData"}

func onlyTypes(f Field, allowed []string) bool {
	for t := range f.Types {
		if t != "null" && !slices.Contains(allowed, t) {
			return false
		}
	}
	return true
}

package mongo

import (
	"slices"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// doc builds an ordered document from key, value pairs.
func doc(kv ...any) bson.D {
	var d bson.D
	for i := 0; i+1 < len(kv); i += 2 {
		d = append(d, bson.E{Key: kv[i].(string), Value: kv[i+1]})
	}
	return d
}

func raw(t *testing.T, d bson.D) bson.Raw {
	b, err := bson.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestInfer(t *testing.T) {
	oid := bson.NewObjectID()
	docs := []bson.Raw{
		raw(t, doc("_id", oid, "name", "a", "age", int32(3), "tags", bson.A{"x", "y"}, "address", doc("city", "Pune"))),
		raw(t, doc("_id", bson.NewObjectID(), "name", "b", "age", int64(4), "tags", bson.A{})),
		raw(t, doc("_id", bson.NewObjectID(), "name", nil, "age", "five", "address", doc("city", "Goa", "pin", int32(1)))),
		raw(t, doc("_id", bson.NewObjectID(), "name", "d")),
	}
	s := Infer(docs)
	var paths []string
	for _, f := range s.Fields {
		paths = append(paths, f.Path)
	}
	if want := []string{"_id", "name", "age", "tags", "tags[]", "address", "address.city", "address.pin"}; !slices.Equal(paths, want) {
		t.Fatalf("paths %v", paths)
	}
	byPath := map[string]Field{}
	for _, f := range s.Fields {
		byPath[f.Path] = f
	}
	if f := byPath["name"]; f.TypeText() != "string 75% | null 25%" || s.Coverage(f) != 100 {
		t.Errorf("name %q %d", f.TypeText(), s.Coverage(f))
	}
	if f := byPath["age"]; f.TypeText() != "int 33% | long 33% | string 33%" || s.Coverage(f) != 75 {
		t.Errorf("age %q %d", f.TypeText(), s.Coverage(f))
	}
	// tags[] counts one document even though it held two strings.
	if f := byPath["tags[]"]; f.Present != 1 || f.TypeText() != "string" {
		t.Errorf("tags[] %+v", f)
	}
	if f := byPath["_id"]; f.TypeText() != "objectId" || s.Coverage(f) != 100 {
		t.Errorf("_id %+v", f)
	}
}

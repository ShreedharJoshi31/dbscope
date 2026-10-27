package apply

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"
)

// Type is a column type in the form format_type() prints it, split into
// parts so two types can be compared: base "character varying", mods [20].
type Type struct {
	// Schema is set for user types written schema-qualified.
	Schema string
	// Base is the format_type name without modifiers, e.g. "numeric",
	// "timestamp with time zone", "app.user_status" (unqualified when the
	// schema is unknown).
	Base  string
	Mods  []int
	Array bool
	// Serial marks serial/bigserial/smallserial, which are integer types
	// with a nextval() default.
	Serial bool
}

// internal names libpg_query emits (pg_catalog.int4) and common aliases,
// mapped to format_type's names.
var typeNames = map[string]string{
	"int2": "smallint", "smallint": "smallint", "int4": "integer", "int": "integer", "integer": "integer",
	"int8": "bigint", "bigint": "bigint", "float4": "real", "real": "real", "float8": "double precision",
	"double precision": "double precision", "bool": "boolean", "boolean": "boolean",
	"varchar": "character varying", "character varying": "character varying",
	"bpchar": "character", "char": "character", "character": "character",
	"numeric": "numeric", "decimal": "numeric",
	"timestamp": "timestamp without time zone", "timestamp without time zone": "timestamp without time zone",
	"timestamptz": "timestamp with time zone", "timestamp with time zone": "timestamp with time zone",
	"time": "time without time zone", "time without time zone": "time without time zone",
	"timetz": "time with time zone", "time with time zone": "time with time zone",
	"varbit": "bit varying", "bit varying": "bit varying", "bit": "bit",
	"interval": "interval", "text": "text", "date": "date", "uuid": "uuid", "json": "json", "jsonb": "jsonb",
	"bytea": "bytea", "inet": "inet", "cidr": "cidr", "macaddr": "macaddr", "macaddr8": "macaddr8",
	"money": "money", "xml": "xml", "oid": "oid", "citext": "citext", "tsvector": "tsvector",
	"serial": "integer", "serial4": "integer", "bigserial": "bigint", "serial8": "bigint",
	"smallserial": "smallint", "serial2": "smallint",
}

var serials = map[string]bool{"serial": true, "serial4": true, "bigserial": true, "serial8": true, "smallserial": true, "serial2": true}

// TypeFromAST converts a parsed type name.
func TypeFromAST(tn *pg_query.TypeName) Type {
	var names []string
	for _, n := range tn.GetNames() {
		names = append(names, n.GetString_().GetSval())
	}
	t := Type{Array: len(tn.GetArrayBounds()) > 0}
	switch {
	case len(names) == 2 && names[0] == "pg_catalog":
		t.Base = names[1]
	case len(names) == 1:
		t.Base = names[0]
	case len(names) >= 2:
		t.Schema, t.Base = names[len(names)-2], names[len(names)-1]
	}
	if t.Schema == "" {
		if mapped, ok := typeNames[t.Base]; ok {
			t.Serial = serials[t.Base]
			t.Base = mapped
		}
	}
	for _, m := range tn.GetTypmods() {
		if c := m.GetAConst(); c != nil && c.GetIval() != nil {
			t.Mods = append(t.Mods, int(c.GetIval().GetIval()))
		}
	}
	if t.Base == "interval" {
		t.Mods = nil // interval typmods encode field masks; not compared
	}
	return t
}

var formatted = regexp.MustCompile(`^(.*?)(?:\((\d+)(?:,(\d+))?\))?( with(?:out)? time zone)?(\[\])?$`)

// ParseType reads a format_type() string, e.g. "character varying(20)",
// "timestamp(3) with time zone", "numeric(10,2)[]".
func ParseType(s string) Type {
	m := formatted.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Type{Base: s}
	}
	t := Type{Base: m[1] + m[4], Array: m[5] != ""}
	for _, v := range m[2:4] {
		if v != "" {
			n, _ := strconv.Atoi(v)
			t.Mods = append(t.Mods, n)
		}
	}
	if i := strings.LastIndexByte(t.Base, '.'); i > 0 && !strings.Contains(t.Base, " ") {
		t.Schema, t.Base = t.Base[:i], t.Base[i+1:]
	}
	return t
}

// String renders the type as format_type() would.
func (t Type) String() string {
	base := t.Base
	if t.Schema != "" {
		base = t.Schema + "." + base
	}
	if t.Base == "numeric" && len(t.Mods) == 1 {
		t.Mods = []int{t.Mods[0], 0}
	}
	mods := ""
	if len(t.Mods) > 0 {
		parts := make([]string, len(t.Mods))
		for i, m := range t.Mods {
			parts[i] = strconv.Itoa(m)
		}
		mods = "(" + strings.Join(parts, ",") + ")"
	}
	out := base + mods
	for _, tz := range []string{" with time zone", " without time zone"} {
		if prefix, ok := strings.CutSuffix(base, tz); ok && mods != "" {
			out = prefix + mods + tz
		}
	}
	if t.Array {
		out += "[]"
	}
	return out
}

// Equal compares types, ignoring the schema when only one side has it.
func (t Type) Equal(u Type) bool {
	if t.Schema != "" && u.Schema != "" && t.Schema != u.Schema {
		return false
	}
	return t.Base == u.Base && t.Array == u.Array && slices.Equal(t.normMods(), u.normMods())
}

func (t Type) normMods() []int {
	if t.Base == "numeric" && len(t.Mods) == 1 {
		return []int{t.Mods[0], 0}
	}
	return t.Mods
}

// Change classifies an ALTER COLUMN TYPE from old to new.
type Change struct {
	// Rewrite is true unless PostgreSQL can skip the table rewrite (the
	// change is binary coercible and needs no per-row check).
	Rewrite bool
	// Narrowing marks changes that can reject or alter existing values
	// (shorter varchar, smaller integer, lower numeric precision or scale).
	Narrowing bool
	Reason    string
}

// textual types that are binary coercible to each other.
var textLike = map[string]bool{"text": true, "character varying": true}

// intRank orders the integer types by width.
var intRank = map[string]int{"smallint": 1, "integer": 2, "bigint": 3}

// TypeChange decides whether old -> new rewrites the table. It follows
// ATColumnChangeRequiresRewrite: only binary-coercible casts whose length
// coercion PostgreSQL can prove is a no-op (varchar_support, numeric_support)
// skip the rewrite. Unknown old types are assumed to rewrite.
func TypeChange(old, new Type) Change {
	switch {
	case old.Base == "":
		return Change{Rewrite: true, Reason: "the current column type is unknown to dbscope"}
	case old.Equal(new):
		return Change{Reason: "the type does not change"}
	case old.Array != new.Array:
		return Change{Rewrite: true, Narrowing: true, Reason: "array-ness changes"}
	}
	o, n := old.normMods(), new.normMods()
	switch {
	case textLike[old.Base] && textLike[new.Base]:
		oldLen, newLen := limit(old), limit(new)
		switch {
		case newLen < 0:
			return Change{Reason: fmt.Sprintf("%s to %s is binary coercible", old, new)}
		case oldLen >= 0 && newLen >= oldLen:
			return Change{Reason: fmt.Sprintf("%s to %s only raises the length limit", old, new)}
		default:
			return Change{Rewrite: true, Narrowing: true, Reason: fmt.Sprintf("%s to %s adds or lowers a length limit, so every value is checked", old, new)}
		}
	case old.Base == "numeric" && new.Base == "numeric":
		switch {
		case len(n) == 0:
			return Change{Reason: "numeric with a precision to unconstrained numeric is binary coercible"}
		case len(o) == 2 && o[1] == n[1] && n[0] >= o[0]:
			return Change{Reason: fmt.Sprintf("numeric precision rises from %d to %d at the same scale", o[0], n[0])}
		default:
			return Change{Rewrite: true, Narrowing: len(o) == 0 || n[0]-n[1] < o[0]-o[1] || n[1] < o[1],
				Reason: fmt.Sprintf("%s to %s changes the scale or lowers the precision", old, new)}
		}
	case old.Base == "bit varying" && new.Base == "bit varying":
		if len(n) == 0 || (len(o) == 1 && n[0] >= o[0]) {
			return Change{Reason: "bit varying limit rises"}
		}
		return Change{Rewrite: true, Narrowing: true, Reason: "bit varying limit falls"}
	case old.Base == "cidr" && new.Base == "inet":
		return Change{Reason: "cidr to inet is binary coercible"}
	case intRank[old.Base] > 0 && intRank[new.Base] > 0:
		return Change{Rewrite: true, Narrowing: intRank[new.Base] < intRank[old.Base],
			Reason: fmt.Sprintf("%s to %s changes the storage size", old, new)}
	case old.Base == new.Base:
		// Same type, different modifiers (character(n), timestamp(p), ...).
		narrow := len(o) > 0 && (len(n) == 0 || n[0] < o[0])
		if old.Base == "timestamp with time zone" || old.Base == "timestamp without time zone" ||
			old.Base == "time without time zone" || old.Base == "time with time zone" || old.Base == "interval" {
			if len(n) == 0 || (len(o) > 0 && n[0] >= o[0]) {
				return Change{Reason: "the fractional-second precision rises"}
			}
		}
		return Change{Rewrite: true, Narrowing: narrow, Reason: fmt.Sprintf("%s to %s changes the type modifier", old, new)}
	}
	// Other conversions rewrite. Converting text into another type can
	// reject values; other cross-type changes are reported as rewrites only.
	return Change{Rewrite: true, Narrowing: textLike[old.Base] || new.Base == "character",
		Reason: fmt.Sprintf("%s to %s is not binary coercible", old, new)}
}

// limit is the length limit of a text-like type, or -1 for none.
func limit(t Type) int {
	if t.Base == "text" || len(t.Mods) == 0 {
		return -1
	}
	return t.Mods[0]
}

package mysql

import (
	"fmt"
	"slices"
	"strings"
)

// Algo is how InnoDB carries out an ALTER, from cheapest to dearest.
type Algo int

const (
	Instant Algo = iota // metadata only
	Nocopy              // MariaDB: in place without rebuilding the clustered index
	Inplace             // in place; may still rebuild the table
	Copy                // a new table, rows copied over
)

func (a Algo) String() string { return [...]string{"INSTANT", "NOCOPY", "INPLACE", "COPY"}[a] }

// Plan is what one ALTER clause, or a whole statement, costs.
type Plan struct {
	Algo Algo
	// Rebuild means the table is rewritten, whatever the algorithm.
	Rebuild bool
	// DML means writes to the table continue while it runs.
	DML bool
	// Index means an index is built by reading the table.
	Index bool
	What  string // a short description, e.g. "change the type of total"
	// Notes are facts about this clause worth a finding (data checks, data loss).
	Notes []Note
}

// Note is a fact the rules turn into a finding.
type Note struct {
	Rule    string
	Message string
	Severe  bool
}

// ColumnInfo is what a rule needs to know about a base column.
type ColumnInfo struct {
	Type     string // COLUMN_TYPE, lower-case, e.g. "varchar(100)"
	Nullable bool
	// BytesPerChar is the column charset's maximum bytes per character (1 for binary types).
	BytesPerChar int
}

// TableInfo is what a rule needs to know about a base table.
type TableInfo struct {
	Rows, Bytes  int64
	Sized        bool
	RowFormat    string // lower-case: dynamic, compact, redundant, compressed
	HasFulltext  bool
	Columns      map[string]ColumnInfo // keyed by lower-case name
	ForeignKeys  int                   // keys from or to the table
	CascadingFKs bool
	PrimaryKey   bool
}

// Session is the state earlier statements of the migration set.
type Session struct {
	ForeignKeyChecks bool // foreign_key_checks; on unless the migration turned it off
	LockWaitTimeout  int  // seconds; 0 means the server's default of a year
}

// clauseSplit divides the tokens after ALTER TABLE name at top-level commas.
func clauseSplit(toks []Token) [][]Token {
	var out [][]Token
	depth, start := 0, 0
	for i, t := range toks {
		switch t.Text {
		case "(":
			depth++
		case ")":
			depth--
		case ",":
			if depth == 0 {
				out = append(out, toks[start:i])
				start = i + 1
			}
		}
	}
	return append(out, toks[start:])
}

// words returns the lower-case words of tokens, and quoted names unquoted.
func words(toks []Token) []string {
	var out []string
	for _, t := range toks {
		switch {
		case t.Word != "":
			out = append(out, t.Word)
		case t.Text[0] == '`':
			out = append(out, strings.ToLower(strings.ReplaceAll(strings.Trim(t.Text, "`"), "``", "`")))
		}
	}
	return out
}

// name is an identifier token unquoted, keeping its case.
func name(t Token) string {
	if t.Text != "" && t.Text[0] == '`' {
		return strings.ReplaceAll(strings.Trim(t.Text, "`"), "``", "`")
	}
	return t.Text
}

// clauseKind names one ALTER clause by its leading words, e.g. "add column".
func clauseKind(w []string) string {
	at := func(i int) string {
		if i < len(w) {
			return w[i]
		}
		return ""
	}
	switch at(0) {
	case "add":
		switch at(1) {
		case "column":
			return "add column"
		case "index", "key":
			return "add index"
		case "unique":
			return "add unique"
		case "fulltext":
			return "add fulltext"
		case "spatial":
			return "add spatial"
		case "primary":
			return "add primary key"
		case "foreign":
			return "add foreign key"
		case "check":
			return "add check"
		case "partition":
			return "add partition"
		case "constraint":
			for _, x := range w[2:] {
				switch x {
				case "primary":
					return "add primary key"
				case "unique":
					return "add unique"
				case "foreign":
					return "add foreign key"
				case "check":
					return "add check"
				}
			}
			return "add constraint"
		}
		return "add column"
	case "drop":
		switch at(1) {
		case "column":
			return "drop column"
		case "index", "key":
			return "drop index"
		case "primary":
			return "drop primary key"
		case "foreign":
			return "drop foreign key"
		case "check", "constraint":
			return "drop constraint"
		case "partition":
			return "drop partition"
		}
		return "drop column"
	case "modify":
		return "modify column"
	case "change":
		return "change column"
	case "alter":
		if at(1) == "index" {
			return "alter index"
		}
		return "alter column"
	case "rename":
		switch at(1) {
		case "column":
			return "rename column"
		case "index", "key":
			return "rename index"
		}
		return "rename table"
	case "convert":
		return "convert charset"
	case "default", "character", "charset", "collate":
		return "table charset"
	case "engine", "force", "row_format", "key_block_size":
		return "rebuild"
	case "algorithm":
		return "algorithm"
	case "lock":
		return "lock"
	case "partition":
		return "partition by"
	case "remove":
		return "remove partitioning"
	case "coalesce", "reorganize", "rebuild", "optimize", "truncate", "exchange", "analyze", "check", "repair", "discard", "import":
		if at(1) == "partition" {
			return at(0) + " partition"
		}
	case "order":
		return "order by"
	}
	return "table option"
}

// columnOf returns the column a clause names and, for ADD/MODIFY/CHANGE, its
// new definition's words.
func columnOf(kind string, toks []Token) (col, newName string, def []Token) {
	i := 1
	if i < len(toks) && toks[i].Word == "column" {
		i++
	}
	if i < len(toks) && toks[i].Word == "if" { // MariaDB IF [NOT] EXISTS
		for i < len(toks) && toks[i].Word != "exists" {
			i++
		}
		i++
	}
	if i >= len(toks) {
		return "", "", nil
	}
	col = name(toks[i])
	newName = col
	switch kind {
	case "change column":
		if i+1 < len(toks) {
			newName = name(toks[i+1])
			return col, newName, toks[min(i+2, len(toks)):]
		}
	case "rename column":
		if i+2 < len(toks) {
			newName = name(toks[i+2])
		}
		return col, newName, nil
	case "add column", "modify column":
		return col, newName, toks[i+1:]
	}
	return col, newName, nil
}

// typeOf is a column definition's type, lower-case without spaces, e.g. "varchar(300)".
func typeOf(def []Token) string {
	var b strings.Builder
	depth := 0
	for i, t := range def {
		if i > 0 && depth == 0 && t.Text != "(" {
			// unsigned, zerofill and character set belong to the type; the rest does not.
			if t.Word != "unsigned" && t.Word != "zerofill" {
				break
			}
			b.WriteString(" ")
		}
		switch t.Text {
		case "(":
			depth++
		case ")":
			depth--
		}
		b.WriteString(strings.ToLower(t.Text))
	}
	return b.String()
}

// baseLength reads n from "varchar(n)" or "char(n)"; ok is false for other types.
func baseLength(typ string) (kind string, n int, ok bool) {
	open := strings.IndexByte(typ, '(')
	if open < 0 {
		return "", 0, false
	}
	kind = typ[:open]
	if kind != "varchar" && kind != "varbinary" {
		return "", 0, false
	}
	_, err := fmt.Sscanf(typ[open:], "(%d)", &n)
	return kind, n, err == nil
}

// enumMembers returns the members of "enum('a','b')" or "set(...)".
func enumMembers(typ string) (kind string, members []string, ok bool) {
	for _, k := range []string{"enum(", "set("} {
		if strings.HasPrefix(typ, k) && strings.HasSuffix(typ, ")") {
			body := typ[len(k) : len(typ)-1]
			for _, t := range Tokens(body) {
				if t.Text[0] == '\'' {
					members = append(members, t.Text)
				}
			}
			return strings.TrimSuffix(k, "("), members, true
		}
	}
	return "", nil, false
}

func has(w []string, x ...string) bool {
	for _, y := range x {
		if slices.Contains(w, y) {
			return true
		}
	}
	return false
}

// notNull reports NOT NULL among definition words.
func notNull(w []string) bool {
	for i := 0; i+1 < len(w); i++ {
		if w[i] == "not" && w[i+1] == "null" {
			return true
		}
	}
	return false
}

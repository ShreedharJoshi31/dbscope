package mysql

import (
	"fmt"
	"regexp"
	"strings"
)

// intWidth matches an integer type's display width, which MariaDB and MySQL
// before 8.0.19 print and which changes nothing.
var intWidth = regexp.MustCompile(`^(tinyint|smallint|mediumint|int|integer|bigint)\(\d+\)`)

func normType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	t = intWidth.ReplaceAllString(t, "$1")
	return strings.ReplaceAll(t, "integer", "int")
}

// planClause predicts one ALTER clause's cost on the server, following the
// online DDL tables in docs/research-mysql.md. The verify tests in
// ddl_verify_test.go check the predictions against real servers.
func planClause(srv Server, tbl *TableInfo, sess Session, kind string, toks []Token, siblings []string) Plan {
	maria := srv.Flavor == "mariadb"
	v := srv.Num
	w := words(toks)
	meta := Plan{Algo: Instant, DML: true}
	inplaceMeta := Plan{Algo: Inplace, DML: true}
	rebuild := Plan{Algo: Inplace, Rebuild: true, DML: true}
	copyBlocking := Plan{Algo: Copy, Rebuild: true, DML: false}
	// MariaDB 11.2 runs COPY with concurrent writes, except with cascading foreign keys.
	if maria && v >= 110200 && (tbl == nil || !tbl.CascadingFKs) {
		copyBlocking.DML = true
	}
	compressed := tbl != nil && tbl.RowFormat == "compressed"
	fulltext := tbl != nil && tbl.HasFulltext

	switch kind {
	case "add column":
		col, _, def := columnOf(kind, toks)
		dw := words(def)
		p := meta
		p.What = "add column " + col
		switch {
		case has(dw, "auto_increment"):
			p = Plan{Algo: Inplace, Rebuild: true, DML: false, What: p.What}
		case has(dw, "stored", "persistent") && has(dw, "as"):
			p = copyBlocking
			p.What = "add stored generated column " + col
		case has(dw, "as") && !has(dw, "stored", "persistent"):
			p.What = "add virtual generated column " + col
		case maria:
			if v < 100300 || fulltext || (v < 100400 && has(dw, "first", "after")) {
				p = rebuild
				p.What = "add column " + col
			}
		default:
			last := !has(dw, "first", "after")
			if v < 80012 || (v < 80029 && !last) || fulltext || compressed {
				p = rebuild
				p.What = "add column " + col
			}
		}
		if p.Algo == Instant && !maria {
			p.Notes = append(p.Notes, Note{Rule: "DBS-MYSQL-ROW-VERSIONS", Message: rowVersionsMessage(v)})
		}
		return p

	case "drop column":
		col, _, _ := columnOf(kind, toks)
		p := meta
		p.What = "drop column " + col
		if (maria && v < 100400) || (!maria && v < 80029) || fulltext || compressed {
			p = rebuild
			p.What = "drop column " + col
		}
		return p

	case "rename column":
		col, to, _ := columnOf(kind, toks)
		p := meta
		if !maria && v < 80028 {
			p = inplaceMeta
		}
		p.What = fmt.Sprintf("rename column %s to %s", col, to)
		return p

	case "modify column", "change column":
		return planModify(srv, tbl, kind, toks)

	case "alter column":
		col, _, _ := columnOf(kind, toks)
		p := meta
		p.What = "change the default or visibility of " + col
		return p

	case "add index", "add unique":
		p := Plan{Algo: Inplace, DML: true, Index: true, What: strings.Join(w[:min(3, len(w))], " ")}
		if maria {
			p.Algo = Nocopy
		}
		if kind == "add unique" {
			p.Notes = append(p.Notes, Note{Rule: "DBS-MYSQL-UNIQUE-DATA", Message: "a unique index fails if existing rows have duplicates, which a schema check cannot see"})
		}
		return p

	case "add fulltext":
		p := Plan{Algo: Inplace, DML: false, Index: true, Rebuild: !fulltext, What: "add a FULLTEXT index"}
		if maria && fulltext {
			p.Algo = Nocopy
		}
		return p

	case "add spatial":
		p := Plan{Algo: Inplace, DML: false, Index: true, What: "add a SPATIAL index"}
		if maria {
			p.Algo = Nocopy
		}
		return p

	case "drop index", "rename index", "alter index":
		p := inplaceMeta
		p.What = strings.Join(w[:min(3, len(w))], " ")
		return p

	case "add primary key":
		p := rebuild
		p.What = "add a primary key"
		p.Notes = append(p.Notes, Note{Rule: "DBS-MYSQL-UNIQUE-DATA", Message: "a primary key fails if existing rows have duplicates or NULLs in its columns, which a schema check cannot see"})
		return p

	case "drop primary key":
		// Dropping and adding a primary key in one statement is in place; dropping alone copies.
		p := copyBlocking
		if containsKind(siblings, "add primary key") {
			p = rebuild
		}
		p.What = "drop the primary key"
		return p

	case "add foreign key":
		if sess.ForeignKeyChecks {
			p := copyBlocking
			p.What = "add a foreign key with foreign_key_checks on"
			return p
		}
		p := inplaceMeta
		if maria {
			p.Algo = Nocopy
		}
		p.What = "add a foreign key with foreign_key_checks off"
		p.Notes = append(p.Notes, Note{Rule: "DBS-MYSQL-FK-UNCHECKED", Message: "with foreign_key_checks off, existing rows are not checked against the new foreign key"})
		return p

	case "drop foreign key", "drop constraint":
		p := inplaceMeta
		if maria {
			p = meta
		}
		p.What = strings.Join(w[:min(3, len(w))], " ")
		return p

	case "add check":
		// Existing rows are checked by copying the table (MySQL 8.0.16+, MariaDB).
		p := copyBlocking
		p.What = "add a CHECK constraint"
		return p

	case "convert charset":
		// Verified on MySQL 8.4: a change of encoding copies the table.
		p := copyBlocking
		p.What = "convert the table's character set"
		return p

	case "table charset", "table option", "algorithm", "lock":
		p := inplaceMeta
		p.What = strings.Join(w[:min(3, len(w))], " ")
		if maria || kind == "algorithm" || kind == "lock" {
			p.Algo = Instant
		}
		return p

	case "rebuild":
		p := rebuild
		if fulltext && !maria {
			p = copyBlocking
		}
		p.What = "rebuild the table (" + strings.Join(w[:min(2, len(w))], " ") + ")"
		return p

	case "rename table":
		p := meta
		p.DML = !maria // MariaDB renames under LOCK=EXCLUSIVE, for an instant
		p.What = "rename the table"
		return p

	case "partition by", "remove partitioning", "order by":
		p := copyBlocking
		p.What = kind
		return p

	case "drop partition", "truncate partition":
		p := inplaceMeta
		p.What = kind
		p.Notes = append(p.Notes, Note{Rule: "DBS-MYSQL-PARTITION-DATA", Severe: true,
			Message: kind + " deletes every row in the partition; it is not a schema-only change"})
		return p

	case "add partition", "discard partition", "import partition", "exchange partition", "analyze partition", "check partition", "repair partition":
		p := inplaceMeta
		p.What = kind
		return p

	case "coalesce partition", "reorganize partition", "rebuild partition", "optimize partition":
		p := Plan{Algo: Inplace, Rebuild: true, DML: false, What: kind}
		return p
	}
	p := inplaceMeta
	p.What = strings.Join(w[:min(3, len(w))], " ")
	return p
}

func containsKind(kinds []string, k string) bool {
	for _, x := range kinds {
		if x == k {
			return true
		}
	}
	return false
}

func rowVersionsMessage(v int) string {
	limit := 64
	if v >= 90100 {
		limit = 255
	}
	return fmt.Sprintf("each INSTANT ADD or DROP COLUMN uses one of the table's %d row versions (INFORMATION_SCHEMA.INNODB_TABLES.TOTAL_ROW_VERSIONS); at the limit they fail until a rebuilding ALTER resets it", limit)
}

// planModify compares a MODIFY or CHANGE COLUMN with the base column.
func planModify(srv Server, tbl *TableInfo, kind string, toks []Token) Plan {
	maria := srv.Flavor == "mariadb"
	v := srv.Num
	col, newName, def := columnOf(kind, toks)
	dw := words(def)
	newType := normType(typeOf(def))
	what := "change column " + col
	copyPlan := Plan{Algo: Copy, Rebuild: true, DML: maria && v >= 110200 && (tbl == nil || !tbl.CascadingFKs), What: what}
	var base *ColumnInfo
	if tbl != nil {
		if c, ok := tbl.Columns[strings.ToLower(col)]; ok {
			base = &c
		}
	}
	if base == nil {
		p := copyPlan
		p.What = what + " (the base column is unknown, so a table copy is assumed)"
		return p
	}
	oldType := normType(base.Type)
	reorder := has(dw, "first", "after")
	renamed := !strings.EqualFold(col, newName)
	if renamed {
		what = fmt.Sprintf("rename column %s to %s", col, newName)
	}

	var p Plan
	switch {
	case oldType == newType:
		p = Plan{Algo: Instant, DML: true, What: what}
		if !maria && v < 80028 && renamed {
			p.Algo = Inplace
		}
	default:
		p = typeChange(srv, tbl, base, oldType, newType, col)
	}
	newNotNull := notNull(dw)
	switch {
	case base.Nullable && newNotNull:
		p = worse(p, Plan{Algo: Inplace, Rebuild: true, DML: true})
		p.Notes = append(p.Notes, Note{Rule: "DBS-MYSQL-NOT-NULL", Message: "NOT NULL on " + col + " fails if any row holds NULL there (with strict SQL mode), and rebuilds the table"})
	case !base.Nullable && !newNotNull && !has(dw, "primary"):
		// MODIFY restates the whole definition: leaving out NOT NULL makes the column nullable.
		rebuildNull := Plan{Algo: Inplace, Rebuild: true, DML: true}
		if maria && tbl != nil && tbl.RowFormat == "redundant" {
			rebuildNull = Plan{Algo: Instant, DML: true}
		}
		p = worse(p, rebuildNull)
		p.Notes = append(p.Notes, Note{Rule: "DBS-MYSQL-NULLABLE", Message: kind + " restates the whole column: without NOT NULL, " + col + " becomes nullable"})
	}
	if reorder {
		if maria && v >= 100400 {
			p = worse(p, Plan{Algo: Instant, DML: true})
		} else {
			p = worse(p, Plan{Algo: Inplace, Rebuild: true, DML: true})
		}
	}
	if p.What == "" {
		p.What = what
	}
	return p
}

// typeChange is the cost of changing a column's type.
func typeChange(srv Server, tbl *TableInfo, base *ColumnInfo, oldType, newType, col string) Plan {
	maria := srv.Flavor == "mariadb"
	copyPlan := Plan{Algo: Copy, Rebuild: true, DML: maria && srv.Num >= 110200 && (tbl == nil || !tbl.CascadingFKs),
		What: fmt.Sprintf("change the type of %s from %s to %s", col, oldType, newType)}
	oldKind, oldN, ok := baseLength(oldType)
	newKind, newN, okNew := baseLength(newType)
	if ok && okNew && oldKind == newKind && newN >= oldN {
		bpc := max(base.BytesPerChar, 1)
		oldBytes, newBytes := oldN*bpc, newN*bpc
		p := Plan{Algo: Inplace, DML: true, What: fmt.Sprintf("widen %s from %s to %s", col, oldType, newType)}
		if maria {
			p.Algo = Instant
		}
		if (oldBytes <= 255) != (newBytes <= 255) {
			// The length prefix grows from one byte to two.
			redundant := tbl != nil && tbl.RowFormat == "redundant"
			if !maria || !(redundant || oldBytes <= 127) {
				p = copyPlan
				p.What = fmt.Sprintf("widen %s from %s to %s, past 255 bytes (%d bytes per character)", col, oldType, newType, bpc)
			}
		}
		return p
	}
	oldEnum, oldMembers, ok1 := enumMembers(oldType)
	newEnum, newMembers, ok2 := enumMembers(newType)
	if ok1 && ok2 && oldEnum == newEnum && len(newMembers) >= len(oldMembers) {
		appended := true
		for i, m := range oldMembers {
			appended = appended && newMembers[i] == m
		}
		storageSame := (oldEnum == "enum" && (len(oldMembers) <= 255) == (len(newMembers) <= 255)) ||
			(oldEnum == "set" && (len(oldMembers)+7)/8 == (len(newMembers)+7)/8)
		if appended && storageSame {
			return Plan{Algo: Instant, DML: true, What: "append members to " + col}
		}
	}
	return copyPlan
}

// worse combines two plans: the dearer algorithm, and the stricter of the rest.
func worse(a, b Plan) Plan {
	out := a
	out.Algo = max(a.Algo, b.Algo)
	out.Rebuild = a.Rebuild || b.Rebuild
	out.DML = a.DML && b.DML
	out.Index = a.Index || b.Index
	out.Notes = append(append([]Note{}, a.Notes...), b.Notes...)
	if out.What == "" {
		out.What = b.What
	}
	return out
}

package neo4j

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dbscope/dbscope/internal/schema"
)

// Supernode thresholds: relationships on one node. Expanding or traversing
// through such a node reads all of them unless the pattern filters by type
// and direction.
const (
	supernodeWarn   = 1_000
	supernodeDanger = 100_000
	// degreeSample bounds the nodes whose degree is read per label, and
	// degreeTimeout the time; COUNT { (n)--() } reads each node's degree
	// from its record, so the cost is the sample, not the relationships.
	degreeSample  = 1_000
	degreeTimeout = 5 * time.Second
	// unindexedRows is the size from which a label with no index at all is flagged.
	unindexedRows = 10_000
)

// addFindings records design risks on a label: supernodes in a sample of its
// nodes, properties stored with more than one type, and a large label with no
// index. A sample that cannot be read in time is skipped, never guessed.
func (c *Client) addFindings(ctx context.Context, r *schema.Relation) {
	if r.RowEstimate != nil && *r.RowEstimate > 0 {
		c.addSupernode(ctx, r)
	}
	for _, col := range r.Columns {
		if strings.HasPrefix(col.Name, "-[:") || !strings.Contains(col.Type, " | ") {
			continue
		}
		r.AddFinding("DBS-NEO4J-MIXED-TYPES", "warn", fmt.Sprintf("%s holds %s values on different nodes; "+
			"comparisons and range indexes treat each type separately, so a query for one type misses the others", col.Name, col.Type), col.Name)
	}
	if r.RowEstimate != nil && *r.RowEstimate >= unindexedRows && len(r.Indexes) == 0 && len(r.Uniques) == 0 && len(r.PrimaryKey) == 0 {
		r.AddFinding("DBS-NEO4J-NO-INDEX", "warn", fmt.Sprintf("%d nodes and no index or constraint: every lookup by a property "+
			"scans all of them (NodeByLabelScan); index the properties queries match on", *r.RowEstimate), "")
	}
}

func (c *Client) addSupernode(ctx context.Context, r *schema.Relation) {
	recs, _, err := c.read(ctx, degreeTimeout, "MATCH (n:"+quote(r.Name)+") WITH n LIMIT $sample "+
		"WITH COUNT { (n)--() } AS d ORDER BY d DESC WITH collect(d) AS ds "+
		"RETURN ds[0] AS top, ds[size(ds) / 2] AS median, size(ds) AS sampled", map[string]any{"sample": int64(degreeSample)})
	if err != nil || len(recs) == 0 {
		return
	}
	top, _ := recs[0].Values[0].(int64)
	median, _ := recs[0].Values[1].(int64)
	sampled, _ := recs[0].Values[2].(int64)
	level := ""
	switch {
	case top >= supernodeDanger:
		level = "danger"
	case top >= supernodeWarn:
		level = "warn"
	default:
		return
	}
	var who string
	switch {
	case sampled == 1:
		who = fmt.Sprintf("The only %s node has %d relationships", r.Name, top)
	case r.RowEstimate != nil && sampled < *r.RowEstimate:
		who = fmt.Sprintf("One node in a sample of %d %s nodes has %d relationships (median %d)", sampled, r.Name, top, median)
	default:
		who = fmt.Sprintf("One %s node has %d relationships (median %d)", r.Name, top, median)
	}
	r.AddFinding("DBS-NEO4J-SUPERNODE", level, who+": a traversal through it reads all of them; "+
		"match it by relationship type and direction, or split the hub", "")
}

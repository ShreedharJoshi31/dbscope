package neo4j

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	drv "github.com/neo4j/neo4j-go-driver/v6/neo4j"

	"github.com/dbscope/dbscope/internal/health"
)

// slowTxAfter is how long a transaction runs before it counts as long-running.
var slowTxAfter = time.Minute

// Health reads the server's health checks (docs/HEALTH.md) with SHOW
// commands, in read transactions that are rolled back like every dbscope read.
func Health(ctx context.Context, c *Client) (*health.Report, error) {
	start := time.Now()
	srv, err := describe(ctx, c)
	if err != nil {
		return nil, err
	}
	r := &health.Report{Engine: "neo4j", Server: fmt.Sprintf("Neo4j %s %s", srv.Version, srv.Edition), Checks: []health.Check{}}
	system := &Client{Driver: c.Driver, Database: "system"}
	why := func(err error) string {
		var ne *drv.Neo4jError
		if errors.As(err, &ne) && strings.HasPrefix(ne.Code, "Neo.ClientError.Security.") {
			return "The user may not run this SHOW command: grant it the matching SHOW privilege (Enterprise) or use an admin user."
		}
		return err.Error()
	}

	recs, _, err := system.read(ctx, 10*time.Second, `SHOW DATABASES YIELD name, currentStatus, requestedStatus, statusMessage
		RETURN name, currentStatus, requestedStatus, statusMessage`, nil)
	if err != nil {
		r.Unavailable("neo4j.databases", "Databases", why(err))
	} else {
		var down []string
		for _, rec := range recs {
			name, _ := rec.Get("name")
			cur, _ := rec.Get("currentStatus")
			req, _ := rec.Get("requestedStatus")
			msg, _ := rec.Get("statusMessage")
			if fmt.Sprint(cur) != fmt.Sprint(req) {
				down = append(down, fmt.Sprintf("%v is %v, should be %v %v", name, cur, req, msg))
			}
		}
		c := health.Check{ID: "neo4j.databases", Title: "Databases", Status: health.OK, Value: fmt.Sprintf("%d, all as requested", len(recs))}
		if len(down) > 0 {
			c.Status, c.Value = health.Danger, fmt.Sprintf("%d not as requested", len(down))
			c.Detail, c.Fix = strings.Join(down, "; "), "Read the status message and the server's debug.log"
		}
		r.Add(c)
	}

	if srv.Community {
		// Community runs one server and has no SHOW SERVERS.
		r.Add(health.Check{ID: "neo4j.servers", Title: "Servers", Status: health.OK, Value: "one server (Community)"})
	} else if recs, _, err = system.read(ctx, 10*time.Second, `SHOW SERVERS YIELD name, address, health, state RETURN name, address, health, state`, nil); err != nil {
		r.Unavailable("neo4j.servers", "Servers", why(err))
	} else {
		var bad []string
		for _, rec := range recs {
			h, _ := rec.Get("health")
			st, _ := rec.Get("state")
			addr, _ := rec.Get("address")
			if fmt.Sprint(h) != "Available" || fmt.Sprint(st) != "Enabled" {
				bad = append(bad, fmt.Sprintf("%v %v, %v", addr, h, st))
			}
		}
		c := health.Check{ID: "neo4j.servers", Title: "Servers", Status: health.OK, Value: fmt.Sprintf("%d available", len(recs))}
		if len(bad) > 0 {
			c.Status, c.Value, c.Detail = health.Danger, fmt.Sprintf("%d of %d not available", len(bad), len(recs)), strings.Join(bad, "; ")
		}
		r.Add(c)
	}

	recs, _, err = c.read(ctx, 10*time.Second, `SHOW TRANSACTIONS YIELD database, elapsedTime, currentQuery, username
		WHERE elapsedTime.milliseconds >= $ms RETURN database, elapsedTime.milliseconds AS ms, currentQuery, username`,
		map[string]any{"ms": slowTxAfter.Milliseconds()})
	if err != nil {
		r.Unavailable("neo4j.long_transactions", "Long-running transactions", why(err))
	} else {
		var parts []string
		for _, rec := range recs {
			db, _ := rec.Get("database")
			ms, _ := rec.Get("ms")
			q, _ := rec.Get("currentQuery")
			user, _ := rec.Get("username")
			ms64, _ := ms.(int64)
			text := strings.TrimSpace(fmt.Sprint(q))
			if len(text) > 80 {
				text = text[:80] + "…"
			}
			parts = append(parts, fmt.Sprintf("%v on %v for %s: %s", user, db, health.Duration(time.Duration(ms64)*time.Millisecond), text))
		}
		r.Add(health.Check{ID: "neo4j.long_transactions", Title: "Long-running transactions", Status: health.Level(float64(len(recs)), 1, 5),
			Value: fmt.Sprintf("%d over %s", len(recs), health.Duration(slowTxAfter)), Detail: strings.Join(parts, "; "),
			Fix: "TERMINATE TRANSACTION 'id' stops one; set db.transaction.timeout to bound them"})
	}

	recs, _, err = c.read(ctx, 10*time.Second, `SHOW INDEXES YIELD name, state, populationPercent WHERE state <> 'ONLINE'
		RETURN name, state, populationPercent`, nil)
	if err != nil {
		r.Unavailable("neo4j.indexes", "Indexes not online", why(err))
	} else {
		var parts []string
		status := health.OK
		for _, rec := range recs {
			name, _ := rec.Get("name")
			st, _ := rec.Get("state")
			pct, _ := rec.Get("populationPercent")
			parts = append(parts, fmt.Sprintf("%v %v (%v%%)", name, st, pct))
			if fmt.Sprint(st) == "FAILED" {
				status = health.Danger
			} else if status == health.OK {
				status = health.Warn
			}
		}
		c := health.Check{ID: "neo4j.indexes", Title: "Indexes not online", Status: status, Value: fmt.Sprint(len(recs)), Detail: strings.Join(parts, "; ")}
		if len(recs) == 0 {
			c.Value = "none"
		} else {
			c.Fix = "SHOW INDEXES YIELD * shows the failure message; drop and recreate a failed index"
		}
		r.Add(c)
	}

	r.TookMs = time.Since(start).Milliseconds()
	return r, nil
}

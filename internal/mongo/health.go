package mongo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/dbscope/dbscope/internal/health"
)

// monitorGrant is the role the server-wide checks need.
const monitorGrant = "The user may not read server status: grant it the clusterMonitor role."

// slowOpAfter is how long an operation runs before it counts as long-running.
var slowOpAfter = time.Minute

// Health reads the server's health checks (docs/HEALTH.md): serverStatus,
// replSetGetStatus and currentOp need clusterMonitor; without it those
// checks come back unknown.
func Health(ctx context.Context, c *mongo.Client, db string) (*health.Report, error) {
	start := time.Now()
	srv, err := describe(ctx, c, db)
	if err != nil {
		return nil, err
	}
	r := &health.Report{Engine: "mongodb", Server: "MongoDB " + srv.Version, Checks: []health.Check{}}
	admin := c.Database("admin")
	unauthorized := func(err error) string {
		var ce mongo.CommandError
		if errors.As(err, &ce) && (ce.Code == 13 || ce.Name == "Unauthorized") {
			return monitorGrant
		}
		return err.Error()
	}

	var status struct {
		Connections struct {
			Current   int64 `bson:"current"`
			Available int64 `bson:"available"`
		} `bson:"connections"`
		WiredTiger struct {
			Cache map[string]any `bson:"cache"`
		} `bson:"wiredTiger"`
	}
	if err := admin.RunCommand(ctx, bson.D{{Key: "serverStatus", Value: 1}}).Decode(&status); err != nil {
		why := unauthorized(err)
		r.Unavailable("mongo.connections", "Connections", why)
		r.Unavailable("mongo.cache", "WiredTiger cache", why)
	} else {
		total := status.Connections.Current + status.Connections.Available
		r.Add(health.Check{ID: "mongo.connections", Title: "Connections", Status: health.Level(float64(status.Connections.Current)/float64(max(total, 1)), 0.8, 0.95),
			Value: fmt.Sprintf("%d of %d", status.Connections.Current, total),
			Fix:   "Lower the drivers' maxPoolSize, or raise net.maxIncomingConnections and the process's file limit"})
		used, maxBytes, dirty := num(status.WiredTiger.Cache["bytes currently in the cache"]), num(status.WiredTiger.Cache["maximum bytes configured"]),
			num(status.WiredTiger.Cache["tracked dirty bytes in the cache"])
		if maxBytes > 0 {
			dirtyShare := dirty / maxBytes
			c := health.Check{ID: "mongo.cache", Title: "WiredTiger cache", Status: health.Level(dirtyShare, 0.05, 0.2),
				Value:  fmt.Sprintf("%.0f%% full, %.1f%% dirty", used/maxBytes*100, dirtyShare*100),
				Detail: fmt.Sprintf("%s configured. Above 5%% dirty, eviction starts slowing writes; above 20%%, application threads evict too.", health.Bytes(int64(maxBytes))),
				Fix:    "Look for write bursts or slow disks; raise storage.wiredTiger.engineConfig.cacheSizeGB if memory allows"}
			r.Add(c)
		}
	}

	replication(ctx, admin, c, r, unauthorized)

	// Operations running for a long time.
	var ops struct {
		Inprog []struct {
			Secs int64  `bson:"secs_running"`
			Op   string `bson:"op"`
			NS   string `bson:"ns"`
		} `bson:"inprog"`
	}
	err = admin.RunCommand(ctx, bson.D{{Key: "currentOp", Value: 1}, {Key: "active", Value: true},
		{Key: "secs_running", Value: bson.D{{Key: "$gte", Value: int64(slowOpAfter.Seconds())}}}}).Decode(&ops)
	if err != nil {
		r.Unavailable("mongo.long_operations", "Long-running operations", unauthorized(err))
	} else {
		var names []string
		var longest int64
		for _, op := range ops.Inprog {
			if op.Op == "none" || strings.HasPrefix(op.NS, "admin.") || strings.HasPrefix(op.NS, "local.") {
				continue
			}
			longest = max(longest, op.Secs)
			names = append(names, fmt.Sprintf("%s on %s for %s", op.Op, op.NS, health.Duration(time.Duration(op.Secs)*time.Second)))
		}
		c := health.Check{ID: "mongo.long_operations", Title: "Long-running operations", Status: health.Level(float64(len(names)), 1, 5),
			Value: fmt.Sprintf("%d over %s", len(names), health.Duration(slowOpAfter)), Detail: strings.Join(names, "; "),
			Fix: "Find them with db.currentOp() and stop them with db.killOp(opid); set maxTimeMS on the queries"}
		r.Add(c)
	}

	r.TookMs = time.Since(start).Milliseconds()
	return r, nil
}

// replication reads replica set member states and lag, and the oplog window.
func replication(ctx context.Context, admin *mongo.Database, c *mongo.Client, r *health.Report, unauthorized func(error) string) {
	var rs struct {
		Members []struct {
			Name     string    `bson:"name"`
			StateStr string    `bson:"stateStr"`
			Health   float64   `bson:"health"`
			Optime   time.Time `bson:"optimeDate"`
		} `bson:"members"`
	}
	err := admin.RunCommand(ctx, bson.D{{Key: "replSetGetStatus", Value: 1}}).Decode(&rs)
	var ce mongo.CommandError
	switch {
	case err != nil && errors.As(err, &ce) && (ce.Code == 76 || ce.Name == "NoReplicationEnabled"):
		r.Add(health.Check{ID: "mongo.replication", Title: "Replication", Status: health.OK, Value: "standalone, not a replica set",
			Detail: "A standalone server has no copy to fail over to.", Fix: "Run production data on a replica set of at least three members"})
		return
	case err != nil:
		r.Unavailable("mongo.replication", "Replication", unauthorized(err))
		return
	}
	var primary time.Time
	for _, m := range rs.Members {
		if m.StateStr == "PRIMARY" {
			primary = m.Optime
		}
	}
	status, lagWorst := health.OK, time.Duration(0)
	var parts []string
	for _, m := range rs.Members {
		part := m.Name + " " + m.StateStr
		switch {
		case m.Health == 0 || m.StateStr != "PRIMARY" && m.StateStr != "SECONDARY" && m.StateStr != "ARBITER":
			status = health.Danger
		case m.StateStr == "SECONDARY" && !primary.IsZero():
			lag := primary.Sub(m.Optime)
			lagWorst = max(lagWorst, lag)
			part += ", " + health.Duration(lag) + " behind"
		}
		parts = append(parts, part)
	}
	if lagStatus := health.Level(lagWorst.Seconds(), 30, 300); status != health.Danger {
		status = lagStatus
	}
	r.Add(health.Check{ID: "mongo.replication", Title: "Replication", Status: status, Value: plural(len(rs.Members), "member"),
		Detail: strings.Join(parts, "; "), Fix: "rs.status() shows each member's state and last heartbeat message"})

	// The oplog window: how far back a secondary can fall and still catch up.
	local := c.Database("local").Collection("oplog.rs")
	var first, last struct {
		TS bson.Timestamp `bson:"ts"`
	}
	natural := func(dir int) *options.FindOneOptionsBuilder {
		return options.FindOne().SetSort(bson.D{{Key: "$natural", Value: dir}})
	}
	if err := local.FindOne(ctx, bson.D{}, natural(1)).Decode(&first); err != nil {
		r.Unavailable("mongo.oplog_window", "Oplog window", unauthorized(err))
		return
	}
	if err := local.FindOne(ctx, bson.D{}, natural(-1)).Decode(&last); err != nil {
		r.Unavailable("mongo.oplog_window", "Oplog window", unauthorized(err))
		return
	}
	window := time.Duration(last.TS.T-first.TS.T) * time.Second
	oc := health.Check{ID: "mongo.oplog_window", Title: "Oplog window", Status: health.Level(-window.Hours(), -24, -6),
		Value:  health.Duration(window),
		Detail: "A secondary down for longer than this cannot catch up and needs a full resync.",
		Fix:    "Raise the oplog size with replSetResizeOplog"}
	// Until the oplog fills, the window is as old as the replica set, not a limit.
	if cur, err := local.Aggregate(ctx, bson.A{bson.D{{Key: "$collStats", Value: bson.D{{Key: "storageStats", Value: bson.D{}}}}}}); err == nil {
		var row struct {
			StorageStats struct {
				Size    float64 `bson:"size"`
				MaxSize float64 `bson:"maxSize"`
			} `bson:"storageStats"`
		}
		if cur.Next(ctx) && cur.Decode(&row) == nil && row.StorageStats.MaxSize > 0 && row.StorageStats.Size < 0.9*row.StorageStats.MaxSize {
			oc.Status = health.OK
			oc.Value += fmt.Sprintf(" so far (the oplog is %.0f%% full)", row.StorageStats.Size/row.StorageStats.MaxSize*100)
			oc.Detail = "The window grows until the oplog fills; " + oc.Detail
		}
		cur.Close(ctx)
	}
	r.Add(oc)
}

// num reads a serverStatus number, which may arrive as int32, int64 or float64.
func num(v any) float64 {
	switch x := v.(type) {
	case int32:
		return float64(x)
	case int64:
		return float64(x)
	case float64:
		return x
	}
	return 0
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

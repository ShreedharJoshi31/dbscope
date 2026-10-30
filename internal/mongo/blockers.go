package mongo

import (
	"context"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"github.com/dbscope/dbscope/internal/migrate"
)

// exclusiveKinds are the calls that take an exclusive collection lock (an
// index build at its start and end), which waits for every operation and
// open transaction holding a lock on the collection.
var exclusiveKinds = []string{"createIndex", "ensureIndex", "createIndexes", "dropIndex", "dropIndexes", "drop",
	"renameCollection", "convertToCapped", "collMod"}

// lockTarget matches lockInfo's resource ids, e.g. "{55...: Collection, 89..., app.t}".
var lockTarget = regexp.MustCompile(`Collection, \d+, ([^}]+)\}$`)

// blockerReadTimeout bounds each of the two reads.
const blockerReadTimeout = 10 * time.Second

// ReadBlockers names the operations and open transactions on the collections
// the migration builds indexes on, drops, renames or modifies, and adds
// DBS-BLOCKED-NOW to each such statement. Active operations come from
// $currentOp; an idle transaction's collections come from lockInfo, since
// $currentOp does not say which collections it locked. Reading other users'
// operations needs the inprog privilege, and lockInfo the serverStatus one;
// without them it warns. It never fails the analysis.
func ReadBlockers(ctx context.Context, c *mongo.Client, db string, rep *migrate.Report) {
	var colls []string
	for _, s := range rep.Statements {
		if slices.Contains(exclusiveKinds, s.Kind) {
			for _, r := range s.Relations {
				if !slices.Contains(colls, r) {
					colls = append(colls, r)
				}
			}
		}
	}
	if len(colls) == 0 {
		return
	}
	admin := c.Database("admin", options.Database().SetReadPreference(readpref.Primary()))
	ops, err := currentOps(ctx, admin)
	if err != nil {
		rep.Warnings = append(rep.Warnings, "operations that would block the migration were not read ($currentOp of all users needs the inprog privilege): "+err.Error())
		return
	}
	held, lockErr := sessionLocks(ctx, admin)
	if lockErr != nil {
		var open []string
		for _, op := range ops {
			if op.idleTx {
				open = append(open, op.describe())
			}
		}
		msg := "lockInfo was not read, so open transactions cannot be tied to collections (it needs the serverStatus privilege): " + lockErr.Error()
		if len(open) > 0 {
			msg += "; these open transactions block an index build, drop or rename on any collection they have written: " + strings.Join(open, "; ")
		}
		rep.Warnings = append(rep.Warnings, msg)
	}
	now := time.Now().UTC()
	rep.BlockersAt = &now
	index := map[string]int{}
	for i := range rep.Statements {
		s := &rep.Statements[i]
		if !slices.Contains(exclusiveKinds, s.Kind) {
			continue
		}
		var named []string
		for _, coll := range s.Relations {
			ns := db + "." + coll
			for _, op := range ops {
				var lock string
				switch l := held[op.lsid][ns]; {
				case op.lsid != "" && l != "":
					lock = l
				case op.idleTx:
					continue
				case op.ns == ns && op.waiting:
					lock = "a collection lock (waiting)"
				case op.ns == ns && op.collLock != "":
					lock = op.collLock + " collection lock"
				default:
					continue
				}
				k := op.id + "\x00" + coll
				j, ok := index[k]
				if !ok {
					j = len(rep.Blockers)
					index[k] = j
					rep.Blockers = append(rep.Blockers, migrate.Blocker{Relation: coll, Session: op.id, User: op.user, State: op.state(),
						AgeSeconds: op.age, Query: op.query, Lock: lock})
				}
				desc := migrate.DescribeBlocker(rep.Blockers[j], op.who())
				if !slices.Contains(named, desc) {
					named = append(named, desc)
				}
			}
		}
		if len(named) == 0 {
			continue
		}
		// transactionLifetimeLimitSeconds (60 by default) aborts a transaction that stays open longer,
		// which also bounds how long the migration waits for one (MongoDB manual, "Production Considerations").
		migrate.MarkBlocked(rep, s, named, "; the exclusive collection lock waits for it, and operations on the collection that arrive later queue behind that request",
			"Wait for it to finish, or end it (db.killOp(opid) for an operation, killSessions for a transaction), and run the migration when no long transaction is open on the collection")
		s.Score = max(s.Score, 8)
	}
}

// op is one entry of $currentOp.
type op struct {
	id, user, ns, query, lsid, kind, collLock string
	idleTx, waiting                           bool
	age                                       float64
	opid                                      int64
}

func (o op) who() string {
	if o.idleTx {
		return "the open transaction of session " + o.lsid
	}
	return fmt.Sprintf("operation %d", o.opid)
}

func (o op) state() string {
	switch {
	case o.idleTx:
		return "idle in transaction"
	case o.waiting:
		return "running " + o.kind + ", waiting for a lock"
	}
	return "running " + o.kind
}

func (o op) describe() string {
	return migrate.DescribeBlocker(migrate.Blocker{Relation: "its collections", User: o.user, State: o.state(), AgeSeconds: o.age,
		Lock: "locks"}, o.who())
}

func currentOps(ctx context.Context, admin *mongo.Database) ([]op, error) {
	ctx, cancel := context.WithTimeout(ctx, blockerReadTimeout)
	defer cancel()
	cur, err := admin.Aggregate(ctx, bson.A{
		bson.D{{Key: "$currentOp", Value: bson.D{{Key: "allUsers", Value: true}, {Key: "idleConnections", Value: false}, {Key: "idleSessions", Value: true}}}},
		bson.D{{Key: "$match", Value: bson.D{{Key: "appName", Value: bson.D{{Key: "$not", Value: bson.Regex{Pattern: "^dbscope"}}}}}}},
		bson.D{{Key: "$limit", Value: 1000}},
	})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []op
	for cur.Next(ctx) {
		var raw struct {
			Type    string            `bson:"type"`
			Op      string            `bson:"op"`
			Opid    any               `bson:"opid"`
			NS      string            `bson:"ns"`
			Active  bool              `bson:"active"`
			Waiting bool              `bson:"waitingForLock"`
			Micros  int64             `bson:"microsecs_running"`
			Command bson.D            `bson:"command"`
			Locks   map[string]string `bson:"locks"`
			IsUser  *bool             `bson:"isFromUserConnection"`
			LSID    struct {
				ID bson.Binary `bson:"id"`
			} `bson:"lsid"`
			Users []struct {
				User string `bson:"user"`
			} `bson:"effectiveUsers"`
			Transaction *struct {
				TimeOpenMicros int64 `bson:"timeOpenMicros"`
			} `bson:"transaction"`
		}
		if err := cur.Decode(&raw); err != nil {
			return nil, err
		}
		if raw.IsUser != nil && !*raw.IsUser {
			continue // the server's own threads
		}
		o := op{ns: raw.NS, waiting: raw.Waiting, kind: raw.Op, age: float64(raw.Micros) / 1e6,
			collLock: raw.Locks["Collection"], lsid: uuidText(raw.LSID.ID)}
		if n, ok := raw.Opid.(int32); ok {
			o.opid = int64(n)
		} else if n, ok := raw.Opid.(int64); ok {
			o.opid = n
		}
		if len(raw.Users) > 0 {
			o.user = raw.Users[0].User
		}
		if raw.Transaction != nil {
			o.age = float64(raw.Transaction.TimeOpenMicros) / 1e6
		}
		switch {
		case raw.Type == "idleSession" && raw.Transaction != nil:
			o.idleTx = true
			o.id = "lsid " + o.lsid
		case raw.Type == "idleSession" || !raw.Active:
			continue
		default:
			o.id = fmt.Sprint(o.opid)
			o.query = opCommand(raw.Command)
		}
		out = append(out, o)
	}
	return out, cur.Err()
}

// opCommand renders an operation's command without the driver's session and cluster fields.
func opCommand(cmd bson.D) string {
	kept := bson.D{}
	for _, e := range cmd {
		if e.Key != "lsid" && e.Key != "txnNumber" && !strings.HasPrefix(e.Key, "$") {
			kept = append(kept, e)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	b, err := bson.MarshalExtJSON(kept, false, false)
	if err != nil {
		return ""
	}
	return migrate.TruncateQuery(string(b))
}

// sessionLocks reads lockInfo: per session id, per namespace, the
// collection lock it holds or waits for.
func sessionLocks(ctx context.Context, admin *mongo.Database) (map[string]map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, blockerReadTimeout)
	defer cancel()
	var res struct {
		LockInfo []struct {
			ResourceID string `bson:"resourceId"`
			Granted    []struct {
				Mode      string `bson:"mode"`
				DebugInfo string `bson:"debugInfo"`
			} `bson:"granted"`
			Pending []struct {
				Mode      string `bson:"mode"`
				DebugInfo string `bson:"debugInfo"`
			} `bson:"pending"`
		} `bson:"lockInfo"`
	}
	if err := admin.RunCommand(ctx, bson.D{{Key: "lockInfo", Value: 1}}).Decode(&res); err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	add := func(debug, mode, ns, suffix string) {
		lsid, ok := strings.CutPrefix(debug, "lsid: ")
		if !ok {
			return
		}
		lsid, _, _ = strings.Cut(lsid, ",")
		if out[lsid] == nil {
			out[lsid] = map[string]string{}
		}
		if out[lsid][ns] == "" {
			out[lsid][ns] = mode + " collection lock" + suffix
		}
	}
	for _, l := range res.LockInfo {
		m := lockTarget.FindStringSubmatch(l.ResourceID)
		if m == nil {
			continue
		}
		for _, g := range l.Granted {
			add(g.DebugInfo, g.Mode, m[1], "")
		}
		for _, p := range l.Pending {
			add(p.DebugInfo, p.Mode, m[1], " (waiting)")
		}
	}
	return out, nil
}

// uuidText renders a UUID session id as lockInfo prints it.
func uuidText(b bson.Binary) string {
	if len(b.Data) != 16 {
		return ""
	}
	h := hex.EncodeToString(b.Data)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

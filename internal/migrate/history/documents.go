package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	drv "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/dbscope/dbscope/internal/elastic"
	"github.com/dbscope/dbscope/internal/migrate/ingest"
)

// changelog is migrate-mongo's default changelogCollectionName.
const changelog = "changelog"

// Mongo reads migrate-mongo's changelog collection: one document per applied
// file ({fileName, appliedAt}, plus fileHash, the SHA-256 of the file, when
// useFileHash is on), as lib/actions/status.js matches them.
func Mongo(ctx context.Context, db *mongo.Database, files []ingest.Input) Result {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	names, err := db.ListCollectionNames(ctx, bson.D{{Key: "name", Value: changelog}})
	if err != nil {
		return readFailed("the "+changelog+" collection", err)
	}
	if len(names) == 0 {
		return notFound("migrate-mongo's " + changelog + " collection")
	}
	cur, err := db.Collection(changelog).Find(ctx, bson.D{},
		options.Find().SetProjection(bson.D{{Key: "fileName", Value: 1}, {Key: "fileHash", Value: 1}}).SetLimit(maxRows))
	if err != nil {
		return readFailed(changelog, err)
	}
	var docs []struct {
		FileName string `bson:"fileName"`
		FileHash string `bson:"fileHash"`
	}
	if err := cur.All(ctx, &docs); err != nil {
		return readFailed(changelog, err)
	}
	hashes := map[string][]string{}
	for _, d := range docs {
		hashes[d.FileName] = append(hashes[d.FileName], d.FileHash)
	}
	return classify("migrate-mongo", changelog, files, func(f ingest.Input) state {
		b := base(f.Name)
		if !strings.HasSuffix(b, ".js") {
			return other
		}
		got, ok := hashes[b]
		if !ok {
			return pending
		}
		sum := sha256Hex(f.SQL)
		for _, h := range got {
			if h == "" || h == sum {
				return applied
			}
		}
		return changed
	})
}

// Neo4j reads neo4j-migrations' __Neo4jMigration nodes in the profile's
// database (the default schema database). Each applied migration is a node
// with its version (underscores shown as dots) and checksum, chained from a
// version 'BASELINE' node; migrations of another target database carry
// migrationTarget and are left out (ac.simons.neo4j.migrations.core.ChainBuilder).
func Neo4j(ctx context.Context, d drv.Driver, database string, files []ingest.Input) Result {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	s := d.NewSession(ctx, drv.SessionConfig{AccessMode: drv.AccessModeRead, DatabaseName: database})
	defer s.Close(ctx)
	res, err := s.ExecuteRead(ctx, func(tx drv.ManagedTransaction) (any, error) {
		r, err := tx.Run(ctx, `MATCH (n:__Neo4jMigration) WHERE n.version <> 'BASELINE' AND n.migrationTarget IS NULL
			RETURN toString(n.version) AS version, toString(n.checksum) AS checksum LIMIT $limit`, map[string]any{"limit": maxRows})
		if err != nil {
			return nil, err
		}
		return r.Collect(ctx)
	}, drv.WithTxTimeout(DefaultTimeout))
	if err != nil {
		return readFailed("the __Neo4jMigration nodes", err)
	}
	recs := res.([]*drv.Record)
	if len(recs) == 0 {
		return notFound("neo4j-migrations' __Neo4jMigration nodes")
	}
	sums := map[string]string{}
	for _, r := range recs {
		v, _ := r.Get("version")
		c, _ := r.Get("checksum")
		vs, _ := v.(string)
		cs, _ := c.(string)
		sums[version(vs)] = cs
	}
	return classify("neo4j-migrations", "__Neo4jMigration", files, func(f ingest.Input) state {
		m := neo4jV.FindStringSubmatch(base(f.Name))
		if m == nil {
			return other
		}
		sum, ok := sums[version(m[1])]
		switch {
		case !ok:
			return pending
		// Either checksum mode: the default, or useFlywayCompatibleChecksums.
		case sum == "" || sum == Neo4jChecksum(f.SQL) || sum == fmt.Sprint(FlywayChecksum(f.SQL)):
			return applied
		}
		return changed
	})
}

// evolutionIndex is elasticsearch-evolution's default historyIndex.
const evolutionIndex = "es_evolution"

// Elastic reads elasticsearch-evolution's history index: one document per
// script run, id and "version" the script's version, with "scriptName",
// "checksum" and "success"; versions 0.x are its own (the lock document is
// 0.1) and are left out, as HistoryRepositoryImpl.findAll does. The checksum
// is not compared: elasticsearch-evolution derives it from a Java hashCode
// that includes the identity hash of a StringBuilder, so it cannot be
// recomputed outside that JVM.
func Elastic(ctx context.Context, c *elastic.Client, files []ingest.Input) Result {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	// 10000 is Elasticsearch's default index.max_result_window, the most one search returns.
	docs, err := c.Sources(ctx, evolutionIndex, 10000)
	var e *elastic.Error
	switch {
	case errors.As(err, &e) && e.Status == 404:
		return notFound("elasticsearch-evolution's " + evolutionIndex + " index")
	case err != nil:
		return readFailed("the "+evolutionIndex+" index", err)
	}
	ok := map[string]bool{}
	for _, raw := range docs {
		var d struct {
			Version json.RawMessage `json:"version"`
			Success bool            `json:"success"`
		}
		if json.Unmarshal(raw, &d) != nil {
			continue
		}
		v := version(strings.Trim(string(d.Version), `"`))
		if d.Success && cmpVersion(v, "1") >= 0 {
			ok[v] = true
		}
	}
	return classify("elasticsearch-evolution", evolutionIndex, files, func(f ingest.Input) state {
		m := evolutionV.FindStringSubmatch(base(f.Name))
		if m == nil {
			return other
		}
		if ok[version(m[1])] {
			return applied
		}
		return pending
	})
}

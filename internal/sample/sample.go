// Package sample embeds a realistic demo schema so the UI can show an ERD
// without a database. model.json is the introspect output for sample.sql,
// regenerated with: go test ./internal/sample -run TestModelJSON -update
package sample

import (
	_ "embed"
	"encoding/json"
	"time"

	"github.com/dbscope/dbscope/internal/schema"
)

//go:embed sample.sql
var SQL string

//go:embed model.json
var modelJSON []byte

// Schemas are the schemas sample.sql creates.
var Schemas = []string{"app", "billing", "shop"}

// ServerVersion is what the sample model reports, independent of the server
// that generated it.
const ServerVersion = 170000

// IntrospectedAt is fixed so regenerating model.json only changes it when
// the schema changes.
var IntrospectedAt = time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)

// Model returns a fresh copy of the sample model, without inferred keys.
func Model() (*schema.Model, error) {
	var m schema.Model
	if err := json.Unmarshal(modelJSON, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Normalize makes an introspected model reproducible: it pins the version
// and timestamp fields that vary between runs and servers.
func Normalize(m *schema.Model) {
	m.ServerVersion = ServerVersion
	m.IntrospectedAt = IntrospectedAt
	m.DurationMs = 0
}

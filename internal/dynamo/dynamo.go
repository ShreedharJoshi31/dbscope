// Package dynamo reads Amazon DynamoDB for dbscope (docs/ENGINES.md): a map
// of tables, keys and indexes with attributes inferred from a small sample,
// and read-only PartiQL queries whose plan says whether they scan the table.
package dynamo

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/smithy-go"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/schema"
)

// Connect builds a client from the profile's options: region, an optional
// AWS profile name, and an optional endpoint (DynamoDB Local). Credentials
// come from the SDK's chain (environment, shared config, SSO); dbscope keeps
// none. For a local endpoint without credentials, dummy ones are used, as
// DynamoDB Local accepts any.
func Connect(ctx context.Context, p conn.Profile) (*dynamodb.Client, error) {
	q, err := url.ParseQuery(p.Options)
	if err != nil {
		return nil, err
	}
	opts := []func(*config.LoadOptions) error{
		config.WithRegion(q.Get("region")),
		config.WithRetryMaxAttempts(2), // fewer than the default 3: a throttled read is a finding, not something to hammer
	}
	if prof := q.Get("profile"); prof != "" {
		opts = append(opts, config.WithSharedConfigProfile(prof))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, err
	}
	endpoint := q.Get("endpoint")
	if endpoint != "" && isLocal(endpoint) {
		if _, err := cfg.Credentials.Retrieve(ctx); err != nil {
			cfg.Credentials = aws.NewCredentialsCache(aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{AccessKeyID: "dbscope", SecretAccessKey: "dbscope", Source: "dbscope-local"}, nil
			}))
		}
	}
	return dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
	}), nil
}

func isLocal(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == "localhost" || h == "127.0.0.1" || h == "::1" || strings.HasSuffix(h, ".localhost")
}

// Test lists tables to prove the credentials and region work.
func Test(ctx context.Context, p conn.Profile) conn.TestResult {
	res := conn.TestResult{Engine: conn.EngineDynamoDB, Warnings: []string{}}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, err := Connect(ctx, p)
	if err != nil {
		res.Error = &conn.TestError{Kind: "auth", Message: err.Error()}
		return res
	}
	start := time.Now()
	if _, err := c.ListTables(ctx, &dynamodb.ListTablesInput{Limit: aws.Int32(1)}); err != nil {
		res.Error = classify(err)
		return res
	}
	ms := time.Since(start).Milliseconds()
	res.OK, res.LatencyMs = true, &ms
	q, _ := url.ParseQuery(p.Options)
	if q.Get("profile") != "" && os.Getenv("AWS_ACCESS_KEY_ID") != "" {
		res.Warnings = append(res.Warnings, "AWS_ACCESS_KEY_ID is set in dbscope's environment, so those credentials are used instead of the AWS profile "+q.Get("profile"))
	}
	if q.Get("endpoint") == "" {
		for _, v := range []string{"AWS_ENDPOINT_URL_DYNAMODB", "AWS_ENDPOINT_URL"} {
			if e := os.Getenv(v); e != "" {
				res.Warnings = append(res.Warnings, v+" is set in dbscope's environment, so requests go to "+e+" rather than AWS")
				break
			}
		}
	}
	res.ServerVersion = "region " + q.Get("region")
	if e := q.Get("endpoint"); e != "" {
		res.ServerVersion = "endpoint " + e
	}
	return res
}

func classify(err error) *conn.TestError {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		kind := "database"
		switch ae.ErrorCode() {
		case "UnrecognizedClientException", "InvalidSignatureException", "AccessDeniedException", "ExpiredTokenException":
			kind = "auth"
		}
		return &conn.TestError{Kind: kind, Message: ae.ErrorMessage(), SQLState: ae.ErrorCode()}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &conn.TestError{Kind: "timeout", Message: err.Error()}
	}
	return conn.Classify(err)
}

// SampleItems is how many items per table attributes are inferred from;
// sampling stops early at sampleRCU consumed read units.
const (
	SampleItems = 100
	sampleRCU   = 25
)

// Introspect maps every table: its key schema as the primary key, its
// secondary indexes, and the attributes a small eventually consistent Scan
// sees. Item counts and sizes are DynamoDB's, refreshed about every six hours.
func Introspect(ctx context.Context, c *dynamodb.Client) (*schema.Model, error) {
	start := time.Now()
	var names []string
	p := dynamodb.NewListTablesPaginator(c, &dynamodb.ListTablesInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		names = append(names, page.TableNames...)
	}
	sort.Strings(names)
	m := &schema.Model{Engine: "dynamodb", Schemas: []string{"tables"}, Relations: []schema.Relation{}, Enums: []schema.Enum{}}
	for _, n := range names {
		d, err := c.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(n)})
		if err != nil {
			return nil, fmt.Errorf("describe %s: %w", n, err)
		}
		r := relation(d.Table)
		ttl, err := c.DescribeTimeToLive(ctx, &dynamodb.DescribeTimeToLiveInput{TableName: aws.String(n)})
		if err == nil && ttl.TimeToLiveDescription != nil && ttl.TimeToLiveDescription.TimeToLiveStatus == types.TimeToLiveStatusEnabled {
			markTTL(&r, aws.ToString(ttl.TimeToLiveDescription.AttributeName))
		}
		if d.Table.TableStatus == types.TableStatusActive {
			items, err := sample(ctx, c, n)
			if err != nil {
				return nil, fmt.Errorf("sampling %s: %w", n, err)
			}
			addAttributes(&r, items)
		}
		m.Relations = append(m.Relations, r)
	}
	m.IntrospectedAt = time.Now().UTC()
	m.DurationMs = time.Since(start).Milliseconds()
	return m, nil
}

var scalarName = map[types.ScalarAttributeType]string{types.ScalarAttributeTypeS: "string", types.ScalarAttributeTypeN: "number", types.ScalarAttributeTypeB: "binary"}

func relation(t *types.TableDescription) schema.Relation {
	r := schema.Relation{Schema: "tables", Name: aws.ToString(t.TableName), Kind: schema.KindTable}
	if t.ItemCount != nil {
		n := *t.ItemCount
		r.RowEstimate = &n
	}
	defs := map[string]string{}
	for _, a := range t.AttributeDefinitions {
		defs[aws.ToString(a.AttributeName)] = scalarName[a.AttributeType]
	}
	keyCols := func(ks []types.KeySchemaElement) []string {
		var out []string
		for _, k := range ks {
			out = append(out, aws.ToString(k.AttributeName))
		}
		return out
	}
	for _, k := range t.KeySchema {
		name := aws.ToString(k.AttributeName)
		role := "partition key"
		if k.KeyType == types.KeyTypeRange {
			role = "sort key"
		}
		r.Columns = append(r.Columns, schema.Column{Name: name, Type: defs[name], Comment: role})
		r.PrimaryKey = append(r.PrimaryKey, name)
	}
	billing := "provisioned"
	if t.BillingModeSummary != nil && t.BillingModeSummary.BillingMode == types.BillingModePayPerRequest {
		billing = "on-demand"
	}
	r.Comment = billing
	if t.TableSizeBytes != nil {
		r.Comment += fmt.Sprintf(", %d bytes", *t.TableSizeBytes)
	}
	for _, g := range t.GlobalSecondaryIndexes {
		r.Indexes = append(r.Indexes, schema.Index{Name: aws.ToString(g.IndexName), Columns: keyCols(g.KeySchema), Method: "gsi " + projection(g.Projection), Valid: g.IndexStatus == types.IndexStatusActive})
		addKeyColumns(&r, g.KeySchema, defs)
	}
	for _, l := range t.LocalSecondaryIndexes {
		r.Indexes = append(r.Indexes, schema.Index{Name: aws.ToString(l.IndexName), Columns: keyCols(l.KeySchema), Method: "lsi " + projection(l.Projection), Valid: true})
		addKeyColumns(&r, l.KeySchema, defs)
	}
	return r
}

func projection(p *types.Projection) string {
	if p == nil {
		return ""
	}
	s := strings.ToLower(string(p.ProjectionType))
	if len(p.NonKeyAttributes) > 0 {
		s += " " + strings.Join(p.NonKeyAttributes, ",")
	}
	return s
}

func addKeyColumns(r *schema.Relation, ks []types.KeySchemaElement, defs map[string]string) {
	for _, k := range ks {
		name := aws.ToString(k.AttributeName)
		if r.Column(name) == nil {
			r.Columns = append(r.Columns, schema.Column{Name: name, Type: defs[name], Nullable: true, Comment: "index key"})
		}
	}
}

func markTTL(r *schema.Relation, attr string) {
	if c := r.Column(attr); c != nil {
		c.Comment = strings.TrimPrefix(c.Comment+"; TTL: items expire at this epoch-seconds time", "; ")
		return
	}
	r.Columns = append(r.Columns, schema.Column{Name: attr, Type: "number", Nullable: true, Comment: "TTL: items expire at this epoch-seconds time"})
}

// sample reads up to SampleItems items, eventually consistent, stopping at
// sampleRCU consumed read units.
func sample(ctx context.Context, c *dynamodb.Client, table string) ([]map[string]types.AttributeValue, error) {
	var items []map[string]types.AttributeValue
	var start map[string]types.AttributeValue
	used := 0.0
	for len(items) < SampleItems && used < sampleRCU {
		out, err := c.Scan(ctx, &dynamodb.ScanInput{TableName: aws.String(table), Limit: aws.Int32(int32(SampleItems - len(items))),
			ExclusiveStartKey: start, ReturnConsumedCapacity: types.ReturnConsumedCapacityTotal})
		if err != nil {
			return nil, err
		}
		items = append(items, out.Items...)
		if out.ConsumedCapacity != nil && out.ConsumedCapacity.CapacityUnits != nil {
			used += *out.ConsumedCapacity.CapacityUnits
		}
		if out.LastEvaluatedKey == nil {
			break
		}
		start = out.LastEvaluatedKey
	}
	return items, nil
}

var typeName = func(v types.AttributeValue) string {
	switch v.(type) {
	case *types.AttributeValueMemberS:
		return "string"
	case *types.AttributeValueMemberN:
		return "number"
	case *types.AttributeValueMemberB:
		return "binary"
	case *types.AttributeValueMemberBOOL:
		return "bool"
	case *types.AttributeValueMemberNULL:
		return "null"
	case *types.AttributeValueMemberM:
		return "map"
	case *types.AttributeValueMemberL:
		return "list"
	case *types.AttributeValueMemberSS:
		return "string set"
	case *types.AttributeValueMemberNS:
		return "number set"
	case *types.AttributeValueMemberBS:
		return "binary set"
	}
	return "unknown"
}

// addAttributes adds the non-key attributes the sample saw, with their type
// mix and coverage, the way the MongoDB map shows fields.
func addAttributes(r *schema.Relation, items []map[string]types.AttributeValue) {
	type stat struct {
		types   map[string]int
		present int
		order   int
	}
	stats := map[string]*stat{}
	for _, it := range items {
		keys := make([]string, 0, len(it))
		for k := range it {
			keys = append(keys, k)
		}
		sort.Strings(keys) // DynamoDB items are unordered
		for _, k := range keys {
			s := stats[k]
			if s == nil {
				s = &stat{types: map[string]int{}, order: len(stats)}
				stats[k] = s
			}
			s.present++
			s.types[typeName(it[k])]++
		}
	}
	names := make([]string, 0, len(stats))
	for k := range stats {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool { return stats[names[i]].order < stats[names[j]].order })
	for _, k := range names {
		s := stats[k]
		var parts []string
		for t, n := range s.types {
			if len(s.types) == 1 {
				parts = append(parts, t)
			} else {
				parts = append(parts, fmt.Sprintf("%s %d%%", t, (n*100+s.present/2)/s.present))
			}
		}
		sort.Strings(parts)
		cov := (s.present*100 + len(items)/2) / len(items)
		if c := r.Column(k); c != nil {
			if c.Type == "" {
				c.Type = strings.Join(parts, " | ")
			}
			continue
		}
		col := schema.Column{Name: k, Type: strings.Join(parts, " | "), Nullable: cov < 100}
		if cov < 100 {
			col.Comment = fmt.Sprintf("in %d%% of %d sampled items", cov, len(items))
		}
		r.Columns = append(r.Columns, col)
	}
}

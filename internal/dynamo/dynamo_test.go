package dynamo

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/query/guard"
	"github.com/dbscope/dbscope/internal/schema"
)

func TestCheck(t *testing.T) {
	for q, want := range map[string]guard.Reason{
		`SELECT * FROM "orders"`:                        "",
		`select id from orders where id = 'a';`:         "",
		`DELETE FROM "orders" WHERE id = 'a'`:           guard.NotSelect,
		`INSERT INTO "orders" VALUE {'id': 'a'}`:        guard.NotSelect,
		`SELECT * FROM a; DELETE FROM a WHERE id = 'x'`: guard.MultipleStatements,
		`SELECT * FROM "a;b"`:                           "",
	} {
		err := Check(q)
		var ge *guard.Error
		if want == "" {
			if err != nil {
				t.Errorf("%s: %v", q, err)
			}
		} else if !errors.As(err, &ge) || ge.Reason != want {
			t.Errorf("%s: %v, want %s", q, err, want)
		}
	}
}

func TestDynamo(t *testing.T) {
	p := mytest.Dynamo(t)
	ctx := context.Background()
	c, err := Connect(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	s := func(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }
	n := func(v string) types.AttributeValue { return &types.AttributeValueMemberN{Value: v} }
	_, err = c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String("orders"), BillingMode: types.BillingModePayPerRequest,
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("customer"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("placed"), AttributeType: types.ScalarAttributeTypeN}, {AttributeName: aws.String("status"), AttributeType: types.ScalarAttributeTypeS}},
		KeySchema: []types.KeySchemaElement{{AttributeName: aws.String("customer"), KeyType: types.KeyTypeHash}, {AttributeName: aws.String("placed"), KeyType: types.KeyTypeRange}},
		GlobalSecondaryIndexes: []types.GlobalSecondaryIndex{{IndexName: aws.String("by_status"),
			KeySchema:  []types.KeySchemaElement{{AttributeName: aws.String("status"), KeyType: types.KeyTypeHash}},
			Projection: &types.Projection{ProjectionType: types.ProjectionTypeKeysOnly}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		item := map[string]types.AttributeValue{"customer": s(fmt.Sprintf("c%d", i%5)), "placed": n(fmt.Sprint(1000 + i)), "status": s([]string{"new", "paid"}[i%2]), "total": n(fmt.Sprintf("%d.5", i))}
		if i%4 == 0 {
			item["coupon"] = s("X")
		}
		if _, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String("orders"), Item: item}); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("introspect", func(t *testing.T) {
		m, err := Introspect(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		o := m.Relation(schema.QualifiedName{Schema: "tables", Name: "orders"})
		if o == nil || !slices.Equal(o.PrimaryKey, []string{"customer", "placed"}) {
			t.Fatalf("orders %+v", o)
		}
		if c := o.Column("placed"); c.Type != "number" || c.Comment != "sort key" {
			t.Errorf("sort key %+v", c)
		}
		if c := o.Column("coupon"); c == nil || !c.Nullable || !strings.HasPrefix(c.Comment, "in 25%") {
			t.Errorf("coupon %+v", c)
		}
		if len(o.Indexes) != 1 || o.Indexes[0].Name != "by_status" || o.Indexes[0].Method != "gsi keys_only" {
			t.Errorf("indexes %+v", o.Indexes)
		}
	})

	t.Run("run", func(t *testing.T) {
		res, err := Run(ctx, c, `SELECT * FROM "orders" WHERE customer = 'c1'`, 3, false, 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if res.RowCount != 3 || !res.Truncated || res.Plan.Children[0].NodeType != "Query (partition key customer)" {
			t.Errorf("key query %+v %+v", res, res.Plan.Children[0])
		}
		res, err = Run(ctx, c, `SELECT customer FROM "orders" WHERE total > 30`, 100, false, 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if res.RowCount != 10 || res.Plan.Children[0].NodeType != "Scan" || len(res.Plan.Children[0].Flags) != 1 {
			t.Errorf("scan %d rows %+v", res.RowCount, res.Plan.Children[0])
		}
		res, err = Run(ctx, c, `SELECT * FROM "orders"."by_status" WHERE status = 'paid'`, 100, true, 10*time.Second)
		if err != nil || res.Plan.Children[0].Index != "by_status" || res.Plan.Children[0].NodeType != "Query (partition key status)" {
			t.Errorf("index plan %+v %v", res, err)
		}
	})

	t.Run("multi-attribute partition key", func(t *testing.T) {
		_, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
			TableName: aws.String("events"), BillingMode: types.BillingModePayPerRequest,
			AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: types.ScalarAttributeTypeS},
				{AttributeName: aws.String("tenant"), AttributeType: types.ScalarAttributeTypeS}, {AttributeName: aws.String("kind"), AttributeType: types.ScalarAttributeTypeS}},
			KeySchema: []types.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: types.KeyTypeHash}},
			GlobalSecondaryIndexes: []types.GlobalSecondaryIndex{{IndexName: aws.String("by_tenant_kind"),
				KeySchema:  []types.KeySchemaElement{{AttributeName: aws.String("tenant"), KeyType: types.KeyTypeHash}, {AttributeName: aws.String("kind"), KeyType: types.KeyTypeHash}},
				Projection: &types.Projection{ProjectionType: types.ProjectionTypeAll}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		res, err := Run(ctx, c, `SELECT * FROM "events"."by_tenant_kind" WHERE tenant = 'a'`, 100, true, 10*time.Second)
		if err != nil || res.Plan.Children[0].NodeType != "Scan" {
			t.Errorf("one of two partition-key attributes %+v %v", res, err)
		}
		res, err = Run(ctx, c, `SELECT * FROM "events"."by_tenant_kind" WHERE tenant = 'a' AND kind = 'login'`, 100, true, 10*time.Second)
		if err != nil || res.Plan.Children[0].NodeType != "Query (partition key tenant, kind)" {
			t.Errorf("both partition-key attributes %+v %v", res, err)
		}
	})

	if res := Test(ctx, p); !res.OK {
		t.Errorf("test %+v %+v", res, res.Error)
	}
}

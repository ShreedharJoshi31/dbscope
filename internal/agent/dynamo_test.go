package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/dbscope/dbscope/internal/conn"
	"github.com/dbscope/dbscope/internal/dynamo"
	"github.com/dbscope/dbscope/internal/mytest"
	"github.com/dbscope/dbscope/internal/schema"
)

func TestDynamoProfile(t *testing.T) {
	srv := mytest.Dynamo(t)
	ctx := context.Background()
	c, err := dynamo.Connect(ctx, srv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{TableName: aws.String("users"), BillingMode: types.BillingModePayPerRequest,
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: types.ScalarAttributeTypeS}},
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: types.KeyTypeHash}}}); err != nil {
		t.Fatal(err)
	}
	c.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String("users"), Item: map[string]types.AttributeValue{
		"id": &types.AttributeValueMemberS{Value: "u1"}, "email": &types.AttributeValueMemberS{Value: "a@x"}}})

	e := keyringEnv(t)
	body, _ := json.Marshal(map[string]any{"name": "Dyn", "engine": "dynamodb", "options": srv.Options, "env": "local"})
	w := e.do("POST", "/api/profiles", string(body))
	expect(t, w, 201, "")
	var p conn.Profile
	json.Unmarshal(w.Body.Bytes(), &p)
	if p.Host != "" || !p.Capabilities.Query {
		t.Fatalf("created %s", w.Body.String())
	}
	w = e.do("POST", "/api/profiles/"+p.ID+"/test", "")
	if !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("test %s", w.Body.String())
	}
	w = e.do("GET", "/api/profiles/"+p.ID+"/schema", "")
	expect(t, w, 200, "")
	var m schema.Model
	json.Unmarshal(w.Body.Bytes(), &m)
	if m.Engine != "dynamodb" || len(m.Relations) != 1 || m.Relations[0].Column("email") == nil {
		t.Fatalf("schema %s", w.Body.String())
	}
	w = e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"SELECT * FROM \"users\" WHERE id = 'u1'"}`)
	expect(t, w, 200, "")
	if !strings.Contains(w.Body.String(), `"a@x"`) {
		t.Errorf("query %s", w.Body.String())
	}
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"DELETE FROM \"users\" WHERE id = 'u1'"}`), 422, "rejected")
	expect(t, e.do("POST", "/api/profiles/"+p.ID+"/query", `{"sql":"SELECT * FROM \"nope\""}`), 400, "database")
	bad, _ := json.Marshal(map[string]any{"name": "X", "engine": "dynamodb", "options": "endpoint=http://localhost:8000", "env": "local"})
	expect(t, e.do("POST", "/api/profiles", string(bad)), 400, "validation")
}

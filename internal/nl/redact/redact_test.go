package redact

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestLiterals(t *testing.T) {
	for in, want := range map[string]string{
		`invalid input syntax for type integer: "hunter2"`:                `invalid input syntax for type integer: <redacted>`,
		`invalid input syntax for type integer: "a"b"c"`:                  `invalid input syntax for type integer: <redacted>`,
		`column "x" of relation "y" does not exist`:                       `column <redacted> does not exist`,
		`value 'it''s' is bad and "q" too`:                                `value <redacted> is bad and <redacted> too`,
		`unterminated "secret value`:                                      `unterminated <redacted>`,
		`invalid byte sequence for encoding "UTF8": 0xe2 0x80`:            `invalid byte sequence for encoding <redacted>: <redacted>`,
		`division by zero`:                                                `division by zero`,
		`date/time field value out of range: "2026-13-45"`:                `date/time field value out of range: <redacted>`,
		`invalid input value for enum mood: "very secret"`:                `invalid input value for enum mood: <redacted>`,
		`value "123456789012345" is out of range for type integer`:        `value <redacted> is out of range for type integer`,
		`malformed array literal: "{1,2"`:                                 `malformed array literal: <redacted>`,
		`operator does not exist: integer = text`:                         `operator does not exist: integer = text`,
		`relation "public.users" does not exist`:                          `relation <redacted> does not exist`,
		`function lower(integer) does not exist`:                          `function lower(integer) does not exist`,
		`invalid input syntax for type uuid: "4f1c' OR '1'='1"`:           `invalid input syntax for type uuid: <redacted>`,
		`could not parse "abc" as type json: Token "abc" is invalid`:      `could not parse <redacted> is invalid`,
		`new row for relation "t" violates check constraint "c_positive"`: `new row for relation <redacted>`,
	} {
		if got := Literals(in); got != want {
			t.Errorf("Literals(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

// TestCastSecret is the documented leak: casting a secret column to int
// puts the value in the message and the DETAIL.
func TestCastSecret(t *testing.T) {
	const secret = "sk-live-4242-very-secret"
	e := &pgconn.PgError{
		Severity: "ERROR", Code: "22P02",
		Message:       `invalid input syntax for type integer: "` + secret + `"`,
		Detail:        "Failing row contains (" + secret + ").",
		Hint:          `Value "` + secret + `" is not numeric.`,
		Where:         "SQL function \"f\" statement 1: " + secret,
		InternalQuery: "SELECT '" + secret + "'::int",
		Position:      40,
	}
	got := PgError(e, 8)
	if strings.Contains(got, secret) || strings.Contains(got, "4242") {
		t.Fatalf("leaked: %s", got)
	}
	want := `SQLSTATE 22P02: invalid input syntax for type integer: <redacted> (hint: Value <redacted> is not numeric.) at character 8 of the executed SQL`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if got := Error("42703", `column "nosuch" does not exist`, "", 0); got != "SQLSTATE 42703: column <redacted> does not exist" {
		t.Fatal(got)
	}
}

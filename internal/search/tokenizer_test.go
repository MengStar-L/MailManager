package search

import (
	"reflect"
	"strings"
	"testing"
)

func TestTokensGeneratesCJKOneTwoThreeGrams(t *testing.T) {
	got := Tokens("邮件管 Mail-Box")
	want := []string{"邮", "件", "管", "邮件", "件管", "邮件管", "mail", "box"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokens() = %#v, want %#v", got, want)
	}
}

func TestMatchQueryEscapesOperators(t *testing.T) {
	query := MatchQuery(`from:alice OR "secret" 邮件`)
	if strings.Contains(query, " OR ") || strings.Contains(query, "from:") {
		t.Fatalf("FTS operator leaked into query: %s", query)
	}
	for _, token := range []string{`"from"`, `"alice"`, `"邮"`, `"件"`, `"邮件"`} {
		if !strings.Contains(query, token) {
			t.Fatalf("query %q is missing %q", query, token)
		}
	}
}

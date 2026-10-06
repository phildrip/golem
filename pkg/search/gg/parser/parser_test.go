package parser

import (
	"testing"
)

func TestParseQuery(t *testing.T) {
	tests := []struct {
		input       string
		wantFuzzy   string
		wantLenCons int
	}{
		{"", "", 0},
		{"   ", "", 0},
		{"hello", "hello", 0},
		{"hello world", "hello world", 0},
		{"name *.rs", "name", 1},
		{"*.rs", "", 1},
		{"**/*.go foo", "foo", 1},
		{"!test cmd/ server", "server", 2},
		{"/src/ foo", "foo", 1},
		{"pkg/search/gg/parser.go", "", 1},
		{"agent !test/ *.json config", "agent config", 2},
		{"src name *.rs !test /lib/", "src name", 3},
		{"*.rs *.toml !test", "", 3},
	}

	for _, tt := range tests {
		q := Parse(tt.input)
		if q.FuzzyText != tt.wantFuzzy {
			t.Errorf("Parse(%q).FuzzyText = %q, want %q", tt.input, q.FuzzyText, tt.wantFuzzy)
		}
		if len(q.Constraints) != tt.wantLenCons {
			t.Errorf("Parse(%q).Constraints len = %d, want %d", tt.input, len(q.Constraints), tt.wantLenCons)
		}
	}
}

func TestQueryMatches(t *testing.T) {
	q := Parse("*.go !test cmd/")

	if !q.Matches("cmd/server/main.go") {
		t.Errorf("expected cmd/server/main.go to match")
	}
	if q.Matches("cmd/server/main_test.go") {
		t.Errorf("expected cmd/server/main_test.go to be excluded by !test")
	}
	if q.Matches("pkg/server/main.go") {
		t.Errorf("expected pkg/server/main.go to fail cmd/ filter")
	}
	if q.Matches("cmd/server/readme.md") {
		t.Errorf("expected cmd/server/readme.md to fail *.go filter")
	}
}

func TestPathSegmentMatching(t *testing.T) {
	q := Parse("/src/ *.go")

	if !q.Matches("src/app.go") {
		t.Errorf("expected src/app.go to match /src/")
	}
	if !q.Matches("pkg/src/app.go") {
		t.Errorf("expected pkg/src/app.go to match /src/")
	}
	// "mysrc" should not match directory segment "src"
	if q.Matches("mysrc/app.go") {
		t.Errorf("expected mysrc/app.go to be rejected by /src/")
	}
}

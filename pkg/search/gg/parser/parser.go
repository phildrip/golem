package parser

import (
	"strings"
)

// ConstraintType identifies the type of query constraint.
type ConstraintType int

const (
	ConstraintText ConstraintType = iota
	ConstraintExtension
	ConstraintGlob
	ConstraintPathSegment
	ConstraintFilePath
	ConstraintExclude
)

// Constraint represents an extracted query constraint.
type Constraint struct {
	Type  ConstraintType
	Value string
}

// Query represents a parsed query containing structured constraints and a cleaned fuzzy search text.
type Query struct {
	Raw         string
	FuzzyText   string
	Constraints []Constraint
}

// Parse extracts constraints (globs, extensions, directory paths, exclusions) and returns the clean fuzzy text.
// Examples:
//   "auth *.go"         -> FuzzyText: "auth", Constraints: [Extension: "go"]
//   "cmd/ !test service" -> FuzzyText: "service", Constraints: [PathSegment: "cmd/", Exclude: "test"]
//   "**/*_test.go foo"  -> FuzzyText: "foo", Constraints: [Glob: "**/*_test.go"]
func Parse(raw string) Query {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Query{}
	}

	tokens := strings.Fields(trimmed)
	var constraints []Constraint
	var fuzzyWords []string

	for _, token := range tokens {
		if strings.HasPrefix(token, "!") {
			neg := strings.TrimPrefix(token, "!")
			if neg != "" {
				constraints = append(constraints, Constraint{
					Type:  ConstraintExclude,
					Value: strings.ToLower(neg),
				})
			}
			continue
		}

		// Check for Glob: contains * or ?
		if strings.ContainsAny(token, "*?") {
			if strings.HasPrefix(token, "*.") && !strings.Contains(token[2:], "*") && !strings.Contains(token[2:], "?") && !strings.Contains(token[2:], "/") {
				// Simple extension shortcut: *.go -> Extension("go")
				ext := strings.TrimPrefix(token, "*.")
				constraints = append(constraints, Constraint{
					Type:  ConstraintExtension,
					Value: strings.ToLower(ext),
				})
			} else {
				constraints = append(constraints, Constraint{
					Type:  ConstraintGlob,
					Value: token,
				})
			}
			continue
		}

		// Check for directory constraint: ends with /
		if strings.HasSuffix(token, "/") {
			constraints = append(constraints, Constraint{
				Type:  ConstraintPathSegment,
				Value: strings.ToLower(token),
			})
			continue
		}

		// Check for file path constraint with slash: e.g. "pkg/tools/fff.go"
		if strings.Contains(token, "/") && strings.Contains(token, ".") {
			constraints = append(constraints, Constraint{
				Type:  ConstraintFilePath,
				Value: strings.ToLower(token),
			})
			continue
		}

		// Plain text token for fuzzy matching
		fuzzyWords = append(fuzzyWords, token)
	}

	return Query{
		Raw:         raw,
		FuzzyText:   strings.Join(fuzzyWords, " "),
		Constraints: constraints,
	}
}

// Matches checks if a candidate relative path satisfies all query constraints.
func (q Query) Matches(path string) bool {
	pathLower := strings.ToLower(path)

	for _, c := range q.Constraints {
		switch c.Type {
		case ConstraintExtension:
			if !strings.HasSuffix(pathLower, "."+c.Value) {
				return false
			}
		case ConstraintPathSegment:
			if !strings.Contains(pathLower, c.Value) {
				return false
			}
		case ConstraintFilePath:
			if !strings.HasSuffix(pathLower, c.Value) && !strings.Contains(pathLower, c.Value) {
				return false
			}
		case ConstraintExclude:
			if strings.Contains(pathLower, c.Value) {
				return false
			}
		case ConstraintGlob:
			if !matchSimpleGlob(c.Value, path) {
				return false
			}
		}
	}
	return true
}

func matchSimpleGlob(pattern, s string) bool {
	// Standard simple wildcard matching (* and ?)
	p := strings.ToLower(pattern)
	t := strings.ToLower(s)

	parts := strings.Split(p, "*")
	if len(parts) == 1 {
		return p == t
	}

	leading := parts[0]
	trailing := parts[len(parts)-1]

	if !strings.HasPrefix(t, leading) {
		return false
	}
	if !strings.HasSuffix(t, trailing) {
		return false
	}

	idx := len(leading)
	for i := 1; i < len(parts)-1; i++ {
		sub := parts[i]
		if sub == "" {
			continue
		}
		subIdx := strings.Index(t[idx:], sub)
		if subIdx < 0 {
			return false
		}
		idx += subIdx + len(sub)
	}

	return true
}

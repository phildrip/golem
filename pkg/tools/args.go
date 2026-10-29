package tools

import (
	"encoding/json"
	"strconv"
	"strings"

	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"
)

// declaringTool is the part of a function tool that carries its schema. ADK
// keeps the interface unexported, so it is restated structurally here. Tools
// without one (model-side built-ins, opaque toolsets) are left alone.
type declaringTool interface {
	Declaration() *genai.FunctionDeclaration
}

// paramSchema is the slice of a JSON Schema this file reads: the type of a
// property and how to descend into the properties of an object. Every other
// keyword is ignored, which keeps the reader working for both the schema ADK
// generates from Go types and a hand-written one.
type paramSchema struct {
	Type       json.RawMessage        `json:"type"`
	Properties map[string]paramSchema `json:"properties"`
	Items      *paramSchema           `json:"items"`
}

// types returns the schema's declared types. "type" is either a string or an
// array of strings in JSON Schema, and an unresolved schema may omit it.
func (s paramSchema) types() []string {
	if len(s.Type) == 0 {
		return nil
	}
	if s.Type[0] == '[' {
		var many []string
		if err := json.Unmarshal(s.Type, &many); err != nil {
			return nil
		}
		return many
	}
	var one string
	if err := json.Unmarshal(s.Type, &one); err != nil {
		return nil
	}
	return []string{one}
}

// has reports whether the schema accepts a value of type typ.
func (s paramSchema) has(typ string) bool {
	for _, t := range s.types() {
		if t == typ {
			return true
		}
	}
	return false
}

// RepairArgs rewrites arguments that arrived with the wrong JSON type into the
// type the tool's own schema asks for, in place, so the call can run.
//
// Models sometimes send a number as a string - {"id":"5441"} for a field the
// schema declares as an integer. The ADK validates arguments against the
// declaration before the handler runs and rejects the call outright
// ("type: 5441 has type \"string\", want \"integer\""), so a tool that would
// have worked is lost, and because the model is not told why, the next attempt
// tends to make the same mistake.
//
// It also undoes two shapes models wrap things in: a list around a single
// string ({"tools":["read"]} for a string field) and a JSON-encoded object or
// array in a string field.
//
// Structure: some models flatten a nested object or array
// into a JSON-encoded string ({"budgets":"{\"max_nodes\":4}"}), which the same
// validation rejects even though the value is the right data in the wrong
// wrapper. A string parameter whose value happens to look like JSON (a shell
// command, a file, a search query) stays a string, because the schema decides.
//
// Only arguments the declaration marks as numeric, boolean, object or array are
// touched, so the rewrite is driven entirely by the schema: it covers nested
// fields too, and it is a no-op for arguments that already have the right type.
func RepairArgs(t tool.Tool, args map[string]any) {
	if t == nil || len(args) == 0 {
		return
	}
	dt, ok := t.(declaringTool)
	if !ok {
		return
	}
	decl := dt.Declaration()
	if decl == nil || decl.ParametersJsonSchema == nil {
		return
	}
	// The declaration holds a *jsonschema.Schema, whose type keywords are not
	// plain struct fields. Round-tripping to JSON is the one shape that does not
	// depend on how the schema was built.
	raw, err := json.Marshal(decl.ParametersJsonSchema)
	if err != nil {
		return
	}
	var root paramSchema
	if err := json.Unmarshal(raw, &root); err != nil {
		return
	}
	repairProperties(root, args)
}

// repairProperties walks one level of args, coercing values whose schema asks
// for a number or boolean and recursing into object-valued arguments.
func repairProperties(schema paramSchema, args map[string]any) {
	for name, value := range args {
		prop, ok := schema.Properties[name]
		if !ok {
			continue
		}
		if s, isString := value.(string); isString {
			if coerced, ok := coerceString(s, prop); ok {
				args[name] = coerced
			}
			continue
		}
		if nested, isObject := value.(map[string]any); isObject && len(prop.Properties) > 0 {
			repairProperties(prop, nested)
			continue
		}
		if list, isList := value.([]any); isList {
			if unwrapped, ok := unwrapSingleString(list, prop); ok {
				args[name] = unwrapped
				continue
			}
			if prop.Items != nil {
				repairItems(*prop.Items, list)
			}
		}
	}
}

// unwrapSingleString turns ["read"] into "read" for a field the schema declares
// as a string: models reach for a list when a value looks like an enum, and a
// one-element list of strings carries no other meaning.
func unwrapSingleString(list []any, schema paramSchema) (string, bool) {
	if !schema.has("string") || len(list) != 1 {
		return "", false
	}
	s, ok := list[0].(string)
	if !ok {
		return "", false
	}
	return s, true
}

// repairItems walks an array argument, coercing stringified elements and
// recursing into object elements.
func repairItems(schema paramSchema, list []any) {
	for i, item := range list {
		switch v := item.(type) {
		case string:
			if coerced, ok := coerceString(v, schema); ok {
				list[i] = coerced
			}
		case map[string]any:
			if len(schema.Properties) > 0 {
				repairProperties(schema, v)
			}
		case []any:
			if unwrapped, ok := unwrapSingleString(v, schema); ok {
				list[i] = unwrapped
				continue
			}
			if schema.Items != nil {
				repairItems(*schema.Items, v)
			}
		}
	}
}

// coerceString turns s into the type schema asks for, reporting false when the
// schema wants a string, wants something else, or s is not a clean literal of
// that type. A failed parse is left alone: the tool's own error message is more
// useful than a guess.
func coerceString(s string, schema paramSchema) (any, bool) {
	if schema.has("string") {
		return nil, false
	}
	trimmed := strings.TrimSpace(s)
	switch {
	case schema.has("integer"):
		n, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return nil, false
		}
		return n, true
	case schema.has("number"):
		f, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return nil, false
		}
		return f, true
	case schema.has("boolean"):
		switch trimmed {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	case schema.has("object"):
		var m map[string]any
		if err := json.Unmarshal([]byte(trimmed), &m); err != nil {
			return nil, false
		}
		// The rehydrated object may itself hold stringified numbers.
		if len(schema.Properties) > 0 {
			repairProperties(schema, m)
		}
		return m, true
	case schema.has("array"):
		var list []any
		if err := json.Unmarshal([]byte(trimmed), &list); err != nil {
			return nil, false
		}
		if schema.Items != nil {
			repairItems(*schema.Items, list)
		}
		return list, true
	}
	return nil, false
}

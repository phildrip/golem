package graph

import "encoding/json"

func marshalIndent(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }

func marshalJSON(v any) ([]byte, error) { return json.Marshal(v) }

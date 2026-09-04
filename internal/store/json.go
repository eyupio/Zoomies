package store

import "encoding/json"

// marshalJSON renders a value for a TEXT column, defaulting to "{}" so the
// schema never holds a NULL where a decoder expects an object.
func marshalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// unmarshalJSON decodes a TEXT column, tolerating empty values.
func unmarshalJSON(s string, out any) error {
	if s == "" {
		return nil
	}
	return json.Unmarshal([]byte(s), out)
}

package groups

import (
	"encoding/json"
	"testing"
)

// roundTrip marshals a value and decodes it back into a generic tree, which is
// what the JSON Schema validator expects to be handed.
func roundTrip(t *testing.T, v any) any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

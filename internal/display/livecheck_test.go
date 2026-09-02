package display

import (
	"encoding/json"
	"os"
	"testing"
)

// TestLivePortsValidate validates a captured `sonar list --json` payload
// against the checked-in Port schema. Skipped unless SONAR_LIVE_JSON points at
// a file; it exists so a maintainer can verify real output on their machine.
func TestLivePortsValidate(t *testing.T) {
	path := os.Getenv("SONAR_LIVE_JSON")
	if path == "" {
		t.Skip("set SONAR_LIVE_JSON to a `sonar list --json` capture to run this")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	sch := compilePortSchema(t)
	for i, row := range rows {
		if err := sch.Validate(row); err != nil {
			t.Errorf("live row %d invalid:\n%v", i, err)
		}
	}
	t.Logf("validated %d live rows", len(rows))
}

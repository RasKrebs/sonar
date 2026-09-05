package cmd

import "testing"

func TestSelectorFrom(t *testing.T) {
	sel, err := selectorFrom("8123", false, nil)
	if err != nil {
		t.Fatalf("selectorFrom: %v", err)
	}
	if sel.Port == nil || *sel.Port != 8123 || sel.PID != nil {
		t.Errorf("selector = %+v, want port 8123", sel)
	}

	sel, err = selectorFrom("4242", true, nil)
	if err != nil {
		t.Fatalf("selectorFrom --pid: %v", err)
	}
	if sel.PID == nil || *sel.PID != 4242 || sel.Port != nil {
		t.Errorf("selector = %+v, want pid 4242", sel)
	}

	for _, bad := range []string{"", "http", "-1", "0"} {
		if _, err := selectorFrom(bad, false, nil); err == nil {
			t.Errorf("selectorFrom(%q) succeeded, want an error", bad)
		}
	}
}

func TestWriteValue(t *testing.T) {
	v, err := writeValue([]string{"8123", " storefront "}, false, "name", "example")
	if err != nil {
		t.Fatalf("writeValue: %v", err)
	}
	if v == nil || *v != "storefront" {
		t.Errorf("value = %v, want the trimmed name", v)
	}

	v, err = writeValue([]string{"8123"}, true, "name", "example")
	if err != nil {
		t.Fatalf("writeValue --clear: %v", err)
	}
	if v != nil {
		t.Errorf("value = %v, want null for --clear", *v)
	}

	if _, err := writeValue([]string{"8123"}, false, "name", "example"); err == nil {
		t.Error("a bare port with no name and no --clear was accepted")
	}
	if _, err := writeValue([]string{"8123", "web"}, true, "name", "example"); err == nil {
		t.Error("--clear with a name was accepted")
	}
	if _, err := writeValue([]string{"8123", "   "}, false, "name", "example"); err == nil {
		t.Error("an all-whitespace name was accepted")
	}
}

func TestHumanTimeFallsBackToTheRawValue(t *testing.T) {
	if got := humanTime("not a time"); got != "not a time" {
		t.Errorf("humanTime = %q, want the raw value back", got)
	}
	if got := humanTime("2026-09-05T10:11:12Z"); got == "2026-09-05T10:11:12Z" {
		t.Error("humanTime did not format a valid timestamp")
	}
}

package store

import (
	"errors"
	"testing"
)

func TestRenameRoundTrip(t *testing.T) {
	s := openTemp(t)
	const key = "cwd:/code/shop:3000"

	if _, ok, err := s.GetRename(key); err != nil || ok {
		t.Fatalf("GetRename on an empty store = %v, %v; want false, nil", ok, err)
	}

	if err := s.SetRename(key, "storefront"); err != nil {
		t.Fatalf("SetRename: %v", err)
	}
	if name, ok, err := s.GetRename(key); err != nil || !ok || name != "storefront" {
		t.Fatalf("GetRename = %q, %v, %v; want storefront, true, nil", name, ok, err)
	}

	// Renaming again replaces rather than duplicating.
	if err := s.SetRename(key, "shop-web"); err != nil {
		t.Fatalf("SetRename (replace): %v", err)
	}
	if name, _, _ := s.GetRename(key); name != "shop-web" {
		t.Errorf("GetRename after replace = %q, want shop-web", name)
	}
	all, err := s.Renames()
	if err != nil {
		t.Fatalf("Renames: %v", err)
	}
	if len(all) != 1 || all[key] != "shop-web" {
		t.Errorf("Renames = %v, want one entry", all)
	}

	if err := s.ClearRename(key); err != nil {
		t.Fatalf("ClearRename: %v", err)
	}
	if _, ok, _ := s.GetRename(key); ok {
		t.Error("rename still present after ClearRename")
	}
	// Clearing again is a no-op, not an error: `--clear` is idempotent.
	if err := s.ClearRename(key); err != nil {
		t.Errorf("second ClearRename: %v", err)
	}
}

func TestPinRoundTrip(t *testing.T) {
	s := openTemp(t)
	const key = "run:sonar/frontend"

	if err := s.SetPin(key, "sonar"); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	if group, ok, err := s.GetPin(key); err != nil || !ok || group != "sonar" {
		t.Fatalf("GetPin = %q, %v, %v; want sonar, true, nil", group, ok, err)
	}
	if err := s.SetPin(key, "shop"); err != nil {
		t.Fatalf("SetPin (replace): %v", err)
	}
	pins, err := s.Pins()
	if err != nil {
		t.Fatalf("Pins: %v", err)
	}
	if len(pins) != 1 || pins[key] != "shop" {
		t.Errorf("Pins = %v, want one entry pinned to shop", pins)
	}
	if err := s.ClearPin(key); err != nil {
		t.Fatalf("ClearPin: %v", err)
	}
	if _, ok, _ := s.GetPin(key); ok {
		t.Error("pin still present after ClearPin")
	}
	if err := s.ClearPin(key); err != nil {
		t.Errorf("second ClearPin: %v", err)
	}
}

func TestRenamesAndPinsAreIndependent(t *testing.T) {
	s := openTemp(t)
	const key = "port:3000"

	if err := s.SetRename(key, "api"); err != nil {
		t.Fatalf("SetRename: %v", err)
	}
	if err := s.SetPin(key, "backend"); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	if err := s.ClearRename(key); err != nil {
		t.Fatalf("ClearRename: %v", err)
	}
	if group, ok, _ := s.GetPin(key); !ok || group != "backend" {
		t.Errorf("clearing the rename also cleared the pin (%q, %v)", group, ok)
	}
}

func TestInvalidNamesRejected(t *testing.T) {
	s := openTemp(t)
	for _, bad := range []string{"", " ", "with space", "with/slash", "with\\backslash", "trailing "} {
		if err := s.SetRename("port:1", bad); !errors.Is(err, ErrInvalidName) {
			t.Errorf("SetRename(%q) error = %v, want ErrInvalidName", bad, err)
		}
		if err := s.SetPin("port:1", bad); !errors.Is(err, ErrInvalidName) {
			t.Errorf("SetPin(%q) error = %v, want ErrInvalidName", bad, err)
		}
	}
	if err := s.SetRename("", "api"); err == nil {
		t.Error("SetRename with an empty key succeeded")
	}
	if err := s.SetPin("", "api"); err == nil {
		t.Error("SetPin with an empty key succeeded")
	}
}

func TestResolvePrefersTheMostSpecificKey(t *testing.T) {
	s := openTemp(t)
	p := port(5173, withRun("sonar", "frontend", 1), withRoot(osPath("/code/sonar")))

	if _, ok, err := s.ResolveRename(p); err != nil || ok {
		t.Fatalf("ResolveRename on an empty store = %v, %v", ok, err)
	}

	if err := s.SetRename("port:5173", "by-port"); err != nil {
		t.Fatalf("SetRename: %v", err)
	}
	if name, ok, _ := s.ResolveRename(p); !ok || name != "by-port" {
		t.Errorf("ResolveRename = %q, %v; want by-port", name, ok)
	}

	if err := s.SetRename(cwdKey("/code/sonar", 5173), "by-cwd"); err != nil {
		t.Fatalf("SetRename: %v", err)
	}
	if name, _, _ := s.ResolveRename(p); name != "by-cwd" {
		t.Errorf("ResolveRename = %q, want the cwd key to beat the port key", name)
	}

	if err := s.SetRename("run:sonar/frontend", "by-run"); err != nil {
		t.Fatalf("SetRename: %v", err)
	}
	if name, _, _ := s.ResolveRename(p); name != "by-run" {
		t.Errorf("ResolveRename = %q, want the run key to win", name)
	}

	// Pins resolve through exactly the same ladder.
	if err := s.SetPin("port:5173", "loose"); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	if err := s.SetPin("run:sonar/frontend", "tight"); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	if group, ok, _ := s.ResolvePin(p); !ok || group != "tight" {
		t.Errorf("ResolvePin = %q, %v; want tight", group, ok)
	}
}

package timeutil

import "testing"

func TestBusinessLocation(t *testing.T) {
	loc := BusinessLocation()
	if loc == nil {
		t.Fatal("nil location")
	}
	if name := loc.String(); name != BusinessZone && name != "CST" {
		t.Fatalf("location = %q", name)
	}
	if _, err := LoadLocation(BusinessZone); err != nil {
		t.Fatalf("embedded tzdata missing: %v", err)
	}
}

package redact

import (
	"strings"
	"testing"
)

func TestRedactAndScan(t *testing.T) {
	in := "phone 13800138000 email a.b@example.com\nAuthorization: Bearer abc.def.ghi\npassword=hunter2"
	out, findings := Redact(in)
	if len(findings) < 3 {
		t.Fatalf("findings = %d, want >=3", len(findings))
	}
	for _, secret := range []string{"13800138000", "a.b@example.com", "abc.def.ghi", "hunter2"} {
		if strings.Contains(out, secret) {
			t.Fatalf("redacted output still contains %q: %s", secret, out)
		}
	}
	if !strings.Contains(out, "[REDACTED:phone]") {
		t.Fatalf("phone marker missing: %s", out)
	}
	if got := Scan(in); len(got) == 0 {
		t.Fatal("Scan returned no findings")
	}
}

func TestHashDeterministic(t *testing.T) {
	a := Hash([]byte("hello"))
	b := Hash([]byte("hello"))
	if a != b || !strings.HasPrefix(a, "sha256:") {
		t.Fatalf("unexpected hash: %q %q", a, b)
	}
	if a == Hash([]byte("hello!")) {
		t.Fatal("different inputs must not collide in this test")
	}
}

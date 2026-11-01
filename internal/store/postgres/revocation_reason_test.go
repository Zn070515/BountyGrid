package postgres

import (
	"strings"
	"testing"
)

func TestSanitizeRevocationReason(t *testing.T) {
	plain := sanitizeRevocationReason("  manual smoke  ")
	if plain.Text != "manual smoke" || plain.Redacted || plain.Hash != "" || len(plain.Kinds) != 0 {
		t.Fatalf("plain reason = %+v", plain)
	}
	if empty := sanitizeRevocationReason("   "); empty.Text != "" || empty.Redacted || empty.Hash != "" {
		t.Fatalf("empty reason = %+v", empty)
	}

	secret := sanitizeRevocationReason("leaked session password=hunter2 for admin")
	if secret.Text != "" {
		t.Fatalf("secret text was preserved: %q", secret.Text)
	}
	if !secret.Redacted {
		t.Fatal("secret reason was not marked redacted")
	}
	if !strings.HasPrefix(secret.Hash, "sha256:") || len(secret.Hash) != len("sha256:")+64 {
		t.Fatalf("reason hash = %q", secret.Hash)
	}
	if len(secret.Kinds) != 1 || secret.Kinds[0] != "password" {
		t.Fatalf("redaction kinds = %v, want [password]", secret.Kinds)
	}
	if strings.Contains(secret.Hash, "hunter2") {
		t.Fatal("reason hash embeds the secret")
	}
}

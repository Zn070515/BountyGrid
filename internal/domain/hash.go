package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// CanonicalParametersHash hashes JSON parameters in a form that survives an
// encoding/json + PostgreSQL JSONB round-trip: whitespace and object key order
// are normalized. Invalid JSON falls back to the raw bytes rather than being
// treated as an empty parameter set.
func CanonicalParametersHash(p []byte) string {
	if len(bytes.TrimSpace(p)) == 0 {
		return hashBytes(nil)
	}
	dec := json.NewDecoder(bytes.NewReader(p))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return hashBytes(p)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return hashBytes(p)
	}
	return hashBytes(b)
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

package domain

import "errors"

// ErrNotFound is the store-neutral "entity does not exist" sentinel. Ports use
// errors.Is against this value so an Action Gateway does not need to import a
// concrete database package.
var ErrNotFound = errors.New("not found")

// ErrConflict means an append-only record with the same ID already exists with
// different content. Callers must create a new ID instead of rewriting history.
var ErrConflict = errors.New("conflict: id exists with different content")

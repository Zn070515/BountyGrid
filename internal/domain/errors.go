package domain

import "errors"

// ErrNotFound is the store-neutral "entity does not exist" sentinel. Ports use
// errors.Is against this value so an Action Gateway does not need to import a
// concrete database package.
var ErrNotFound = errors.New("not found")

package model

import _ "embed"

// Source is this package's own code. Parsed usage is cached as Turn values,
// so a cache written by a different Turn shape must not be reused.
//
//go:embed types.go
var Source []byte

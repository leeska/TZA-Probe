// Package catalog provides a bundled, offline list of editable probe targets.
package catalog

import _ "embed"

// JSON is a dated target snapshot, not a promise of target availability.
//
//go:embed targets.json
var JSON []byte

// Package verify — JSON helper (no build tag; used by verifier + tests).
package verify

import "encoding/json"

// jsonUnmarshal wraps json.Unmarshal so the rest of the package does not need
// to import "encoding/json" directly, keeping the import surface minimal.
func jsonUnmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

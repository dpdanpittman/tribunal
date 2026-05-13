package main

import (
	"encoding/json"
	"io"
)

// newJSONEncoder returns a json.Encoder with sensible defaults for CLI
// output (2-space indent, no HTML escaping).
func newJSONEncoder(w io.Writer) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc
}

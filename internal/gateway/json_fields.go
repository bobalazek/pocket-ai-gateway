package gateway

import (
	"bytes"
	"encoding/json"
	"io"
)

// uniqueJSONFields rejects duplicate keys and bounds the depth and work of the
// shared JSON wire parser before any provider-specific interpretation.
func uniqueJSONFields(raw []byte, maxDepth int) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var walk func(int) bool
	values := 0
	walk = func(depth int) bool {
		values++
		if depth > maxDepth || values > 100000 {
			return false
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				name, err := decoder.Token()
				key, ok := name.(string)
				if err != nil || !ok || seen[key] {
					return false
				}
				seen[key] = true
				if !walk(depth + 1) {
					return false
				}
			}
		case '[':
			for decoder.More() {
				if !walk(depth + 1) {
					return false
				}
			}
		default:
			return false
		}
		end, err := decoder.Token()
		return err == nil && end == map[json.Delim]json.Delim{'{': '}', '[': ']'}[delim]
	}
	if !walk(0) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

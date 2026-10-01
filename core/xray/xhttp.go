package xray

import (
	"encoding/json"
	"fmt"
)

// Xboard/PHP serializes empty objects as []; Xray expects objects in extra settings.
func normalizeXHTTPSettings(raw json.RawMessage) (json.RawMessage, error) {
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		return nil, fmt.Errorf("decode xhttp settings: %w", err)
	}
	var sanitize func(any)
	sanitize = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			for key, item := range v {
				if array, ok := item.([]any); ok && len(array) == 0 {
					delete(v, key)
				} else {
					sanitize(item)
				}
			}
		case []any:
			for _, item := range v {
				sanitize(item)
			}
		}
	}
	sanitize(settings)
	return json.Marshal(settings)
}

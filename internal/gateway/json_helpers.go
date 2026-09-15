package gateway

func objectMap(value any) map[string]any { result, _ := value.(map[string]any); return result }
func array(value any) []any              { result, _ := value.([]any); return result }
func stringValue(value any) string       { result, _ := value.(string); return result }
func number(value any) int64             { result, _ := value.(float64); return int64(result) }

func firstString(object map[string]any, names ...string) string {
	for _, name := range names {
		if value := stringValue(object[name]); value != "" {
			return value
		}
	}
	return ""
}

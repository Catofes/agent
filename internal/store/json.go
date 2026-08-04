package store

import "encoding/json"

func encodeTools(v []string) string { b, _ := json.Marshal(v); return string(b) }
func decodeTools(v string) []string {
	var out []string
	_ = json.Unmarshal([]byte(v), &out)
	if out == nil {
		out = []string{}
	}
	return out
}

package repo

import "encoding/json"

// Small wrappers so the onboarding code does not import encoding/json twice over.
func jsonMarshal(v any) ([]byte, error)   { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

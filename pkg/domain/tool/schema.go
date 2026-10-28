package tool

import "encoding/json"

func decodeSchema(data []byte, target any, fields ...string) (map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	known := make(map[string]json.RawMessage)
	for _, field := range fields {
		if value, ok := raw[field]; ok {
			if field == "type" || field == "enum" {
				var scalar any
				_ = json.Unmarshal(value, &scalar)
				if field == "type" {
					if _, ok := scalar.(string); !ok {
						continue
					}
				} else {
					var values []string
					if json.Unmarshal(value, &values) != nil {
						continue
					}
				}
			}
			known[field] = value
			delete(raw, field)
		}
	}
	encoded, err := json.Marshal(known)
	if err != nil {
		return nil, err
	}
	return raw, json.Unmarshal(encoded, target)
}

func mergeSchema(data []byte, extra map[string]json.RawMessage) ([]byte, error) {
	if len(extra) == 0 {
		return data, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	for key, value := range extra {
		raw[key] = value
	}
	return json.Marshal(raw)
}

func (s *JSONSchema) UnmarshalJSON(data []byte) error {
	type plain JSONSchema
	var decoded plain
	extra, err := decodeSchema(data, &decoded, "type", "properties", "required")
	if err != nil {
		return err
	}
	*s = JSONSchema(decoded)
	s.Extra = extra
	return nil
}

func (s *SchemaProp) UnmarshalJSON(data []byte) error {
	type plain SchemaProp
	var decoded plain
	extra, err := decodeSchema(data, &decoded, "type", "description", "enum", "items", "properties", "required")
	if err != nil {
		return err
	}
	*s = SchemaProp(decoded)
	s.Extra = extra
	return nil
}

func (s SchemaProp) MarshalJSON() ([]byte, error) {
	type plain SchemaProp
	encoded, err := json.Marshal(plain(s))
	if err != nil {
		return nil, err
	}
	return mergeSchema(encoded, s.Extra)
}

package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
)

func TaskSigningBytes(id, kind string, payload json.RawMessage) ([]byte, error) {
	var semantic any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&semantic); err != nil {
		return nil, fmt.Errorf("decode task payload: %w", err)
	}
	canonical, err := json.Marshal(semantic)
	if err != nil {
		return nil, err
	}
	result := make([]byte, 0, len(id)+len(kind)+len(canonical)+2)
	result = append(result, id...)
	result = append(result, '\n')
	result = append(result, kind...)
	result = append(result, '\n')
	result = append(result, canonical...)
	return result, nil
}

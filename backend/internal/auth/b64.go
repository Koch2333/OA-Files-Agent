package auth

import (
	"encoding/base64"
	"encoding/json"
)

func encodeB64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// decodeB64 reads {"secret_enc": "<base64>"} into d and returns the decoded bytes.
func decodeB64(data []byte, d *struct {
	SecretEnc string `json:"secret_enc"`
}) ([]byte, error) {
	if err := json.Unmarshal(data, d); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(d.SecretEnc)
}

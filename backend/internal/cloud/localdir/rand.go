package localdir

import (
	"crypto/rand"
	"encoding/hex"
)

func randSuffix() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "-" + hex.EncodeToString(b)
}

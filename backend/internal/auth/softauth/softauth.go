// Package softauth is a software WebAuthn authenticator (ES256, "none" attestation, user verification always
// performed) for tests and the acceptance driver. It speaks the standard JSON forms the browser API uses.
package softauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
)

var b64 = base64.RawURLEncoding

// Authenticator holds one discoverable credential.
type Authenticator struct {
	Key    *ecdsa.PrivateKey
	CredID []byte
	Handle []byte // user handle given at registration
	// Counter is the signature counter reported by the next assertion (incremented before each use unless Fixed).
	Counter uint32
	// Fixed keeps Counter unchanged between assertions (simulates a cloned authenticator).
	Fixed bool
	// NoUV clears the user-verified flag (to test that the server demands UV).
	NoUV bool
	// BackupEligible sets the BE flag (synced passkey).
	BackupEligible bool
}

func New() *Authenticator {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	id := make([]byte, 32)
	_, _ = rand.Read(id)
	return &Authenticator{Key: k, CredID: id}
}

type creationOptions struct {
	Challenge string `json:"challenge"`
	RP        struct {
		ID string `json:"id"`
	} `json:"rp"`
	User struct {
		ID string `json:"id"`
	} `json:"user"`
	Exclude []struct {
		ID string `json:"id"`
	} `json:"excludeCredentials"`
	Selection struct {
		ResidentKey      string `json:"residentKey"`
		UserVerification string `json:"userVerification"`
	} `json:"authenticatorSelection"`
}

type requestOptions struct {
	Challenge string `json:"challenge"`
	RPID      string `json:"rpId"`
	UV        string `json:"userVerification"`
}

func (a *Authenticator) flags(extra byte) byte {
	f := byte(0x01) | extra
	if !a.NoUV {
		f |= 0x04
	}
	if a.BackupEligible {
		f |= 0x08
	}
	return f
}

func clientData(typ, challenge, origin string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": origin, "crossOrigin": false})
	return b
}

// Create answers navigator.credentials.create() for the given PublicKeyCredentialCreationOptionsJSON.
func (a *Authenticator) Create(optionsJSON []byte, origin string) ([]byte, error) {
	var o creationOptions
	if err := json.Unmarshal(optionsJSON, &o); err != nil {
		return nil, err
	}
	if o.Selection.ResidentKey != "required" || o.Selection.UserVerification != "required" {
		return nil, errors.New("softauth: server did not require a discoverable, user-verified credential")
	}
	for _, e := range o.Exclude {
		if id, _ := b64.DecodeString(e.ID); string(id) == string(a.CredID) {
			return nil, errors.New("softauth: credential excluded (already registered)")
		}
	}
	handle, err := b64.DecodeString(o.User.ID)
	if err != nil {
		return nil, err
	}
	a.Handle = handle
	rp := sha256.Sum256([]byte(o.RP.ID))
	x, y := a.Key.PublicKey.X.FillBytes(make([]byte, 32)), a.Key.PublicKey.Y.FillBytes(make([]byte, 32))
	cose, err := webauthncbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	if err != nil {
		return nil, err
	}
	auth := append([]byte{}, rp[:]...)
	auth = append(auth, a.flags(0x40))
	auth = binary.BigEndian.AppendUint32(auth, a.Counter)
	auth = append(auth, make([]byte, 16)...) // zero AAGUID
	auth = binary.BigEndian.AppendUint16(auth, uint16(len(a.CredID)))
	auth = append(auth, a.CredID...)
	auth = append(auth, cose...)
	att, err := webauthncbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": auth})
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"id": b64.EncodeToString(a.CredID), "rawId": b64.EncodeToString(a.CredID), "type": "public-key", "authenticatorAttachment": "platform",
		"response":               map[string]any{"clientDataJSON": b64.EncodeToString(clientData("webauthn.create", o.Challenge, origin)), "attestationObject": b64.EncodeToString(att), "transports": []string{"internal"}},
		"clientExtensionResults": map[string]any{},
	})
}

// Get answers navigator.credentials.get() for the given PublicKeyCredentialRequestOptionsJSON.
func (a *Authenticator) Get(optionsJSON []byte, origin string) ([]byte, error) {
	var o requestOptions
	if err := json.Unmarshal(optionsJSON, &o); err != nil {
		return nil, err
	}
	if !a.Fixed {
		a.Counter++
	}
	rp := sha256.Sum256([]byte(o.RPID))
	auth := append([]byte{}, rp[:]...)
	auth = append(auth, a.flags(0))
	auth = binary.BigEndian.AppendUint32(auth, a.Counter)
	cd := clientData("webauthn.get", o.Challenge, origin)
	h := sha256.Sum256(cd)
	sig, err := ecdsa.SignASN1(rand.Reader, a.Key, sha256sum(append(append([]byte{}, auth...), h[:]...)))
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"id": b64.EncodeToString(a.CredID), "rawId": b64.EncodeToString(a.CredID), "type": "public-key", "authenticatorAttachment": "platform",
		"response":               map[string]any{"clientDataJSON": b64.EncodeToString(cd), "authenticatorData": b64.EncodeToString(auth), "signature": b64.EncodeToString(sig), "userHandle": b64.EncodeToString(a.Handle)},
		"clientExtensionResults": map[string]any{},
	})
}

func sha256sum(b []byte) []byte { h := sha256.Sum256(b); return h[:] }

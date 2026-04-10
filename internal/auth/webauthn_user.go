package auth

import (
	"encoding/json"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// webauthnUser adapts a Store User + Credentials to the webauthn.User interface.
// It is used by the WebAuthn begin/finish handlers.
type webauthnUser struct {
	id          []byte
	name        string
	displayName string
	credentials []webauthn.Credential
}

var _ webauthn.User = (*webauthnUser)(nil)

// newWebAuthnUser creates a webauthnUser from a Store User and its credentials.
func newWebAuthnUser(u *User, creds []Credential) (*webauthnUser, error) {
	waCreds := make([]webauthn.Credential, 0, len(creds))
	for _, c := range creds {
		var transports []protocol.AuthenticatorTransport
		if c.Transport != "" {
			if err := json.Unmarshal([]byte(c.Transport), &transports); err != nil {
				// If transport JSON is malformed, treat as no transport.
				transports = nil
			}
		}
		waCreds = append(waCreds, webauthn.Credential{
			ID:              []byte(c.ID),
			PublicKey:       c.PublicKey,
			AttestationType: c.AttestationType,
			Transport:       transports,
		})
	}
	return &webauthnUser{
		id:          []byte(u.ID),
		name:        u.Username,
		displayName: u.Username,
		credentials: waCreds,
	}, nil
}

func (u *webauthnUser) WebAuthnID() []byte {
	return u.id
}

func (u *webauthnUser) WebAuthnName() string {
	return u.name
}

func (u *webauthnUser) WebAuthnDisplayName() string {
	return u.displayName
}

func (u *webauthnUser) WebAuthnCredentials() []webauthn.Credential {
	return u.credentials
}

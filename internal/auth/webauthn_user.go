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

// credentialFlags is a JSON-serializable representation of the
// webauthn.CredentialFlags struct. These flags are stored alongside each
// credential and must be restored when loading credentials for login
// verification. Missing or empty flags default to false, which is safe for
// the initial registration flow but MUST be populated from the stored values
// for re-login to succeed (particularly BackupEligible).
type credentialFlags struct {
	UserPresent    bool `json:"user_present"`
	UserVerified   bool `json:"user_verified"`
	BackupEligible bool `json:"backup_eligible"`
	BackupState    bool `json:"backup_state"`
}

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

		// Restore CredentialFlags from stored JSON. These flags (especially
		// BackupEligible) are checked by the WebAuthn library during login
		// validation. If they don't match what the authenticator reports,
		// login fails with "Backup Eligible flag inconsistency detected".
		var flags credentialFlags
		if c.Flags != "" && c.Flags != "{}" {
			_ = json.Unmarshal([]byte(c.Flags), &flags)
		}

		waCreds = append(waCreds, webauthn.Credential{
			ID:              []byte(c.ID),
			PublicKey:       c.PublicKey,
			AttestationType: c.AttestationType,
			Transport:       transports,
			Flags: webauthn.CredentialFlags{
				UserPresent:    flags.UserPresent,
				UserVerified:   flags.UserVerified,
				BackupEligible: flags.BackupEligible,
				BackupState:    flags.BackupState,
			},
			Authenticator: webauthn.Authenticator{
				AAGUID:    c.AAGUID,
				SignCount: uint32(c.SignCount),
			},
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

// marshalCredentialFlags converts a webauthn.CredentialFlags to JSON for
// storage in the credentials table.
func marshalCredentialFlags(flags webauthn.CredentialFlags) string {
	f := credentialFlags{
		UserPresent:    flags.UserPresent,
		UserVerified:   flags.UserVerified,
		BackupEligible: flags.BackupEligible,
		BackupState:    flags.BackupState,
	}
	b, _ := json.Marshal(f)
	return string(b)
}

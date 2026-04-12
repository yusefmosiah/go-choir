package auth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// AccessTokenCookieName is the cookie name for the short-lived access JWT.
const AccessTokenCookieName = "choir_access"

// RefreshTokenCookieName is the cookie name for the refresh token.
const RefreshTokenCookieName = "choir_refresh"

// SessionChallengeTTL is how long a WebAuthn challenge remains valid.
const SessionChallengeTTL = 5 * time.Minute

// Handler provides HTTP handlers for the /auth/* routes.
type Handler struct {
	store    *Store
	webauthn *webauthn.WebAuthn
	config   *Config
	signer   ed25519.PrivateKey // loaded at startup for JWT signing
}

// NewHandler creates a Handler with the given store, WebAuthn instance, and config.
// The signer is the Ed25519 private key used for JWT signing.
func NewHandler(store *Store, wa *webauthn.WebAuthn, cfg *Config, signer ed25519.PrivateKey) *Handler {
	return &Handler{
		store:    store,
		webauthn: wa,
		config:   cfg,
		signer:   signer,
	}
}

// --- JSON request/response types ---

// beginRequest is the expected JSON body for register/login begin endpoints.
type beginRequest struct {
	Email string `json:"email"`
}

// sessionResponse is the JSON response for GET /auth/session.
type sessionResponse struct {
	Authenticated bool      `json:"authenticated"`
	User          *userInfo `json:"user,omitempty"`
	Error         string    `json:"error,omitempty"`
}

// userInfo contains non-secret user fields returned in session responses.
type userInfo struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	CreatedAt string `json:"created_at"`
}

// errorResponse is a generic JSON error envelope.
type errorResponse struct {
	Error string `json:"error"`
}

// finishResponse is the JSON response for successful register/login finish.
type finishResponse struct {
	OK   bool      `json:"ok"`
	User *userInfo `json:"user"`
}

// --- Helper functions ---

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("auth handler: json encode error: %v", err)
	}
}

// readJSON reads and validates a JSON body from the request.
func readJSON(r *http.Request, dst interface{}) error {
	if r.Body == nil {
		return errors.New("request body is required")
	}
	defer func() { _ = r.Body.Close() }()

	// Limit read to 1KB to prevent abuse.
	body, err := io.ReadAll(io.LimitReader(r.Body, 1024))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	if len(body) == 0 {
		return errors.New("request body is required")
	}

	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("invalid json: %w", err)
	}

	return nil
}

// readBody reads the request body and returns it as bytes. It also replaces
// r.Body with a new reader containing the same bytes so that downstream
// handlers (like the WebAuthn library) can re-read it.
func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, errors.New("request body is required")
	}
	defer func() { _ = r.Body.Close() }()

	// Limit to 64KB for WebAuthn responses (which can be larger than 1KB).
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	if len(body) == 0 {
		return nil, errors.New("request body is required")
	}

	// Replace the body so the WebAuthn library can read it.
	r.Body = io.NopCloser(bytes.NewReader(body))

	return body, nil
}

// extractChallengeFromBody extracts the WebAuthn challenge from a raw JSON
// response body. It parses just enough of the body to find the challenge
// without fully parsing the entire WebAuthn response.
func extractChallengeFromBody(body []byte) (string, error) {
	// WebAuthn responses have the challenge in response.clientDataJSON,
	// which is base64url-encoded. The raw body has it at
	// $.response.clientDataJSON which is also base64url-encoded.
	// We need to decode the clientDataJSON to get the challenge.
	//
	// The simplest approach: parse the outer JSON to get
	// response.clientDataJSON, then decode that to get the challenge field.
	var raw struct {
		Response struct {
			ClientDataJSON string `json:"clientDataJSON"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", fmt.Errorf("parse webauthn response: %w", err)
	}
	if raw.Response.ClientDataJSON == "" {
		return "", errors.New("response.clientDataJSON is empty")
	}

	// The clientDataJSON is base64url-encoded.
	decoded, err := base64.RawURLEncoding.DecodeString(raw.Response.ClientDataJSON)
	if err != nil {
		// Try standard base64 encoding as fallback.
		decoded, err = base64.StdEncoding.DecodeString(raw.Response.ClientDataJSON)
		if err != nil {
			return "", fmt.Errorf("decode clientDataJSON: %w", err)
		}
	}

	var clientData struct {
		Challenge string `json:"challenge"`
		Type      string `json:"type"`
		Origin    string `json:"origin"`
	}
	if err := json.Unmarshal(decoded, &clientData); err != nil {
		return "", fmt.Errorf("parse client data: %w", err)
	}
	if clientData.Challenge == "" {
		return "", errors.New("client data challenge is empty")
	}

	return clientData.Challenge, nil
}

// --- Session issuance helpers ---

// issueAccessJWT creates a signed Ed25519 JWT for the given user with the
// configured access token TTL.
func (h *Handler) issueAccessJWT(user *User) (string, error) {
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"sub":   user.ID,
		"iat":   now.Unix(),
		"exp":   now.Add(h.config.AccessTokenTTL).Unix(),
		"scope": "access",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	return token.SignedString(h.signer)
}

// generateRefreshToken creates a new opaque refresh token, stores its SHA-256
// hash in the database, and returns the raw token string. The raw token is
// only ever returned once (to be set as a cookie) and never stored directly.
func (h *Handler) generateRefreshToken(user *User) (string, error) {
	raw := uuid.NewString()
	hash := sha256.Sum256([]byte(raw))
	hashHex := fmt.Sprintf("%x", hash)

	now := time.Now().UTC()
	rs := &RefreshSession{
		ID:        uuid.NewString(),
		UserID:    user.ID,
		TokenHash: hashHex,
		CreatedAt: now,
		ExpiresAt: now.Add(h.config.RefreshTokenTTL),
	}
	if err := h.store.CreateRefreshSession(rs); err != nil {
		return "", fmt.Errorf("create refresh session: %w", err)
	}

	return raw, nil
}

// setAuthCookies sets the access JWT and refresh token cookies on the response.
// Both cookies are HttpOnly, SameSite=Lax, and use the configured Secure flag.
func (h *Handler) setAuthCookies(w http.ResponseWriter, accessToken, refreshToken string) {
	http.SetCookie(w, &http.Cookie{
		Name:     AccessTokenCookieName,
		Value:    accessToken,
		Path:     "/",
		MaxAge:   int(h.config.AccessTokenTTL.Seconds()),
		Secure:   h.config.CookieSecure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     RefreshTokenCookieName,
		Value:    refreshToken,
		Path:     "/auth",
		MaxAge:   int(h.config.RefreshTokenTTL.Seconds()),
		Secure:   h.config.CookieSecure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// issueSession creates a full authenticated session for the user: it mints
// an access JWT, generates a refresh token, and sets both as cookies. It
// returns the user info for inclusion in the response body.
func (h *Handler) issueSession(w http.ResponseWriter, user *User) (*userInfo, error) {
	accessToken, err := h.issueAccessJWT(user)
	if err != nil {
		return nil, fmt.Errorf("issue access JWT: %w", err)
	}

	refreshToken, err := h.generateRefreshToken(user)
	if err != nil {
		return nil, fmt.Errorf("generate refresh token: %w", err)
	}

	h.setAuthCookies(w, accessToken, refreshToken)

	return &userInfo{
		ID:        user.ID,
		Email:     user.Email,
		CreatedAt: user.CreatedAt.Format(time.RFC3339),
	}, nil
}

// validateRefreshCookie validates the refresh token from the request cookie
// against the stored refresh session. Returns the refresh session and user if
// valid, or nil if the token is missing, invalid, or expired.
func (h *Handler) validateRefreshCookie(r *http.Request) (*RefreshSession, *User, error) {
	cookie, err := r.Cookie(RefreshTokenCookieName)
	if err != nil || cookie.Value == "" {
		return nil, nil, errors.New("no refresh cookie")
	}

	hash := sha256.Sum256([]byte(cookie.Value))
	hashHex := fmt.Sprintf("%x", hash)

	rs, err := h.store.GetRefreshSessionByTokenHash(hashHex)
	if err != nil {
		return nil, nil, errors.New("refresh session not found")
	}

	if time.Now().UTC().After(rs.ExpiresAt) {
		// Expired — clean up and reject.
		_ = h.store.DeleteRefreshSessionByID(rs.ID)
		return nil, nil, errors.New("refresh session expired")
	}

	user, err := h.store.GetUserByID(rs.UserID)
	if err != nil {
		return nil, nil, errors.New("user not found for refresh session")
	}

	return rs, user, nil
}

// rotateRefreshSession replaces the current refresh session with a new one.
// The old session is deleted to prevent reuse (refresh rotation).
func (h *Handler) rotateRefreshSession(w http.ResponseWriter, oldSession *RefreshSession, user *User) error {
	// Delete the old session.
	if err := h.store.DeleteRefreshSessionByID(oldSession.ID); err != nil {
		return fmt.Errorf("delete old refresh session: %w", err)
	}

	// Generate a new refresh token.
	newRefresh, err := h.generateRefreshToken(user)
	if err != nil {
		return fmt.Errorf("generate new refresh token: %w", err)
	}

	// Issue new access JWT.
	accessJWT, err := h.issueAccessJWT(user)
	if err != nil {
		return fmt.Errorf("issue new access JWT: %w", err)
	}

	h.setAuthCookies(w, accessJWT, newRefresh)
	return nil
}

// isValidEmail checks if the given string is a valid email address.
// It uses Go's standard library mail parser and additionally verifies
// that the address contains exactly one "@" and a domain with at least
// one dot (e.g., "user@example.com").
func isValidEmail(email string) bool {
	email = strings.TrimSpace(email)
	if email == "" {
		return false
	}

	// Use Go's standard mail parser for basic validation.
	addr, err := mail.ParseAddress(email)
	if err != nil {
		return false
	}

	// The parsed address should match the input (no extra names etc).
	if addr.Address != email {
		return false
	}

	// Verify there's a domain part with at least one dot.
	parts := strings.SplitN(addr.Address, "@", 2)
	if len(parts) != 2 {
		return false
	}
	domain := parts[1]
	if !strings.Contains(domain, ".") {
		return false
	}

	return true
}

// --- HTTP Handlers ---

// HandleRegisterBegin handles POST /auth/register/begin.
// It creates a new user (if not existing), generates WebAuthn registration
// options with a challenge, and returns them as JSON.
func (h *Handler) HandleRegisterBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}

	var req beginRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	if req.Email == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "email is required"})
		return
	}

	if !isValidEmail(req.Email) {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "please enter a valid email address"})
		return
	}

	// Look up the user by email.
	user, err := h.store.GetUserByEmail(req.Email)
	if err != nil {
		// User doesn't exist yet — create them.
		userID := uuid.NewString()
		user, err = h.store.CreateUser(userID, req.Email)
		if err != nil {
			log.Printf("auth register begin: create user: %v", err)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to create user"})
			return
		}
	}

	// Build a WebAuthn user adapter.
	waUser, err := newWebAuthnUser(user, nil) // no credentials for registration
	if err != nil {
		log.Printf("auth register begin: webauthn user adapter: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal error"})
		return
	}

	// Begin the WebAuthn registration ceremony.
	creation, session, err := h.webauthn.BeginRegistration(waUser,
		webauthn.WithConveyancePreference("none"),
		webauthn.WithAuthenticatorSelection(
			webauthn.SelectAuthenticator("platform", nil, "required"),
		),
	)
	if err != nil {
		log.Printf("auth register begin: begin registration: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to begin registration"})
		return
	}

	// Serialize the WebAuthn session data for the finish handler.
	sessionDataJSON, err := json.Marshal(session)
	if err != nil {
		log.Printf("auth register begin: marshal session data: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal error"})
		return
	}

	// Persist the challenge state so the finish handler can verify it.
	challengeState := &ChallengeState{
		ID:                  session.Challenge,
		UserID:              user.ID,
		Challenge:           session.Challenge,
		Type:                "registration",
		WebAuthnSessionData: string(sessionDataJSON),
		CreatedAt:           time.Now().UTC(),
		ExpiresAt:           time.Now().UTC().Add(SessionChallengeTTL),
	}
	if err := h.store.SaveChallengeState(challengeState); err != nil {
		log.Printf("auth register begin: save challenge: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save challenge"})
		return
	}

	writeJSON(w, http.StatusOK, creation)
}

// HandleLoginBegin handles POST /auth/login/begin.
// It looks up an existing user and their credentials, then returns
// WebAuthn assertion options for the registered passkeys.
func (h *Handler) HandleLoginBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}

	var req beginRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	if req.Email == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "email is required"})
		return
	}

	if !isValidEmail(req.Email) {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "please enter a valid email address"})
		return
	}

	// Look up the user.
	user, err := h.store.GetUserByEmail(req.Email)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "user not found"})
		return
	}

	// Fetch their credentials.
	creds, err := h.store.GetCredentialsByUserID(user.ID)
	if err != nil {
		log.Printf("auth login begin: get credentials: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal error"})
		return
	}

	if len(creds) == 0 {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "user has no registered passkeys"})
		return
	}

	// Build a WebAuthn user adapter with credentials.
	waUser, err := newWebAuthnUser(user, creds)
	if err != nil {
		log.Printf("auth login begin: webauthn user adapter: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal error"})
		return
	}

	// Begin the WebAuthn login ceremony.
	assertion, session, err := h.webauthn.BeginLogin(waUser)
	if err != nil {
		log.Printf("auth login begin: begin login: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to begin login"})
		return
	}

	// Serialize the WebAuthn session data for the finish handler.
	sessionDataJSON, err := json.Marshal(session)
	if err != nil {
		log.Printf("auth login begin: marshal session data: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal error"})
		return
	}

	// Persist the challenge state.
	// Serialize allowed credential IDs as JSON for the challenge_state table.
	allowedCredIDs := make([]string, 0, len(session.AllowedCredentialIDs))
	for _, id := range session.AllowedCredentialIDs {
		allowedCredIDs = append(allowedCredIDs, string(id))
	}
	allowedCredsJSON, _ := json.Marshal(allowedCredIDs)

	challengeState := &ChallengeState{
		ID:                  session.Challenge,
		UserID:              user.ID,
		Challenge:           session.Challenge,
		Type:                "login",
		AllowedCredentials:  string(allowedCredsJSON),
		WebAuthnSessionData: string(sessionDataJSON),
		CreatedAt:           time.Now().UTC(),
		ExpiresAt:           time.Now().UTC().Add(SessionChallengeTTL),
	}
	if err := h.store.SaveChallengeState(challengeState); err != nil {
		log.Printf("auth login begin: save challenge: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save challenge"})
		return
	}

	writeJSON(w, http.StatusOK, assertion)
}

// HandleRegisterFinish handles POST /auth/register/finish.
// It validates the WebAuthn registration response against the stored challenge,
// stores the credential, deletes the challenge (replay protection), and issues
// cookie-backed auth state.
func (h *Handler) HandleRegisterFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}

	// Read and buffer the request body so we can extract the challenge and
	// still let the WebAuthn library parse the full response.
	body, err := readBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	// Extract the challenge from the WebAuthn response to look up the session.
	challengeID, err := extractChallengeFromBody(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid webauthn response: " + err.Error()})
		return
	}

	// Look up the challenge state.
	cs, err := h.store.GetChallengeStateByID(challengeID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "challenge not found or already used"})
		return
	}

	// Validate challenge type.
	if cs.Type != "registration" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "challenge type mismatch"})
		return
	}

	// Check challenge not expired.
	if time.Now().UTC().After(cs.ExpiresAt) {
		_ = h.store.DeleteChallengeStateByID(cs.ID)
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "challenge expired"})
		return
	}

	// Delete the challenge immediately to prevent replay, regardless of whether
	// the WebAuthn verification succeeds. This ensures that a replayed finish
	// payload cannot find the challenge again.
	if err := h.store.DeleteChallengeStateByID(cs.ID); err != nil {
		log.Printf("auth register finish: delete challenge: %v", err)
		// Continue anyway — the challenge is consumed.
	}

	// Deserialize the WebAuthn session data.
	if cs.WebAuthnSessionData == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "challenge has no session data"})
		return
	}

	var sessionData webauthn.SessionData
	if err := json.Unmarshal([]byte(cs.WebAuthnSessionData), &sessionData); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid session data"})
		return
	}

	// Look up the user.
	user, err := h.store.GetUserByID(cs.UserID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "user not found"})
		return
	}

	// Build a WebAuthn user adapter.
	creds, _ := h.store.GetCredentialsByUserID(user.ID)
	waUser, err := newWebAuthnUser(user, creds)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "internal error"})
		return
	}

	// Parse the WebAuthn response from the buffered body.
	parsedResponse, err := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid registration response: " + err.Error()})
		return
	}

	// Verify the WebAuthn registration.
	credential, err := h.webauthn.CreateCredential(waUser, sessionData, parsedResponse)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "registration verification failed: " + err.Error()})
		return
	}

	// Store the verified credential in our persistence.
	dbCred := &Credential{
		ID:              string(credential.ID),
		UserID:          user.ID,
		PublicKey:       credential.PublicKey,
		AttestationType: credential.AttestationType,
		Transport:       transportListToJSON(credential.Transport),
		SignCount:       int64(credential.Authenticator.SignCount),
		AAGUID:          credential.Authenticator.AAGUID,
		Flags:           marshalCredentialFlags(credential.Flags),
		CreatedAt:       time.Now().UTC(),
	}
	if err := h.store.CreateCredential(dbCred); err != nil {
		log.Printf("auth register finish: store credential: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to store credential"})
		return
	}

	// Issue cookie-backed auth state.
	userInfo, err := h.issueSession(w, user)
	if err != nil {
		log.Printf("auth register finish: issue session: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to create session"})
		return
	}

	writeJSON(w, http.StatusOK, finishResponse{OK: true, User: userInfo})
}

// HandleLoginFinish handles POST /auth/login/finish.
// It validates the WebAuthn login response against the stored challenge,
// updates the credential sign count, deletes the challenge (replay protection),
// and issues cookie-backed auth state.
func (h *Handler) HandleLoginFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}

	// Read and buffer the request body.
	body, err := readBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	// Extract the challenge from the WebAuthn response to look up the session.
	challengeID, err := extractChallengeFromBody(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid webauthn response: " + err.Error()})
		return
	}

	// Look up the challenge state.
	cs, err := h.store.GetChallengeStateByID(challengeID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "challenge not found or already used"})
		return
	}

	// Validate challenge type.
	if cs.Type != "login" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "challenge type mismatch"})
		return
	}

	// Check challenge not expired.
	if time.Now().UTC().After(cs.ExpiresAt) {
		_ = h.store.DeleteChallengeStateByID(cs.ID)
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "challenge expired"})
		return
	}

	// Delete the challenge immediately to prevent replay.
	if err := h.store.DeleteChallengeStateByID(cs.ID); err != nil {
		log.Printf("auth login finish: delete challenge: %v", err)
	}

	// Deserialize the WebAuthn session data.
	if cs.WebAuthnSessionData == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "challenge has no session data"})
		return
	}

	var sessionData webauthn.SessionData
	if err := json.Unmarshal([]byte(cs.WebAuthnSessionData), &sessionData); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid session data"})
		return
	}

	// Look up the user.
	user, err := h.store.GetUserByID(cs.UserID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "user not found"})
		return
	}

	// Build a WebAuthn user adapter with credentials.
	creds, err := h.store.GetCredentialsByUserID(user.ID)
	if err != nil || len(creds) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "user has no credentials"})
		return
	}

	waUser, err := newWebAuthnUser(user, creds)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "internal error"})
		return
	}

	// Parse the WebAuthn assertion response from the buffered body.
	parsedResponse, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid login response: " + err.Error()})
		return
	}

	// Verify the WebAuthn login.
	credential, err := h.webauthn.ValidateLogin(waUser, sessionData, parsedResponse)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "login verification failed: " + err.Error()})
		return
	}

	// Update the credential sign count.
	if err := h.store.UpdateCredentialSignCount(string(credential.ID), int64(credential.Authenticator.SignCount)); err != nil {
		log.Printf("auth login finish: update sign count: %v", err)
		// Non-fatal — the session is still valid.
	}

	// Issue cookie-backed auth state.
	userInfo, err := h.issueSession(w, user)
	if err != nil {
		log.Printf("auth login finish: issue session: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to create session"})
		return
	}

	writeJSON(w, http.StatusOK, finishResponse{OK: true, User: userInfo})
}

// HandleSession handles GET /auth/session.
// It returns the current session state: signed-out for missing/bogus/expired
// auth state, or authenticated user info for a valid session.
// If the access JWT is expired but a valid refresh token exists, the handler
// rotates the refresh token and issues a new access JWT automatically.
func (h *Handler) HandleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}

	// Try to extract and validate the access JWT from the cookie.
	cookie, err := r.Cookie(AccessTokenCookieName)
	if err != nil || cookie.Value == "" {
		// No access cookie — try refresh rotation.
		h.tryRefreshRotation(w, r)
		return
	}

	// Parse and validate the JWT.
	token, err := jwt.Parse(cookie.Value, func(t *jwt.Token) (interface{}, error) {
		// Verify signing method is EdDSA (Ed25519).
		if t.Method != jwt.SigningMethodEdDSA {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return h.signer.Public(), nil
	})
	if err != nil {
		// JWT is invalid or expired — try refresh rotation.
		h.tryRefreshRotation(w, r)
		return
	}

	// Extract user ID from claims.
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		h.tryRefreshRotation(w, r)
		return
	}

	userID, ok := claims["sub"].(string)
	if !ok || userID == "" {
		h.tryRefreshRotation(w, r)
		return
	}

	// Look up the user to return non-secret info.
	user, err := h.store.GetUserByID(userID)
	if err != nil {
		// User not found — signed out.
		writeJSON(w, http.StatusOK, sessionResponse{Authenticated: false})
		return
	}

	writeJSON(w, http.StatusOK, sessionResponse{
		Authenticated: true,
		User: &userInfo{
			ID:        user.ID,
			Email:     user.Email,
			CreatedAt: user.CreatedAt.Format(time.RFC3339),
		},
	})
}

// tryRefreshRotation attempts to renew access via a valid refresh token.
// If the refresh token is valid and not expired, it rotates the refresh
// session, issues a new access JWT, and returns authenticated user info.
// If refresh is also invalid or missing, it returns signed-out state.
func (h *Handler) tryRefreshRotation(w http.ResponseWriter, r *http.Request) {
	rs, user, err := h.validateRefreshCookie(r)
	if err != nil {
		// No valid refresh — signed out.
		writeJSON(w, http.StatusOK, sessionResponse{Authenticated: false})
		return
	}

	// Rotate the refresh session and issue new cookies.
	if err := h.rotateRefreshSession(w, rs, user); err != nil {
		log.Printf("auth session: refresh rotation failed: %v", err)
		writeJSON(w, http.StatusOK, sessionResponse{Authenticated: false})
		return
	}

	writeJSON(w, http.StatusOK, sessionResponse{
		Authenticated: true,
		User: &userInfo{
			ID:        user.ID,
			Email:     user.Email,
			CreatedAt: user.CreatedAt.Format(time.RFC3339),
		},
	})
}

// HandleLogout handles POST /auth/logout.
// It invalidates the current authenticated state: deletes the refresh session
// from the store, clears both auth cookies, and returns a signed-out response.
// If the user is already signed out (no valid cookies), it returns a
// non-500 signed-out result so repeat logout is safe.
func (h *Handler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}

	// Best-effort: try to find the user ID from either the access JWT or the
	// refresh cookie so we can delete their refresh sessions from the store.
	userID := h.extractUserIDFromAuthCookies(r)

	// Delete all refresh sessions for the user (prevents silent restoration).
	if userID != "" {
		if err := h.store.DeleteRefreshSessionsByUserID(userID); err != nil {
			log.Printf("auth logout: delete refresh sessions for user %q: %v", userID, err)
			// Continue anyway — we still clear the cookies.
		}
	}

	// Clear both auth cookies by setting MaxAge=-1 with empty values.
	http.SetCookie(w, &http.Cookie{
		Name:     AccessTokenCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   h.config.CookieSecure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     RefreshTokenCookieName,
		Value:    "",
		Path:     "/auth",
		MaxAge:   -1,
		Secure:   h.config.CookieSecure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	writeJSON(w, http.StatusOK, sessionResponse{Authenticated: false})
}

// extractUserIDFromAuthCookies attempts to extract a user ID from the access
// JWT or the refresh cookie. It returns an empty string if neither cookie
// provides a valid user ID. This is used by logout to identify which user's
// refresh sessions to delete.
func (h *Handler) extractUserIDFromAuthCookies(r *http.Request) string {
	// Try the access JWT first.
	if cookie, err := r.Cookie(AccessTokenCookieName); err == nil && cookie.Value != "" {
		token, err := jwt.Parse(cookie.Value, func(t *jwt.Token) (interface{}, error) {
			if t.Method != jwt.SigningMethodEdDSA {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return h.signer.Public(), nil
		})
		if err == nil {
			if claims, ok := token.Claims.(jwt.MapClaims); ok {
				if sub, ok := claims["sub"].(string); ok && sub != "" {
					return sub
				}
			}
		}
	}

	// Fall back to the refresh cookie.
	if cookie, err := r.Cookie(RefreshTokenCookieName); err == nil && cookie.Value != "" {
		hash := sha256.Sum256([]byte(cookie.Value))
		hashHex := fmt.Sprintf("%x", hash)
		rs, err := h.store.GetRefreshSessionByTokenHash(hashHex)
		if err == nil {
			return rs.UserID
		}
	}

	return ""
}

// transportListToJSON converts a list of protocol.AuthenticatorTransport to
// a JSON-encoded string for storage in the credentials table.
func transportListToJSON(transports []protocol.AuthenticatorTransport) string {
	if len(transports) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(transports)
	return string(b)
}

// ValidateAccessToken validates an access JWT string and returns the user ID
// if valid, or an error if the token is invalid, expired, or tampered.
// This is used by the proxy to verify auth state.
func (h *Handler) ValidateAccessToken(tokenStr string) (string, error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if t.Method != jwt.SigningMethodEdDSA {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		// Verify the signing method is EdDSA (Ed25519).
		pub := h.signer.Public()
		if t.Method.Alg() != "EdDSA" {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Method.Alg())
		}
		return pub, nil
	})
	if err != nil {
		return "", fmt.Errorf("invalid access token: %w", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", errors.New("invalid token claims")
	}

	userID, ok := claims["sub"].(string)
	if !ok || userID == "" {
		return "", errors.New("invalid token subject")
	}

	// Check scope is "access".
	scope, _ := claims["scope"].(string)
	if scope != "access" {
		return "", errors.New("token is not an access token")
	}

	return userID, nil
}

// LoadPrivateKey loads the Ed25519 private key from the configured path.
func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := loadKeyFile(path)
	if err != nil {
		return nil, fmt.Errorf("load private key %s: %w", path, err)
	}

	// ssh-keygen writes OpenSSH private key format. Parse it.
	key, err := parseSSHPrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("parse private key %s: %w", path, err)
	}

	return key, nil
}

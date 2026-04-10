package auth

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

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
	store   *Store
	webauthn *webauthn.WebAuthn
	config  *Config
	signer  ed25519.PrivateKey // loaded at startup for JWT signing
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
	Username string `json:"username"`
}

// sessionResponse is the JSON response for GET /auth/session.
type sessionResponse struct {
	Authenticated bool        `json:"authenticated"`
	User          *userInfo   `json:"user,omitempty"`
	Error         string      `json:"error,omitempty"`
}

// userInfo contains non-secret user fields returned in session responses.
type userInfo struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	CreatedAt string `json:"created_at"`
}

// errorResponse is a generic JSON error envelope.
type errorResponse struct {
	Error string `json:"error"`
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
	defer r.Body.Close()

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

	if req.Username == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "username is required"})
		return
	}

	// Create the user in the store if they don't already exist.
	user, err := h.store.GetUserByUsername(req.Username)
	if err != nil {
		// User doesn't exist yet — create them.
		userID := uuid.NewString()
		user, err = h.store.CreateUser(userID, req.Username)
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

	// Persist the challenge state so the finish handler can verify it.
	challengeState := &ChallengeState{
		ID:        session.Challenge,
		UserID:    user.ID,
		Challenge: session.Challenge,
		Type:      "registration",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(SessionChallengeTTL),
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

	if req.Username == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "username is required"})
		return
	}

	// Look up the user.
	user, err := h.store.GetUserByUsername(req.Username)
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

	// Persist the challenge state.
	// Serialize allowed credential IDs as JSON for the challenge_state table.
	allowedCredIDs := make([]string, 0, len(session.AllowedCredentialIDs))
	for _, id := range session.AllowedCredentialIDs {
		allowedCredIDs = append(allowedCredIDs, string(id))
	}
	allowedCredsJSON, _ := json.Marshal(allowedCredIDs)

	challengeState := &ChallengeState{
		ID:                 session.Challenge,
		UserID:             user.ID,
		Challenge:          session.Challenge,
		Type:               "login",
		AllowedCredentials: string(allowedCredsJSON),
		CreatedAt:          time.Now().UTC(),
		ExpiresAt:          time.Now().UTC().Add(SessionChallengeTTL),
	}
	if err := h.store.SaveChallengeState(challengeState); err != nil {
		log.Printf("auth login begin: save challenge: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save challenge"})
		return
	}

	writeJSON(w, http.StatusOK, assertion)
}

// HandleSession handles GET /auth/session.
// It returns the current session state: signed-out for missing/bogus/expired
// auth state, or authenticated user info for a valid session.
func (h *Handler) HandleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}

	// Try to extract and validate the access JWT from the cookie.
	cookie, err := r.Cookie(AccessTokenCookieName)
	if err != nil {
		// No cookie — signed out.
		writeJSON(w, http.StatusOK, sessionResponse{Authenticated: false})
		return
	}

	if cookie.Value == "" {
		// Empty cookie value — signed out.
		writeJSON(w, http.StatusOK, sessionResponse{Authenticated: false})
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
		// Invalid, tampered, or expired — signed out.
		writeJSON(w, http.StatusOK, sessionResponse{Authenticated: false})
		return
	}

	// Extract user ID from claims.
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		writeJSON(w, http.StatusOK, sessionResponse{Authenticated: false})
		return
	}

	userID, ok := claims["sub"].(string)
	if !ok || userID == "" {
		writeJSON(w, http.StatusOK, sessionResponse{Authenticated: false})
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
			Username:  user.Username,
			CreatedAt: user.CreatedAt.Format(time.RFC3339),
		},
	})
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

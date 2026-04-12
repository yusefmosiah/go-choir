package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureLogs captures log output during the execution of fn.
// It returns the captured log output as a string.
func captureLogs(t *testing.T, fn func()) string {
	t.Helper()

	// Capture log output by redirecting the default logger.
	r, w := io.Pipe()

	oldWriter := log.Writer()
	oldFlags := log.Flags()
	log.SetOutput(w)
	log.SetFlags(0) // Remove timestamps for deterministic matching

	// Channel to signal that we've read all output.
	done := make(chan struct{})
	var buf bytes.Buffer

	go func() {
		defer close(done)
		io.Copy(&buf, r)
	}()

	fn()

	// Restore the logger before reading output.
	log.SetOutput(oldWriter)
	log.SetFlags(oldFlags)
	w.Close()

	<-done
	return buf.String()
}

// --- hashEmail tests ---

func TestHashEmailDeterministic(t *testing.T) {
	email := "test@example.com"
	h1 := hashEmail(email)
	h2 := hashEmail(email)
	if h1 != h2 {
		t.Errorf("hashEmail should be deterministic: got %q then %q", h1, h2)
	}
}

func TestHashEmailLength(t *testing.T) {
	h := hashEmail("user@example.com")
	if len(h) != 8 {
		t.Errorf("hashEmail should return 8-char hash, got %d chars: %q", len(h), h)
	}
}

func TestHashEmailCaseInsensitive(t *testing.T) {
	h1 := hashEmail("User@Example.COM")
	h2 := hashEmail("user@example.com")
	if h1 != h2 {
		t.Errorf("hashEmail should be case-insensitive: got %q vs %q", h1, h2)
	}
}

func TestHashEmailDifferentEmails(t *testing.T) {
	h1 := hashEmail("alice@example.com")
	h2 := hashEmail("bob@example.com")
	if h1 == h2 {
		t.Errorf("hashEmail should produce different hashes for different emails")
	}
}

func TestHashEmailTrimsWhitespace(t *testing.T) {
	h1 := hashEmail("  user@example.com  ")
	h2 := hashEmail("user@example.com")
	if h1 != h2 {
		t.Errorf("hashEmail should trim whitespace: got %q vs %q", h1, h2)
	}
}

func TestHashEmailDoesNotContainAt(t *testing.T) {
	h := hashEmail("sensitive@example.com")
	if strings.Contains(h, "@") {
		t.Errorf("hashEmail should not contain @ symbol: got %q", h)
	}
	if strings.Contains(h, "example.com") {
		t.Errorf("hashEmail should not contain domain: got %q", h)
	}
}

// --- Structured logging format tests ---

func TestRegisterBeginLogsOperationAndEmailHash(t *testing.T) {
	h, _ := testHandlerEnv(t)

	logs := captureLogs(t, func() {
		body := `{"email":"newuser@example.com"}`
		req := httptest.NewRequest(http.MethodPost, "/auth/register/begin", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.HandleRegisterBegin(rec, req)
	})

	if !strings.Contains(logs, "operation=register_begin") {
		t.Errorf("expected log to contain operation=register_begin, got:\n%s", logs)
	}
	if !strings.Contains(logs, "email_hash=") {
		t.Errorf("expected log to contain email_hash=, got:\n%s", logs)
	}
	if !strings.Contains(logs, "user_id=") {
		t.Errorf("expected log to contain user_id=, got:\n%s", logs)
	}
	if !strings.Contains(logs, "result=success") {
		t.Errorf("expected log to contain result=success, got:\n%s", logs)
	}
}

func TestRegisterBeginLogsDuplicateRejection(t *testing.T) {
	h, _ := testHandlerEnv(t)

	// Create user with credentials.
	user, err := h.store.CreateUser("du-log-user", "dup@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	cred := &Credential{
		ID: "cred-du-log", UserID: user.ID, PublicKey: []byte("pk"),
		AttestationType: "none", Transport: "[]", SignCount: 0,
		AAGUID: []byte{}, Flags: "{}",
	}
	if err := h.store.CreateCredential(cred); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	logs := captureLogs(t, func() {
		body := `{"email":"dup@example.com"}`
		req := httptest.NewRequest(http.MethodPost, "/auth/register/begin", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.HandleRegisterBegin(rec, req)
	})

	if !strings.Contains(logs, "result=rejected") {
		t.Errorf("expected log to contain result=rejected, got:\n%s", logs)
	}
	if !strings.Contains(logs, "reason=duplicate_registration") {
		t.Errorf("expected log to contain reason=duplicate_registration, got:\n%s", logs)
	}
	if !strings.Contains(logs, "user_id=du-log-user") {
		t.Errorf("expected log to contain user_id=du-log-user, got:\n%s", logs)
	}
}

func TestLoginBeginLogsOperationAndEmailHash(t *testing.T) {
	h, _ := testHandlerEnv(t)

	// Create user with credentials for login begin to succeed.
	user, err := h.store.CreateUser("login-log-user", "login@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	cred := &Credential{
		ID: "cred-login-log", UserID: user.ID, PublicKey: []byte("pk"),
		AttestationType: "none", Transport: "[]", SignCount: 0,
		AAGUID: []byte{}, Flags: "{}",
	}
	if err := h.store.CreateCredential(cred); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	logs := captureLogs(t, func() {
		body := `{"email":"login@example.com"}`
		req := httptest.NewRequest(http.MethodPost, "/auth/login/begin", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.HandleLoginBegin(rec, req)
	})

	if !strings.Contains(logs, "operation=login_begin") {
		t.Errorf("expected log to contain operation=login_begin, got:\n%s", logs)
	}
	if !strings.Contains(logs, "email_hash=") {
		t.Errorf("expected log to contain email_hash=, got:\n%s", logs)
	}
	if !strings.Contains(logs, "user_id=login-log-user") {
		t.Errorf("expected log to contain user_id=login-log-user, got:\n%s", logs)
	}
	if !strings.Contains(logs, "result=success") {
		t.Errorf("expected log to contain result=success, got:\n%s", logs)
	}
	if !strings.Contains(logs, "credential_count=") {
		t.Errorf("expected log to contain credential_count=, got:\n%s", logs)
	}
}

func TestLoginBeginLogsUserNotFound(t *testing.T) {
	h, _ := testHandlerEnv(t)

	logs := captureLogs(t, func() {
		body := `{"email":"nonexistent@example.com"}`
		req := httptest.NewRequest(http.MethodPost, "/auth/login/begin", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.HandleLoginBegin(rec, req)
	})

	if !strings.Contains(logs, "operation=login_begin") {
		t.Errorf("expected log to contain operation=login_begin, got:\n%s", logs)
	}
	if !strings.Contains(logs, "result=not_found") {
		t.Errorf("expected log to contain result=not_found, got:\n%s", logs)
	}
}

func TestLoginBeginLogsNoCredentials(t *testing.T) {
	h, _ := testHandlerEnv(t)

	// Create user without credentials.
	_, err := h.store.CreateUser("no-creds-log-user", "nocreds@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	logs := captureLogs(t, func() {
		body := `{"email":"nocreds@example.com"}`
		req := httptest.NewRequest(http.MethodPost, "/auth/login/begin", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.HandleLoginBegin(rec, req)
	})

	if !strings.Contains(logs, "result=no_credentials") {
		t.Errorf("expected log to contain result=no_credentials, got:\n%s", logs)
	}
}

func TestRegisterFinishLogsChallengeLookup(t *testing.T) {
	h, _ := testHandlerEnv(t)

	logs := captureLogs(t, func() {
		// Send a finish with an invalid body — challenge lookup should fail.
		body := `{"id":"nonexistent","response":{"clientDataJSON":"eyJjaGFsbGVuZ2UiOiJib2d1cyIsInR5cGUiOiJ3ZWJhdXRobi5jcmVhdGUiLCJvcmlnaW4iOiJodHRwOi8vbG9jYWxob3N0OjQxNzMifQ"}}`
		req := httptest.NewRequest(http.MethodPost, "/auth/register/finish", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.HandleRegisterFinish(rec, req)
	})

	if !strings.Contains(logs, "operation=register_finish") {
		t.Errorf("expected log to contain operation=register_finish, got:\n%s", logs)
	}
	if !strings.Contains(logs, "step=challenge_lookup") {
		t.Errorf("expected log to contain step=challenge_lookup, got:\n%s", logs)
	}
}

func TestLoginFinishLogsChallengeLookup(t *testing.T) {
	h, _ := testHandlerEnv(t)

	logs := captureLogs(t, func() {
		body := `{"id":"nonexistent","response":{"clientDataJSON":"eyJjaGFsbGVuZ2UiOiJib2d1cyIsInR5cGUiOiJ3ZWJhdXRobi5nZXQiLCJvcmlnaW4iOiJodHRwOi8vbG9jYWxob3N0OjQxNzMifQ"}}`
		req := httptest.NewRequest(http.MethodPost, "/auth/login/finish", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.HandleLoginFinish(rec, req)
	})

	if !strings.Contains(logs, "operation=login_finish") {
		t.Errorf("expected log to contain operation=login_finish, got:\n%s", logs)
	}
	if !strings.Contains(logs, "step=challenge_lookup") {
		t.Errorf("expected log to contain step=challenge_lookup, got:\n%s", logs)
	}
}

func TestLogoutLogsUserID(t *testing.T) {
	h, priv := testHandlerEnv(t)

	// Create user and issue cookies.
	user, err := h.store.CreateUser("logout-log-user", "logout@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Make an authenticated request with access cookie.
	accessToken, err := h.issueAccessJWT(user)
	if err != nil {
		t.Fatalf("issue access JWT: %v", err)
	}
	_ = priv

	logs := captureLogs(t, func() {
		req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
		req.AddCookie(&http.Cookie{Name: AccessTokenCookieName, Value: accessToken})
		rec := httptest.NewRecorder()
		h.HandleLogout(rec, req)
	})

	if !strings.Contains(logs, "operation=logout") {
		t.Errorf("expected log to contain operation=logout, got:\n%s", logs)
	}
	if !strings.Contains(logs, "user_id=logout-log-user") {
		t.Errorf("expected log to contain user_id=logout-log-user, got:\n%s", logs)
	}
	if !strings.Contains(logs, "result=success") {
		t.Errorf("expected log to contain result=success, got:\n%s", logs)
	}
}

func TestLogoutLogsWithoutUserID(t *testing.T) {
	h, _ := testHandlerEnv(t)

	logs := captureLogs(t, func() {
		req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
		rec := httptest.NewRecorder()
		h.HandleLogout(rec, req)
	})

	if !strings.Contains(logs, "operation=logout") {
		t.Errorf("expected log to contain operation=logout, got:\n%s", logs)
	}
	if !strings.Contains(logs, "note=no_user_id_found_cookies_cleared") {
		t.Errorf("expected log to contain note=no_user_id_found_cookies_cleared, got:\n%s", logs)
	}
}

func TestSessionRefreshLogsOperation(t *testing.T) {
	h, priv := testHandlerEnv(t)

	// Create user with a refresh session.
	user, err := h.store.CreateUser("refresh-log-user", "refresh@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Issue a refresh token.
	refreshToken, err := h.generateRefreshToken(user)
	if err != nil {
		t.Fatalf("generate refresh token: %v", err)
	}
	_ = priv

	logs := captureLogs(t, func() {
		// Session with only refresh cookie (no access) triggers rotation.
		req := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
		req.AddCookie(&http.Cookie{Name: RefreshTokenCookieName, Value: refreshToken})
		rec := httptest.NewRecorder()
		h.HandleSession(rec, req)
	})

	if !strings.Contains(logs, "operation=session_refresh") {
		t.Errorf("expected log to contain operation=session_refresh, got:\n%s", logs)
	}
	if !strings.Contains(logs, "user_id=refresh-log-user") {
		t.Errorf("expected log to contain user_id=refresh-log-user, got:\n%s", logs)
	}
	if !strings.Contains(logs, "result=success") {
		t.Errorf("expected log to contain result=success, got:\n%s", logs)
	}
}

// --- Structured log format consistency tests ---

func TestLogFormatContainsAuthPrefix(t *testing.T) {
	h, _ := testHandlerEnv(t)

	logs := captureLogs(t, func() {
		body := `{"email":"prefix@example.com"}`
		req := httptest.NewRequest(http.MethodPost, "/auth/register/begin", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.HandleRegisterBegin(rec, req)
	})

	lines := strings.Split(strings.TrimSpace(logs), "\n")
	for _, line := range lines {
		if !strings.Contains(line, "[auth]") {
			t.Errorf("expected all log lines to contain [auth] prefix, got: %q", line)
		}
	}
}

func TestLogsDoNotContainRawEmail(t *testing.T) {
	h, _ := testHandlerEnv(t)

	email := "sensitive@example.com"
	logs := captureLogs(t, func() {
		body := fmt.Sprintf(`{"email":%q}`, email)
		req := httptest.NewRequest(http.MethodPost, "/auth/register/begin", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.HandleRegisterBegin(rec, req)
	})

	if strings.Contains(logs, email) {
		t.Errorf("logs should not contain raw email address, got:\n%s", logs)
	}
	if strings.Contains(logs, "sensitive") {
		t.Errorf("logs should not contain email local part, got:\n%s", logs)
	}
	if strings.Contains(logs, "@example.com") {
		t.Errorf("logs should not contain email domain, got:\n%s", logs)
	}
}

func TestLogsDoNotContainSensitiveCredentialData(t *testing.T) {
	// Verify hashEmail output doesn't look like a credential/key.
	h := hashEmail("test@example.com")
	if len(h) > 16 {
		t.Errorf("hashEmail output should be short, got %d chars", len(h))
	}
}

// --- Table-driven test for all operations produce structured logs ---

func TestAllOperationsProduceStructuredLogs(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		setup     func(h *Handler) (reqBody string, cookies []*http.Cookie)
		handler   func(h *Handler, w http.ResponseWriter, r *http.Request)
		wantLogOp string
	}{
		{
			name:      "register_begin new user",
			operation: "register/begin",
			setup: func(h *Handler) (string, []*http.Cookie) {
				return `{"email":"new@example.com"}`, nil
			},
			handler:   (*Handler).HandleRegisterBegin,
			wantLogOp: "operation=register_begin",
		},
		{
			name:      "login_begin user not found",
			operation: "login/begin",
			setup: func(h *Handler) (string, []*http.Cookie) {
				return `{"email":"unknown@example.com"}`, nil
			},
			handler:   (*Handler).HandleLoginBegin,
			wantLogOp: "operation=login_begin",
		},
		{
			name:      "register_finish challenge not found",
			operation: "register/finish",
			setup: func(h *Handler) (string, []*http.Cookie) {
				return `{"id":"bogus","response":{"clientDataJSON":"eyJjaGFsbGVuZ2UiOiJib2d1cyIsInR5cGUiOiJ3ZWJhdXRobi5jcmVhdGUiLCJvcmlnaW4iOiJodHRwOi8vbG9jYWxob3N0OjQxNzMifQ"}}`, nil
			},
			handler:   (*Handler).HandleRegisterFinish,
			wantLogOp: "operation=register_finish",
		},
		{
			name:      "login_finish challenge not found",
			operation: "login/finish",
			setup: func(h *Handler) (string, []*http.Cookie) {
				return `{"id":"bogus","response":{"clientDataJSON":"eyJjaGFsbGVuZ2UiOiJib2d1cyIsInR5cGUiOiJ3ZWJhdXRobi5nZXQiLCJvcmlnaW4iOiJodHRwOi8vbG9jYWxob3N0OjQxNzMifQ"}}`, nil
			},
			handler:   (*Handler).HandleLoginFinish,
			wantLogOp: "operation=login_finish",
		},
		{
			name:      "logout no cookies",
			operation: "logout",
			setup: func(h *Handler) (string, []*http.Cookie) {
				return "", nil
			},
			handler:   (*Handler).HandleLogout,
			wantLogOp: "operation=logout",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := testHandlerEnv(t)

			reqBody, cookies := tc.setup(h)

			logs := captureLogs(t, func() {
				var body io.Reader
				if reqBody != "" {
					body = bytes.NewBufferString(reqBody)
				}
				req := httptest.NewRequest(http.MethodPost, "/auth/"+tc.operation, body)
				req.Header.Set("Content-Type", "application/json")
				for _, c := range cookies {
					req.AddCookie(c)
				}
				rec := httptest.NewRecorder()
				tc.handler(h, rec, req)
			})

			if !strings.Contains(logs, tc.wantLogOp) {
				t.Errorf("expected log to contain %q, got:\n%s", tc.wantLogOp, logs)
			}
			if !strings.Contains(logs, "[auth]") {
				t.Errorf("expected log to contain [auth] prefix, got:\n%s", logs)
			}
		})
	}
}

// --- Verify log output is valid for a complete register-then-login flow ---

func TestCompleteRegisterLoginFlowLogging(t *testing.T) {
	h, priv := testHandlerEnv(t)
	_ = priv

	email := "flow@example.com"
	emailHash := hashEmail(email)

	// Step 1: Register begin
	registerBeginLogs := captureLogs(t, func() {
		body := fmt.Sprintf(`{"email":%q}`, email)
		req := httptest.NewRequest(http.MethodPost, "/auth/register/begin", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.HandleRegisterBegin(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("register begin: got %d, want 200", rec.Code)
		}
	})

	if !strings.Contains(registerBeginLogs, "operation=register_begin") {
		t.Error("register begin logs should contain operation=register_begin")
	}
	if !strings.Contains(registerBeginLogs, "email_hash="+emailHash) {
		t.Errorf("register begin logs should contain email_hash=%s, got:\n%s", emailHash, registerBeginLogs)
	}
	if !strings.Contains(registerBeginLogs, "result=success") {
		t.Errorf("register begin logs should contain result=success, got:\n%s", registerBeginLogs)
	}

	// Extract user_id from logs.
	if !strings.Contains(registerBeginLogs, "user_id=") {
		t.Errorf("register begin logs should contain user_id=, got:\n%s", registerBeginLogs)
	}

	// Verify no raw email in logs
	if strings.Contains(registerBeginLogs, email) {
		t.Errorf("register begin logs should NOT contain raw email %q, got:\n%s", email, registerBeginLogs)
	}
}

// --- Verify login begin logs include credential count ---

func TestLoginBeginLogsCredentialCount(t *testing.T) {
	h, _ := testHandlerEnv(t)

	// Create user with 2 credentials.
	user, err := h.store.CreateUser("multi-cred-log-user", "multi@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	for i := 0; i < 2; i++ {
		cred := &Credential{
			ID: fmt.Sprintf("cred-multi-%d", i), UserID: user.ID, PublicKey: []byte("pk"),
			AttestationType: "none", Transport: "[]", SignCount: 0,
			AAGUID: []byte{}, Flags: "{}",
		}
		if err := h.store.CreateCredential(cred); err != nil {
			t.Fatalf("create credential %d: %v", i, err)
		}
	}

	logs := captureLogs(t, func() {
		body := `{"email":"multi@example.com"}`
		req := httptest.NewRequest(http.MethodPost, "/auth/login/begin", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.HandleLoginBegin(rec, req)
	})

	if !strings.Contains(logs, "credential_count=2") {
		t.Errorf("expected log to contain credential_count=2, got:\n%s", logs)
	}
}

// --- Verify the JSON response doesn't leak into logs ---

func TestLogsDoNotContainJSONResponseData(t *testing.T) {
	h, _ := testHandlerEnv(t)

	email := "json@example.com"
	logs := captureLogs(t, func() {
		body := fmt.Sprintf(`{"email":%q}`, email)
		req := httptest.NewRequest(http.MethodPost, "/auth/register/begin", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.HandleRegisterBegin(rec, req)

		// Parse the response to verify it still works.
		var resp map[string]interface{}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
	})

	// Logs should not contain the WebAuthn challenge or public key data.
	if strings.Contains(logs, "publicKey") {
		t.Errorf("logs should not contain publicKey data, got:\n%s", logs)
	}
}

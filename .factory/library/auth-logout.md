# Auth Logout Implementation

## Overview

The `POST /auth/logout` route is implemented in `internal/auth/handlers.go` and registered in `cmd/auth/main.go`. It completes the Milestone 1 `/auth/*` route set.

## Behavior

- **Method**: POST only (other methods get 405 JSON)
- **Invalidation**: Extracts the user ID from the access JWT (primary) or refresh cookie (fallback), then deletes ALL refresh sessions for that user from the store. This prevents silent session restoration via refresh rotation after logout.
- **Cookie clearing**: Sets both `choir_access` and `choir_refresh` cookies with `MaxAge=-1` and empty values, matching the same path/Secure/HttpOnly/SameSite attributes used when they were originally set.
- **Signed-out response**: Returns `{"authenticated":false}` with 200 status, even when no valid cookies are present (safe repeat).
- **Bogus cookies**: Handled gracefully — no 5xx, no panic, just returns signed-out and clears cookies.

## Design Notes

- The access JWT is self-contained (signed Ed25519 JWT with no server-side revocation list in Milestone 1). After logout, the old access JWT remains technically valid until it expires naturally. However, the refresh session is fully deleted, so:
  - When the access JWT expires, `/auth/session` will fall through to refresh rotation, which fails because the refresh session is gone.
  - The proxy `ValidateAccessToken` will also reject the expired JWT.
  - A server-side token revocation list is an expected future enhancement but is out of scope for Milestone 1.
- The handler extracts user ID via `extractUserIDFromAuthCookies`, which tries the access JWT first then falls back to the refresh cookie. This ensures that even if the access JWT is expired, we can still find and delete the user's refresh sessions if the refresh cookie is still present.

## Test Coverage

Six test cases in `internal/auth/handlers_test.go`:
1. `TestLogoutRejectsNonPost` — non-POST methods return 405
2. `TestLogoutReturnsSignedOutWhenAlreadySignedOut` — no cookies, non-5xx signed-out result
3. `TestLogoutInvalidatesAuthenticatedSession` — full integration: authenticated session → logout → refresh session deleted → expired-access-with-old-refresh cannot restore
4. `TestLogoutRepeatIsSafe` — consecutive logouts with no cookies both return non-5xx signed-out
5. `TestLogoutWithBogusCookiesIsSafe` — invalid cookie values don't cause 5xx
6. `TestLogoutThenSessionReportsSignedOut` — post-logout `/auth/session` with cleared cookies reports signed-out

## Complete /auth/* Route Set

All six Milestone 1 auth routes are now registered:
- `POST /auth/register/begin`
- `POST /auth/register/finish`
- `POST /auth/login/begin`
- `POST /auth/login/finish`
- `GET /auth/session`
- `POST /auth/logout`

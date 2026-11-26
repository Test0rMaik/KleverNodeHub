package auth

import (
	"context"
	"net/http"
	"strings"
	"time"
)

type contextKey string

const claimsContextKey contextKey = "jwt_claims"

// Middleware creates an HTTP middleware that validates JWT tokens.
// Tokens are read from the "Authorization: Bearer <token>" header
// or from an httpOnly cookie named "access_token".
func Middleware(jm *JWTManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractToken(r)
			if token == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			claims, err := jm.ValidateAccessToken(token)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), claimsContextKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Authenticated reports whether r carries a valid access token, for public
// routes whose behaviour depends on whether the caller is logged in.
func Authenticated(jm *JWTManager, r *http.Request) bool {
	token := extractToken(r)
	if token == "" {
		return false
	}
	_, err := jm.ValidateAccessToken(token)
	return err == nil
}

// RecentlyAuthenticated reports whether r carries a valid access token whose
// user logged in (not just refreshed) within maxAge. Required before adding a
// login method, so a stolen session can't make itself permanent.
func RecentlyAuthenticated(jm *JWTManager, r *http.Request, maxAge time.Duration) bool {
	token := extractToken(r)
	if token == "" {
		return false
	}
	claims, err := jm.ValidateAccessToken(token)
	if err != nil || claims.AuthTime == 0 {
		return false
	}
	return time.Since(time.Unix(claims.AuthTime, 0)) <= maxAge
}

// RequestAuthTime returns the login time of r's access token, or 0.
func RequestAuthTime(jm *JWTManager, r *http.Request) int64 {
	token := extractToken(r)
	if token == "" {
		return 0
	}
	claims, err := jm.ValidateAccessToken(token)
	if err != nil {
		return 0
	}
	return claims.AuthTime
}

// ClaimsFromContext extracts JWT claims from the request context.
// Returns nil if no claims are present.
func ClaimsFromContext(ctx context.Context) *JWTClaims {
	claims, ok := ctx.Value(claimsContextKey).(*JWTClaims)
	if !ok {
		return nil
	}
	return claims
}

func extractToken(r *http.Request) string {
	// Try Authorization header first
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}

	// Fall back to cookie
	cookie, err := r.Cookie("access_token")
	if err == nil && cookie.Value != "" {
		return cookie.Value
	}

	return ""
}

package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	accessTokenTTL = 15 * time.Minute
	refreshTokenTTL = 7 * 24 * time.Hour
)

type contextKey string

const ctxUserID contextKey = "userID"

// -----------------------------------------------------------------------
// Token generation
// -----------------------------------------------------------------------

func jwtSecret() []byte {
	s := os.Getenv("JWT_SECRET")
	if s == "" {
		panic("JWT_SECRET environment variable not set")
	}
	return []byte(s)
}

// makeAccessToken creates a signed HS256 JWT with the user's id as subject.
func makeAccessToken(userID int64) (string, error) {
	claims := jwt.RegisteredClaims{
		Subject:   fmt.Sprintf("%d", userID),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(accessTokenTTL)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(jwtSecret())
}

// makeRefreshToken creates a longer-lived token used only to re-issue access tokens.
func makeRefreshToken(userID int64) (string, error) {
	claims := jwt.RegisteredClaims{
		Subject:   fmt.Sprintf("%d", userID),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(refreshTokenTTL)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(jwtSecret())
}

// parseToken validates a signed JWT and returns the user ID embedded in it.
func parseToken(tokenStr string) (int64, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &jwt.RegisteredClaims{},
		func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return jwtSecret(), nil
		},
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return 0, err
	}

	claims, ok := token.Claims.(*jwt.RegisteredClaims)
	if !ok || !token.Valid {
		return 0, errors.New("invalid token claims")
	}

	var id int64
	if _, err := fmt.Sscanf(claims.Subject, "%d", &id); err != nil {
		return 0, fmt.Errorf("invalid subject: %w", err)
	}
	return id, nil
}

// -----------------------------------------------------------------------
// Middleware
// -----------------------------------------------------------------------

// requireAuth extracts the Bearer token from the Authorization header,
// validates it, and injects the userID into the request context.
// Returns 401 if the token is missing or invalid.
func requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "missing or malformed Authorization header")
			return
		}
		tokenStr := strings.TrimPrefix(header, "Bearer ")

		userID, err := parseToken(tokenStr)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}

		ctx := context.WithValue(r.Context(), ctxUserID, userID)
		next(w, r.WithContext(ctx))
	}
}

// userIDFromCtx retrieves the authenticated user's id from the context.
// Panics if called outside of requireAuth — that would be a programming error.
func userIDFromCtx(ctx context.Context) int64 {
	return ctx.Value(ctxUserID).(int64)
}

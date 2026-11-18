package main

// ===========================================================================
// TWO KINDS OF IDENTITY — keep them separate in your head and in your code.
//
//   USER identity     "this request is from customer alice"
//                     proven by: the customer's access token (OAuth2 / JWT)
//                     checked by: the GATEWAY, once, at the edge
//
//   SERVICE identity  "this request is from the payments service"
//                     proven by: a service token (here) or an mTLS client cert
//                     (in a service mesh like Istio/Linkerd)
//                     checked by: EVERY service, on every internal call
//
// The two use DIFFERENT signing keys and DIFFERENT audiences. If they shared
// one key, a stolen customer token could be replayed against an internal
// "/debit" endpoint — a "token confusion" attack.
//
// HS256 (shared secret) keeps this demo self-contained. In production use
// RS256/ES256: the identity provider signs with a private key and services
// verify with the public key fetched from a JWKS URL, so no service can mint
// tokens it should only be verifying.
// ===========================================================================

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	userIssuer      = "bank-idp"         // who issues customer tokens
	userAudience    = "bank-public-api"  // customer tokens are only valid at the gateway
	internalIssuer  = "bank-internal-ca" // who issues service tokens
	serviceTokenTTL = time.Minute        // short life: a leaked token expires fast
)

// UserClaims is what the identity provider puts in a customer's token.
type UserClaims struct {
	Scope string `json:"scope"`
	jwt.RegisteredClaims
}

// MintUserToken stands in for the bank's identity provider (login + OTP +
// device binding would happen there). The gateway never mints these.
func MintUserToken(key []byte, userID, scope string, ttl time.Duration) string {
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, UserClaims{
		Scope: scope,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    userIssuer,
			Subject:   userID,
			Audience:  jwt.ClaimStrings{userAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	})
	s, _ := tok.SignedString(key)
	return s
}

func verifyUserToken(key []byte, raw string) (*UserClaims, error) {
	claims := &UserClaims{}
	_, err := jwt.ParseWithClaims(raw, claims,
		func(*jwt.Token) (any, error) { return key, nil },
		// Pinning the algorithm blocks the "alg: none" and RS->HS swap attacks.
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(userIssuer),
		jwt.WithAudience(userAudience),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(5*time.Second), // tolerate small clock drift between machines
	)
	if err != nil {
		return nil, err
	}
	return claims, nil
}

func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(h, "Bearer ")
	return tok, ok && tok != ""
}

// ---------------------------------------------------------------------------
// SERVICE-TO-SERVICE AUTH
// ---------------------------------------------------------------------------

type ServiceAuth struct{ key []byte }

// Mint creates a token saying "I am <caller> and this is meant for <target>".
// The audience matters: a token minted for "notifications" must not work
// against "accounts", or one compromised low-value service unlocks them all.
func (a ServiceAuth) Mint(caller, target string) string {
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Issuer:    internalIssuer,
		Subject:   caller,
		Audience:  jwt.ClaimStrings{target},
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(serviceTokenTTL)),
	})
	s, _ := tok.SignedString(a.key)
	return s
}

// Authenticate is middleware every internal service runs FIRST. It answers
// "which service is calling me?" and only then trusts headers like
// X-User-ID — those are only as trustworthy as whoever sent them.
func (a ServiceAuth) Authenticate(self string) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := bearer(r)
			if !ok {
				writeError(w, r, http.StatusUnauthorized, "missing_service_token", "internal endpoint: service token required")
				return
			}
			claims := &jwt.RegisteredClaims{}
			_, err := jwt.ParseWithClaims(raw, claims,
				func(*jwt.Token) (any, error) { return a.key, nil },
				jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
				jwt.WithIssuer(internalIssuer),
				jwt.WithAudience(self),
				jwt.WithExpirationRequired(),
			)
			if err != nil {
				code := "invalid_service_token"
				if errors.Is(err, jwt.ErrTokenInvalidAudience) {
					code = "wrong_audience"
				}
				writeError(w, r, http.StatusUnauthorized, code, err.Error())
				return
			}
			ctx := withValue(r.Context(), ctxCaller, claims.Subject)
			// Safe to read these now: the caller is a verified internal service.
			ctx = withValue(ctx, ctxRequestID, r.Header.Get(HeaderRequestID))
			ctx = withValue(ctx, ctxUserID, r.Header.Get(HeaderUserID))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// allow is per-route AUTHORIZATION for services: authentication said "you
// are the gateway", this says "the gateway may not call /debit".
// Least privilege: only payments can move money.
func allow(callers []string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !slices.Contains(callers, callerFrom(r.Context())) {
			writeError(w, r, http.StatusForbidden, "caller_not_allowed",
				"service "+callerFrom(r.Context())+" may not call "+r.URL.Path)
			return
		}
		h(w, r)
	})
}

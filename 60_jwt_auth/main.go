package main

// ============================================================================
// CONCEPT: JWT authentication in Go with golang-jwt/jwt/v5.
//
// A JWT is three base64url chunks joined by dots:
//
//	header . payload . signature
//	{"alg":"HS256","typ":"JWT"} . {"sub":"u1","exp":...} . HMAC(header.payload, secret)
//
// The payload is SIGNED, not ENCRYPTED. Anyone can read it (paste it into
// jwt.io). So: never put a password, a card number or anything private in a
// JWT. The signature only proves "this server issued it and nobody edited it".
//
// What this lesson builds, end to end, and then exercises live:
//
//	POST /login      -> bcrypt-verify password, issue access + refresh tokens
//	GET  /me         -> any logged-in user            (auth middleware)
//	GET  /admin      -> admin role only               (auth + RBAC middleware)
//	POST /refresh    -> swap a refresh token for a new access token (with rotation)
//	POST /logout     -> revoke the refresh token (the only real logout there is)
//
// Run: go run ./60_jwt_auth
// ============================================================================

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// ---------------------------------------------------------------------------
// 1. CLAIMS
//
// jwt.RegisteredClaims gives you the standard fields (sub, exp, iat, nbf, iss,
// aud, jti). Embed it and add your own. Keep custom claims SMALL -- the token
// travels on every request, and browsers/proxies cap header size (~8KB).
//
// Do NOT put the whole user object in here. Put the id and the role, then load
// the rest from your DB/cache. A JWT is a claim about identity, not a cache.
// ---------------------------------------------------------------------------

type AccessClaims struct {
	Role  string `json:"role"`
	Email string `json:"email"`
	jwt.RegisteredClaims
}

// ---------------------------------------------------------------------------
// 2. THE TOKEN SERVICE
// ---------------------------------------------------------------------------

const (
	accessTTL  = 15 * time.Minute // short: it cannot be revoked, so it must expire fast
	refreshTTL = 7 * 24 * time.Hour
	issuer     = "go-basics.auth"
	audience   = "go-basics.api"
)

type TokenService struct {
	secret []byte // HS256 shared secret -- from config/env, NEVER hard-coded

	// Refresh tokens are OPAQUE random strings stored server-side, not JWTs.
	// That is deliberate: a stateful refresh token can be revoked instantly,
	// which is the whole point of having one.
	mu      sync.Mutex
	refresh map[string]refreshRecord
	revoked map[string]bool // jti blacklist for access tokens (optional, see notes)
}

type refreshRecord struct {
	UserID    string
	ExpiresAt time.Time
}

func NewTokenService(secret []byte) *TokenService {
	return &TokenService{
		secret:  secret,
		refresh: map[string]refreshRecord{},
		revoked: map[string]bool{},
	}
}

// NewAccessToken signs a short-lived JWT.
func (ts *TokenService) NewAccessToken(u User, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := AccessClaims{
		Role:  u.Role,
		Email: u.Email,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   u.ID,
			Issuer:    issuer,
			Audience:  jwt.ClaimStrings{audience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			ID:        randomHex(8), // jti -- lets you blacklist one specific token
		},
	}

	// SigningMethodHS256: symmetric, one secret signs and verifies. Fine when
	// the same service issues and validates. Use RS256/ES256 (private key
	// signs, public key verifies) when OTHER services must verify tokens
	// without being able to mint them -- that is the microservices case.
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString(ts.secret)
}

// Parse validates a token string and returns its claims.
func (ts *TokenService) Parse(tokenStr string) (*AccessClaims, error) {
	claims := &AccessClaims{}

	_, err := jwt.ParseWithClaims(tokenStr, claims,
		// The keyfunc is where you enforce the algorithm. THIS IS THE
		// SECURITY-CRITICAL PART: without the check below, an attacker can
		// send alg:"none" or swap HS256/RS256 and forge tokens.
		func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return ts.secret, nil
		},
		// v5 validates exp/nbf automatically. These add the rest:
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), // belt and braces
		jwt.WithIssuer(issuer),
		jwt.WithAudience(audience),
		jwt.WithLeeway(30*time.Second), // tolerate small clock skew between hosts
	)
	if err != nil {
		// v5 exposes typed errors -- map them to useful API responses.
		switch {
		case errors.Is(err, jwt.ErrTokenExpired):
			return nil, ErrTokenExpired
		case errors.Is(err, jwt.ErrTokenSignatureInvalid):
			return nil, ErrTokenInvalid
		default:
			return nil, fmt.Errorf("%w: %v", ErrTokenInvalid, err)
		}
	}

	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.revoked[claims.ID] {
		return nil, ErrTokenRevoked
	}
	return claims, nil
}

// NewRefreshToken mints an opaque random token and stores it.
func (ts *TokenService) NewRefreshToken(userID string) string {
	t := randomHex(32)
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.refresh[t] = refreshRecord{UserID: userID, ExpiresAt: time.Now().Add(refreshTTL)}
	return t
}

// UseRefreshToken validates a refresh token and ROTATES it: the old one is
// deleted and a new one issued. Rotation means a stolen refresh token is
// usable at most once, and the theft becomes detectable (the real user's next
// refresh fails).
func (ts *TokenService) UseRefreshToken(t string) (userID, next string, err error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	rec, ok := ts.refresh[t]
	if !ok {
		return "", "", ErrTokenInvalid
	}
	delete(ts.refresh, t) // single use
	if time.Now().After(rec.ExpiresAt) {
		return "", "", ErrTokenExpired
	}

	next = randomHex(32)
	ts.refresh[next] = refreshRecord{UserID: rec.UserID, ExpiresAt: time.Now().Add(refreshTTL)}
	return rec.UserID, next, nil
}

func (ts *TokenService) RevokeRefreshToken(t string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	delete(ts.refresh, t)
}

// RevokeAccessToken blacklists one jti until it would have expired anyway.
// In production this map is Redis with a TTL == the token's remaining life.
func (ts *TokenService) RevokeAccessToken(jti string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.revoked[jti] = true
}

var (
	ErrTokenExpired = errors.New("token expired")
	ErrTokenInvalid = errors.New("token invalid")
	ErrTokenRevoked = errors.New("token revoked")
)

// ---------------------------------------------------------------------------
// 3. USERS + PASSWORD HASHING
//
// bcrypt, not sha256. bcrypt is deliberately slow and salts automatically,
// which is what makes a leaked password table useless to an attacker.
// ---------------------------------------------------------------------------

type User struct {
	ID       string
	Email    string
	Role     string // "user" | "admin"
	PassHash []byte
}

type UserStore struct{ byEmail map[string]User }

func NewUserStore() *UserStore {
	s := &UserStore{byEmail: map[string]User{}}
	s.add(User{ID: "u1", Email: "prince@example.com", Role: "user"}, "hunter2")
	s.add(User{ID: "u2", Email: "boss@example.com", Role: "admin"}, "s3cret")
	return s
}

func (s *UserStore) add(u User, password string) {
	// DefaultCost (10) ~ 60ms. Higher cost = slower login = harder brute force.
	h, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	u.PassHash = h
	s.byEmail[u.Email] = u
}

func (s *UserStore) Authenticate(email, password string) (User, error) {
	u, ok := s.byEmail[email]
	if !ok {
		// Still run a bcrypt comparison against a dummy hash so the response
		// time does not reveal whether the email exists (timing attack).
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$invalidinvalidinvalidinvalidinvalidinvalidinvalidinvalidin"), []byte(password))
		return User{}, errors.New("invalid credentials")
	}
	if err := bcrypt.CompareHashAndPassword(u.PassHash, []byte(password)); err != nil {
		return User{}, errors.New("invalid credentials")
	}
	return u, nil
}

// ---------------------------------------------------------------------------
// 4. MIDDLEWARE: authenticate, then authorize. Two separate steps.
//
//	Authentication = who are you?    (valid token -> claims)
//	Authorization  = are you allowed? (claims.Role vs required role)
// ---------------------------------------------------------------------------

// Context key with its own unexported type -- never use a bare string, or
// another package can collide with your key.
type ctxKey struct{ name string }

var claimsKey = ctxKey{"claims"}

func ClaimsFrom(r *http.Request) (*AccessClaims, bool) {
	c, ok := r.Context().Value(claimsKey).(*AccessClaims)
	return c, ok
}

func (ts *TokenService) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get("Authorization")
		// Exactly "Bearer <token>", case-insensitive scheme.
		scheme, token, found := strings.Cut(raw, " ")
		if !found || !strings.EqualFold(scheme, "bearer") || token == "" {
			writeErr(w, http.StatusUnauthorized, "missing bearer token")
			return
		}

		claims, err := ts.Parse(token)
		if err != nil {
			// 401 = "you are not authenticated". Tell the client whether to
			// refresh or to re-login, but never leak why the signature failed.
			code := "invalid_token"
			if errors.Is(err, ErrTokenExpired) {
				code = "token_expired" // the client's cue to hit /refresh
			}
			writeErr(w, http.StatusUnauthorized, code)
			return
		}

		// Stash the claims on the context for handlers downstream.
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), claimsKey, claims)))
	})
}

// RequireRole must run AFTER RequireAuth -- it trusts the claims in context.
func RequireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := ClaimsFrom(r)
			if !ok {
				// Programmer error: RequireRole was wired without RequireAuth.
				writeErr(w, http.StatusUnauthorized, "not authenticated")
				return
			}
			if claims.Role != role {
				// 403 = "we know who you are, you just cannot do this".
				writeErr(w, http.StatusForbidden, "forbidden: requires role "+role)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ---------------------------------------------------------------------------
// 5. HANDLERS
// ---------------------------------------------------------------------------

type API struct {
	users  *UserStore
	tokens *TokenService
}

func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /login", a.login)
	mux.HandleFunc("POST /refresh", a.refresh)
	mux.HandleFunc("POST /logout", a.logout)

	// Protected: wrap the handler in the middleware chain.
	mux.Handle("GET /me", a.tokens.RequireAuth(http.HandlerFunc(a.me)))
	mux.Handle("GET /admin", a.tokens.RequireAuth(RequireRole("admin")(http.HandlerFunc(a.admin))))

	return mux
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var body struct{ Email, Password string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}

	u, err := a.users.Authenticate(body.Email, body.Password)
	if err != nil {
		// Same message for "no such user" and "wrong password" -- do not help
		// an attacker enumerate valid emails.
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	access, err := a.tokens.NewAccessToken(u, accessTTL)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not sign token")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  access,
		"refresh_token": a.tokens.NewRefreshToken(u.ID),
		"expires_in":    int(accessTTL.Seconds()),
		"token_type":    "Bearer",
	})
}

func (a *API) refresh(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	userID, next, err := a.tokens.UseRefreshToken(body.RefreshToken)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid refresh token")
		return
	}

	// Re-load the user: role may have changed, account may be disabled.
	// This is exactly why access tokens are short-lived.
	var u User
	for _, cand := range a.users.byEmail {
		if cand.ID == userID {
			u = cand
		}
	}

	access, _ := a.tokens.NewAccessToken(u, accessTTL)
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  access,
		"refresh_token": next, // rotated
	})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	a.tokens.RevokeRefreshToken(body.RefreshToken)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	c, _ := ClaimsFrom(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id": c.Subject, "email": c.Email, "role": c.Role,
		"expires_at": c.ExpiresAt.Time.Format(time.RFC3339),
	})
}

func (a *API) admin(w http.ResponseWriter, r *http.Request) {
	c, _ := ClaimsFrom(r)
	writeJSON(w, http.StatusOK, map[string]any{"secret": "the launch codes", "for": c.Email})
}

// ---------------------------------------------------------------------------
// 6. LIVE DEMO -- spins the API up on a real local port and walks the flow.
// ---------------------------------------------------------------------------

func main() {
	api := &API{users: NewUserStore(), tokens: NewTokenService([]byte("dev-secret-change-me"))}
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	fmt.Println("=== 1. login with a wrong password ===")
	call(srv.URL, "POST", "/login", "", `{"email":"prince@example.com","password":"nope"}`)

	fmt.Println("\n=== 2. login correctly ===")
	body := call(srv.URL, "POST", "/login", "", `{"email":"prince@example.com","password":"hunter2"}`)
	var tok struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	_ = json.Unmarshal([]byte(body), &tok)
	fmt.Println("  decoded payload:", decodePayload(tok.Access))

	fmt.Println("\n=== 3. call /me without a token ===")
	call(srv.URL, "GET", "/me", "", "")

	fmt.Println("\n=== 4. call /me with the token ===")
	call(srv.URL, "GET", "/me", tok.Access, "")

	fmt.Println("\n=== 5. call /admin as a plain user -> 403, not 401 ===")
	call(srv.URL, "GET", "/admin", tok.Access, "")

	fmt.Println("\n=== 6. a tampered token fails the signature check ===")
	call(srv.URL, "GET", "/me", tamper(tok.Access), "")

	fmt.Println("\n=== 7. an expired token tells the client to refresh ===")
	expired, _ := api.tokens.NewAccessToken(User{ID: "u1", Email: "prince@example.com", Role: "user"}, -time.Minute)
	call(srv.URL, "GET", "/me", expired, "")

	fmt.Println("\n=== 8. refresh -> new access token, refresh token rotated ===")
	body = call(srv.URL, "POST", "/refresh", "", `{"refresh_token":"`+tok.Refresh+`"}`)
	var refreshed struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	_ = json.Unmarshal([]byte(body), &refreshed)

	fmt.Println("\n=== 9. reusing the OLD refresh token now fails (rotation) ===")
	call(srv.URL, "POST", "/refresh", "", `{"refresh_token":"`+tok.Refresh+`"}`)

	fmt.Println("\n=== 10. admin login reaches /admin ===")
	body = call(srv.URL, "POST", "/login", "", `{"email":"boss@example.com","password":"s3cret"}`)
	var adminTok struct {
		Access string `json:"access_token"`
	}
	_ = json.Unmarshal([]byte(body), &adminTok)
	call(srv.URL, "GET", "/admin", adminTok.Access, "")

	fmt.Println("\n=== 11. logout revokes the refresh token ===")
	call(srv.URL, "POST", "/logout", "", `{"refresh_token":"`+refreshed.Refresh+`"}`)
	call(srv.URL, "POST", "/refresh", "", `{"refresh_token":"`+refreshed.Refresh+`"}`)

	fmt.Print(`
--- THE PARTS PEOPLE GET WRONG -------------------------------------------

1. Not pinning the algorithm in the keyfunc. Without the HMAC type check,
   a forged alg:"none" or an RS256->HS256 confusion attack mints tokens.
2. Treating a JWT as revocable. It is not: it is valid until exp, full stop.
   Short access TTL + a revocable refresh token is the standard answer.
3. Putting secrets or a whole user record in the payload. It is readable.
4. Storing the token in localStorage. Any XSS steals it. Prefer an
   httpOnly + Secure + SameSite cookie, then add CSRF protection.
5. 401 vs 403. 401 = no/bad token (client should re-auth). 403 = good token,
   insufficient role. Getting this wrong breaks client retry logic.
6. Hashing passwords with sha256. Use bcrypt/argon2 -- slow and salted.
7. Different error messages for "unknown email" vs "wrong password".
   That is a free user-enumeration oracle.

--- HS256 vs RS256, in one line each --------------------------------------

  HS256: one shared secret signs AND verifies -> fine inside one service.
  RS256: private key signs, public key verifies -> the auth service mints,
         every other microservice verifies with the public key (JWKS) and
         cannot forge. This is what you want across service boundaries.
`)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func call(base, method, path, bearer, body string) string {
	req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Println("  request failed:", err)
		return ""
	}
	defer resp.Body.Close()

	buf := new(strings.Builder)
	_, _ = io.Copy(buf, resp.Body)
	out := buf.String()
	fmt.Printf("  %s %-9s -> %d %s\n", method, path, resp.StatusCode, truncate(out, 120))
	return out
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand -- never math/rand for anything security related
	return hex.EncodeToString(b)
}

// tamper flips a character in the payload so the signature no longer matches.
func tamper(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return token
	}
	p := []byte(parts[1])
	p[len(p)-2] ^= 0x01
	return parts[0] + "." + string(p) + "." + parts[2]
}

// decodePayload proves the payload is only base64 -- readable by anyone.
func decodePayload(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	raw, err := jwt.NewParser().DecodeSegment(parts[1])
	if err != nil {
		return ""
	}
	return string(raw)
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

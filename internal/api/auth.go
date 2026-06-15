package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// AuthState holds bcrypt hash and session HMAC secret.
type AuthState = authState

// authState holds bcrypt hash and session HMAC secret.
type authState struct {
	user         string
	passwordHash []byte
	secret       []byte
}

// NewAuthState is the exported constructor.
func NewAuthState(user, password, secret string) (*authState, error) {
	return newAuthState(user, password, secret)
}

func newAuthState(user, password, secret string) (*authState, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	sec := []byte(secret)
	if len(sec) == 0 {
		sec = make([]byte, 32)
		if _, err := rand.Read(sec); err != nil {
			return nil, err
		}
	}
	return &authState{user: user, passwordHash: hash, secret: sec}, nil
}

func (a *authState) verify(user, password string) bool {
	if user != a.user {
		return false
	}
	return bcrypt.CompareHashAndPassword(a.passwordHash, []byte(password)) == nil
}

// createSession returns a signed session token.
func (a *authState) createSession() string {
	payload := "auth"
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return payload + "." + sig
}

// validateSession checks the HMAC signature on a session token.
func (a *authState) validateSession(token string) bool {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return false
	}
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(parts[0]))
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(parts[1]), []byte(expected))
}

const sessionCookie = "sb_session"

// sessionMiddleware rejects requests without a valid session cookie.
func sessionMiddleware(auth *authState) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(sessionCookie)
			if err != nil || !auth.validateSession(cookie.Value) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(withAuth(r.Context())))
		})
	}
}

type ctxAuthKey struct{}

func withAuth(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxAuthKey{}, true)
}

// rateLimit tracks failed login attempts per IP with TTL eviction.
type rateLimit struct {
	mu       sync.Mutex
	attempts map[string]*rlEntry
}

type rlEntry struct {
	count       int
	lockedUntil time.Time
	lastSeen    time.Time
}

const (
	rlMaxAttempts = 5
	rlLockDuration = 15 * time.Minute
	rlEvictAfter  = time.Hour
)

func newRateLimit() *rateLimit {
	rl := &rateLimit{attempts: make(map[string]*rlEntry)}
	go rl.evictLoop()
	return rl
}

func (rl *rateLimit) allowed(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	e := rl.attempts[ip]
	if e == nil {
		return true
	}
	return time.Now().After(e.lockedUntil)
}

func (rl *rateLimit) record(ip string, success bool) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if success {
		delete(rl.attempts, ip)
		return
	}
	e := rl.attempts[ip]
	if e == nil {
		e = &rlEntry{}
		rl.attempts[ip] = e
	}
	e.lastSeen = time.Now()
	e.count++
	if e.count >= rlMaxAttempts {
		e.lockedUntil = time.Now().Add(rlLockDuration)
	}
}

func (rl *rateLimit) evictLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	for range ticker.C {
		cutoff := time.Now().Add(-rlEvictAfter)
		rl.mu.Lock()
		for ip, e := range rl.attempts {
			if e.lastSeen.Before(cutoff) {
				delete(rl.attempts, ip)
			}
		}
		rl.mu.Unlock()
	}
}

// clientIP extracts the real client IP, respecting trusted proxy headers.
func clientIP(r *http.Request, trustedCIDRs []*net.IPNet) string {
	remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	remote := net.ParseIP(remoteIP)

	for _, cidr := range trustedCIDRs {
		if cidr.Contains(remote) {
			if xri := r.Header.Get("X-Real-IP"); xri != "" {
				return strings.TrimSpace(xri)
			}
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				return strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
			}
			break
		}
	}
	return remoteIP
}

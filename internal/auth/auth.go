// Package auth implements admin login with PBKDF2-HMAC-SHA256 password
// hashing and in-memory session tokens.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"sync"
	"time"
)

const pbkdf2Iterations = 100_000

type Auth struct {
	mu           sync.Mutex
	username     string
	passwordHash []byte
	salt         []byte
	ttl          time.Duration
	sessions     map[string]time.Time
}

func New(username, password string, ttl time.Duration) *Auth {
	a := &Auth{ttl: ttl, sessions: make(map[string]time.Time)}
	a.SetCredentials(username, password, ttl)
	return a
}

// SetCredentials updates the active admin credentials. Existing sessions stay
// valid until they expire; new logins use the updated username/password.
func (a *Auth) SetCredentials(username, password string, ttl time.Duration) {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	salt := sha256.Sum256([]byte("wrapper-lite:" + username))
	a.mu.Lock()
	defer a.mu.Unlock()
	a.username = username
	a.passwordHash = pbkdf2Key([]byte(password), salt[:], pbkdf2Iterations, 32)
	a.salt = salt[:]
	a.ttl = ttl
}

func (a *Auth) Login(username, password string) (string, bool) {
	a.mu.Lock()
	wantUser := a.username
	salt := append([]byte(nil), a.salt...)
	wantHash := append([]byte(nil), a.passwordHash...)
	ttl := a.ttl
	a.mu.Unlock()

	if username != wantUser {
		return "", false
	}
	hash := pbkdf2Key([]byte(password), salt, pbkdf2Iterations, 32)
	if subtle.ConstantTimeCompare(hash, wantHash) != 1 {
		return "", false
	}
	token := randomToken()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.purgeExpiredLocked()
	a.sessions[token] = time.Now().Add(ttl)
	return token, true
}

func (a *Auth) Valid(token string) bool {
	if token == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.sessions[token]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(a.sessions, token)
		return false
	}
	return true
}

func (a *Auth) Logout(token string) {
	if token == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, token)
}

func (a *Auth) purgeExpiredLocked() {
	now := time.Now()
	for t, exp := range a.sessions {
		if now.After(exp) {
			delete(a.sessions, t)
		}
	}
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// pbkdf2Key implements PBKDF2-HMAC-SHA256 (RFC 2898).
func pbkdf2Key(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hLen := prf.Size()
	numBlocks := (keyLen + hLen - 1) / hLen
	var block [4]byte
	out := make([]byte, 0, numBlocks*hLen)
	U := make([]byte, hLen)
	T := make([]byte, hLen)
	for i := 1; i <= numBlocks; i++ {
		prf.Reset()
		prf.Write(salt)
		block[0] = byte(i >> 24)
		block[1] = byte(i >> 16)
		block[2] = byte(i >> 8)
		block[3] = byte(i)
		prf.Write(block[:])
		U = prf.Sum(U[:0])
		copy(T, U)
		for n := 2; n <= iter; n++ {
			prf.Reset()
			prf.Write(U)
			U = U[:0]
			U = prf.Sum(U[:0])
			for x := range U {
				T[x] ^= U[x]
			}
		}
		out = append(out, T...)
	}
	return out[:keyLen]
}

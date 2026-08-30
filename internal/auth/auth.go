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
	username     string
	passwordHash []byte
	salt         []byte
	ttl          time.Duration

	mu       sync.Mutex
	sessions map[string]time.Time
}

func New(username, password string, ttl time.Duration) *Auth {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	salt := sha256.Sum256([]byte("wrapper-lite:" + username))
	return &Auth{
		username:     username,
		passwordHash: pbkdf2Key([]byte(password), salt[:], pbkdf2Iterations, 32),
		salt:         salt[:],
		ttl:          ttl,
		sessions:     make(map[string]time.Time),
	}
}

func (a *Auth) Login(username, password string) (string, bool) {
	if username != a.username {
		return "", false
	}
	hash := pbkdf2Key([]byte(password), a.salt, pbkdf2Iterations, 32)
	if subtle.ConstantTimeCompare(hash, a.passwordHash) != 1 {
		return "", false
	}
	token := randomToken()
	a.mu.Lock()
	a.purgeExpiredLocked()
	a.sessions[token] = time.Now().Add(a.ttl)
	a.mu.Unlock()
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

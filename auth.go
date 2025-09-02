package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// User represents an application user.
type User struct {
	ID               string   `json:"id"`
	Email            string   `json:"email"`
	PasswordHash     string   `json:"password_hash"`
	Roles            []string `json:"roles,omitempty"`
	SubscriptionOK   bool     `json:"subscription_ok"`
	StripeCustomerID string   `json:"stripe_customer_id,omitempty"`
	CreatedAt        int64    `json:"created_at"`
}

// UserStore provides thread-safe CRUD access to users backed by a JSON file.
type UserStore struct {
	filePath string
	mu       sync.RWMutex
	byEmail  map[string]*User
}

func NewUserStore(filePath string) (*UserStore, error) {
	us := &UserStore{filePath: filePath, byEmail: map[string]*User{}}
	if err := us.load(); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Ensure directory exists
			_ = os.MkdirAll(filepath.Dir(filePath), 0o755)
			if err := us.save(); err != nil { return nil, err }
		} else {
			return nil, err
		}
	}
	return us, nil
}

func (s *UserStore) load() error {
	b, err := os.ReadFile(s.filePath)
	if err != nil { return err }
	var users []*User
	if err := json.Unmarshal(b, &users); err != nil { return err }
	for _, u := range users {
		s.byEmail[strings.ToLower(u.Email)] = u
	}
	return nil
}

func (s *UserStore) save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var users []*User
	for _, u := range s.byEmail {
		users = append(users, u)
	}
	b, err := json.MarshalIndent(users, "", "  ")
	if err != nil { return err }
	return os.WriteFile(s.filePath, b, 0o600)
}

func (s *UserStore) CreateUser(email, plaintextPassword string) (*User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || plaintextPassword == "" { return nil, fmt.Errorf("email and password required") }
	s.mu.Lock(); defer s.mu.Unlock()
	if _, exists := s.byEmail[email]; exists { return nil, fmt.Errorf("user already exists") }
	hash, err := bcrypt.GenerateFromPassword([]byte(plaintextPassword), bcrypt.DefaultCost)
	if err != nil { return nil, err }
	id := generateDeterministicID(email)
	u := &User{ID: id, Email: email, PasswordHash: string(hash), Roles: []string{"user"}, SubscriptionOK: false, CreatedAt: time.Now().Unix()}
	s.byEmail[email] = u
	if err := s.save(); err != nil { return nil, err }
	return u, nil
}

func (s *UserStore) Authenticate(email, plaintextPassword string) (*User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	s.mu.RLock(); u := s.byEmail[email]; s.mu.RUnlock()
	if u == nil { return nil, fmt.Errorf("invalid credentials") }
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(plaintextPassword)); err != nil { return nil, fmt.Errorf("invalid credentials") }
	return u, nil
}

func (s *UserStore) GetByEmail(email string) *User {
	email = strings.ToLower(strings.TrimSpace(email))
	s.mu.RLock(); defer s.mu.RUnlock()
	return s.byEmail[email]
}

func (s *UserStore) SetSubscription(email string, ok bool) error {
	email = strings.ToLower(strings.TrimSpace(email))
	s.mu.Lock(); defer s.mu.Unlock()
	u := s.byEmail[email]
	if u == nil { return fmt.Errorf("user not found") }
	u.SubscriptionOK = ok
	return s.save()
}

func (s *UserStore) SetStripeCustomerID(email, customerID string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	s.mu.Lock(); defer s.mu.Unlock()
	u := s.byEmail[email]
	if u == nil { return fmt.Errorf("user not found") }
	u.StripeCustomerID = customerID
	return s.save()
}

func (s *UserStore) FindByStripeCustomerID(customerID string) *User {
	s.mu.RLock(); defer s.mu.RUnlock()
	for _, u := range s.byEmail {
		if u.StripeCustomerID == customerID { return u }
	}
	return nil
}

func generateDeterministicID(seed string) string {
	h := sha256.Sum256([]byte(seed))
	return base64.RawURLEncoding.EncodeToString(h[:16])
}

// JWT utilities (HMAC-SHA256)

type JWTClaims struct {
	Subject string `json:"sub"`
	Email   string `json:"email"`
	Exp     int64  `json:"exp"`
}

func getJWTSecret() ([]byte, error) {
	secret := strings.TrimSpace(os.Getenv("JWT_SECRET"))
	if secret == "" { return nil, fmt.Errorf("JWT_SECRET not set") }
	return []byte(secret), nil
}

func createJWT(claims JWTClaims) (string, error) {
	head := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payloadBytes, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	unsigned := head + "." + payload
	secret, err := getJWTSecret()
	if err != nil { return "", err }
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(unsigned))
	sig := base64.RawURLEncoding.EncodeToString(h.Sum(nil))
	return unsigned + "." + sig, nil
}

func parseAndValidateJWT(token string) (*JWTClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 { return nil, fmt.Errorf("invalid token format") }
	unsigned := parts[0] + "." + parts[1]
	secret, err := getJWTSecret()
	if err != nil { return nil, err }
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(unsigned))
	expected := base64.RawURLEncoding.EncodeToString(h.Sum(nil))
	if !constantTimeEqual(parts[2], expected) { return nil, fmt.Errorf("invalid signature") }
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil { return nil, err }
	var claims JWTClaims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil { return nil, err }
	if time.Now().Unix() > claims.Exp { return nil, fmt.Errorf("token expired") }
	return &claims, nil
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) { return false }
	var v byte
	for i := 0; i < len(a); i++ { v |= a[i] ^ b[i] }
	return v == 0
}
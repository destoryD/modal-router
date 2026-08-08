package modalauth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Account struct {
	ID            string     `json:"id"`
	Email         string     `json:"email"`
	Name          string     `json:"name,omitempty"`
	CookieCipher  string     `json:"cookieCipher,omitempty"`
	CookiePreview string     `json:"cookiePreview,omitempty"`
	WorkspaceURL  string     `json:"workspaceUrl,omitempty"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

type Settings struct {
	BaseURL       string `json:"baseUrl"`
	ProxyURL      string `json:"proxyUrl,omitempty"`
	JobConcurrency int   `json:"jobConcurrency"`
	Headless      bool   `json:"headless"`
}

type State struct {
	Accounts []Account `json:"accounts"`
	Settings Settings  `json:"settings"`
}

type Store struct {
	mu       sync.Mutex
	path     string
	secret   []byte
	state    State
	dirty    bool
}

func NewStore(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}
	secret, err := loadSecret(filepath.Join(dataDir, ".modal_secret"))
	if err != nil {
		return nil, err
	}
	s := &Store{
		path:   filepath.Join(dataDir, "modal_accounts.json"),
		secret: secret,
	}
	s.state.Settings = Settings{
		BaseURL:        "https://modal.com",
		JobConcurrency: 1,
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func loadSecret(path string) ([]byte, error) {
	if raw, err := os.ReadFile(path); err == nil {
		sum := sha256.Sum256(raw)
		return sum[:], nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	enc := hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(enc), 0600); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(enc))
	return sum[:], nil
}

func (s *Store) load() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s.saveLocked()
		}
		return err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, &s.state); err != nil {
		return err
	}
	s.migrateLocked()
	return nil
}

func (s *Store) migrateLocked() {
	if strings.TrimSpace(s.state.Settings.BaseURL) == "" {
		s.state.Settings.BaseURL = "https://modal.com"
	}
	if s.state.Settings.JobConcurrency <= 0 {
		s.state.Settings.JobConcurrency = 1
	}
	if s.state.Settings.JobConcurrency > 10 {
		s.state.Settings.JobConcurrency = 10
	}
}

func (s *Store) saveLocked() error {
	snapshot, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	var persist State
	if err := json.Unmarshal(snapshot, &persist); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(persist, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return renameWithRetry(tmp, s.path, 6, 100*time.Millisecond)
}

func renameWithRetry(src, dst string, attempts int, wait time.Duration) error {
	var err error
	for i := 0; i < attempts; i++ {
		err = os.Rename(src, dst)
		if err == nil {
			return nil
		}
		if os.IsNotExist(err) {
			if _, statErr := os.Stat(dst); statErr == nil {
				return nil
			}
		}
		time.Sleep(wait)
	}
	return err
}

func (s *Store) GetSettings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Settings
}

func (s *Store) UpdateSettings(in Settings) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	if in.BaseURL == "" {
		in.BaseURL = "https://modal.com"
	}
	in.ProxyURL = strings.TrimSpace(in.ProxyURL)
	if in.JobConcurrency <= 0 {
		in.JobConcurrency = 1
	}
	if in.JobConcurrency > 10 {
		in.JobConcurrency = 10
	}
	s.state.Settings = in
	if err := s.saveLocked(); err != nil {
		return Settings{}, err
	}
	return in, nil
}

func (s *Store) ListAccounts() []Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Account, len(s.state.Accounts))
	copy(out, s.state.Accounts)
	for i := range out {
		out[i].CookieCipher = ""
	}
	return out
}

func (s *Store) GetAccountCookie(id string) (Account, string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, acc := range s.state.Accounts {
		if acc.ID == id {
			if acc.CookieCipher == "" {
				return acc, "", "", errors.New("cookie missing")
			}
			cookie, err := s.decrypt(acc.CookieCipher)
			return acc, cookie, s.state.Settings.ProxyURL, err
		}
	}
	return Account{}, "", "", os.ErrNotExist
}

func (s *Store) findAccountLocked(id string) int {
	for i := range s.state.Accounts {
		if s.state.Accounts[i].ID == id {
			return i
		}
	}
	return -1
}

func (s *Store) AddAccount(email, name, cookie, workspaceURL string) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cipherText, err := s.encrypt(cookie)
	if err != nil {
		return Account{}, err
	}
	now := time.Now()
	acc := Account{
		ID:            newID("modal"),
		Email:         email,
		Name:          name,
		CookieCipher:  cipherText,
		CookiePreview: previewCookie(cookie),
		WorkspaceURL:  workspaceURL,
		Status:        "active",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	s.state.Accounts = append(s.state.Accounts, acc)
	if err := s.saveLocked(); err != nil {
		s.state.Accounts = s.state.Accounts[:len(s.state.Accounts)-1]
		return Account{}, err
	}
	acc.CookieCipher = ""
	return acc, nil
}

func (s *Store) DeleteAccount(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, acc := range s.state.Accounts {
		if acc.ID == id {
			s.state.Accounts = append(s.state.Accounts[:i], s.state.Accounts[i+1:]...)
			return s.saveLocked()
		}
	}
	return os.ErrNotExist
}

func (s *Store) CookieExists(cookie string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := cookieDedupKey(cookie)
	if key == "" {
		return false
	}
	for _, acc := range s.state.Accounts {
		if acc.CookieCipher == "" {
			continue
		}
		plain, err := s.decrypt(acc.CookieCipher)
		if err == nil && cookieDedupKey(plain) == key {
			return true
		}
	}
	return false
}

func (s *Store) encrypt(plain string) (string, error) {
	block, err := aes.NewCipher(s.secret)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (s *Store) decrypt(cipherText string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cipherText)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(s.secret)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("ciphertext corrupted")
	}
	nonce, body := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func cleanCookieHeader(raw string) (string, error) {
	cookie := strings.TrimSpace(raw)
	if cookie == "" {
		return "", errors.New("cookie cannot be empty")
	}
	if len(cookie) >= len("Cookie:") && strings.EqualFold(cookie[:len("Cookie:")], "Cookie:") {
		cookie = strings.TrimSpace(cookie[len("Cookie:"):])
	}
	parts := strings.Split(cookie, ";")
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "=") {
			return "", errors.New("invalid Cookie format")
		}
		cleaned = append(cleaned, part)
	}
	if len(cleaned) == 0 {
		return "", errors.New("cookie cannot be empty")
	}
	return strings.Join(cleaned, "; "), nil
}

func cookieDedupKey(raw string) string {
	cookie, err := cleanCookieHeader(raw)
	if err != nil {
		return ""
	}
	parts := strings.Split(cookie, ";")
	pairs := make([]string, 0, len(parts))
	for _, part := range parts {
		pair := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(pair) != 2 {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(pair[0]))
		if name == "" {
			continue
		}
		pairs = append(pairs, name+"="+strings.TrimSpace(pair[1]))
	}
	if len(pairs) == 0 {
		return ""
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ";")
}

func previewCookie(cookie string) string {
	parts := strings.Split(cookie, ";")
	first := strings.TrimSpace(parts[0])
	if len(first) <= 18 {
		return first
	}
	return first[:14] + "..." + first[len(first)-4:]
}

func newID(prefix string) string {
	b := make([]byte, 12)
	rand.Read(b)
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return fmt.Sprintf("%s_%s", prefix, string(b))
}

var _ = log.Printf

package api

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"gpupanel/internal/models"
	"gpupanel/internal/store"
)

type session struct {
	Account int64
	Expire  time.Time
}

type Auth struct {
	st       *store.Store
	mu       sync.Mutex
	sessions map[string]session
	fails    map[string][]time.Time
}

func NewAuth(st *store.Store) *Auth {
	return &Auth{st: st, sessions: map[string]session{}, fails: map[string][]time.Time{}}
}

func Token(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func HashPassword(p string) string {
	h, _ := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	return string(h)
}

func checkPassword(hash, p string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(p)) == nil
}

// EnsureAdmin creates the first admin account when none exists.
func (a *Auth) EnsureAdmin(user, pass string) (created bool) {
	for _, ac := range store.List[models.Account](a.st, models.KAccount) {
		if ac.Role == "admin" {
			return false
		}
	}
	id := a.st.NextID(models.KAccount)
	a.st.Put(models.KAccount, id, models.Account{ID: id, Username: user, Password: HashPassword(pass), Role: "admin", Token: Token(16), Lang: "ar", Theme: "dark", MustChange: true})
	return true
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		h = r.RemoteAddr
	}
	return h
}

func (a *Auth) Login(r *http.Request, user, pass string) (string, *models.Account, string) {
	ip := clientIP(r)
	a.mu.Lock()
	var recent []time.Time
	for _, t := range a.fails[ip] {
		if time.Since(t) < 10*time.Minute {
			recent = append(recent, t)
		}
	}
	a.fails[ip] = recent
	a.mu.Unlock()
	if len(recent) >= 8 {
		return "", nil, "too many attempts, try again later"
	}
	for _, ac := range store.List[models.Account](a.st, models.KAccount) {
		if strings.EqualFold(ac.Username, user) && !ac.Disabled && checkPassword(ac.Password, pass) {
			tok := Token(24)
			a.mu.Lock()
			a.sessions[tok] = session{ac.ID, time.Now().Add(7 * 24 * time.Hour)}
			delete(a.fails, ip)
			a.mu.Unlock()
			ac.LastLogin, ac.LastIP = time.Now().Unix(), ip
			a.st.Put(models.KAccount, ac.ID, ac)
			return tok, &ac, ""
		}
	}
	a.mu.Lock()
	a.fails[ip] = append(a.fails[ip], time.Now())
	a.mu.Unlock()
	return "", nil, "invalid username or password"
}

func (a *Auth) Logout(tok string) {
	a.mu.Lock()
	delete(a.sessions, tok)
	a.mu.Unlock()
}

// Account resolves the caller from the session cookie or an API token.
func (a *Auth) Account(r *http.Request) *models.Account {
	tok := ""
	if c, err := r.Cookie("mgpu_session"); err == nil {
		tok = c.Value
	}
	if tok != "" {
		a.mu.Lock()
		s, ok := a.sessions[tok]
		a.mu.Unlock()
		if ok && time.Now().Before(s.Expire) {
			var ac models.Account
			if a.st.Get(models.KAccount, s.Account, &ac) == nil && !ac.Disabled {
				return &ac
			}
		}
	}
	api := r.Header.Get("X-API-Key")
	if api == "" {
		api = r.URL.Query().Get("api_key")
	}
	if api != "" {
		for _, ac := range store.List[models.Account](a.st, models.KAccount) {
			if ac.Token != "" && ac.Token == api && !ac.Disabled {
				return &ac
			}
		}
	}
	return nil
}

// ChangePassword updates credentials after verifying the current password.
func (a *Auth) ChangePassword(ac *models.Account, oldPass, newUser, newPass string) string {
	if !checkPassword(ac.Password, oldPass) {
		return "current password is wrong"
	}
	if len(newPass) < 5 {
		return "new password must be at least 5 characters"
	}
	if newUser != "" && !strings.EqualFold(newUser, ac.Username) {
		for _, o := range store.List[models.Account](a.st, models.KAccount) {
			if o.ID != ac.ID && strings.EqualFold(o.Username, newUser) {
				return "username already exists"
			}
		}
		ac.Username = newUser
	}
	ac.Password = HashPassword(newPass)
	ac.MustChange = false
	a.st.Put(models.KAccount, ac.ID, *ac)
	a.mu.Lock()
	for t, s := range a.sessions {
		if s.Account == ac.ID {
			delete(a.sessions, t)
		}
	}
	a.mu.Unlock()
	return ""
}

func (a *Auth) NewSession(id int64) string {
	tok := Token(24)
	a.mu.Lock()
	a.sessions[tok] = session{id, time.Now().Add(7 * 24 * time.Hour)}
	a.mu.Unlock()
	return tok
}

// ResetAdmin resets (or creates) the first admin account to the given login and forces a password change.
func (a *Auth) ResetAdmin(user, pass string) {
	for _, ac := range store.List[models.Account](a.st, models.KAccount) {
		if ac.Role == "admin" {
			ac.Username, ac.Password, ac.MustChange, ac.Disabled = user, HashPassword(pass), true, false
			a.st.Put(models.KAccount, ac.ID, ac)
			a.mu.Lock()
			for k, sid := range a.sessions {
				if sid.Account == ac.ID {
					delete(a.sessions, k)
				}
			}
			a.mu.Unlock()
			return
		}
	}
	a.EnsureAdmin(user, pass)
}

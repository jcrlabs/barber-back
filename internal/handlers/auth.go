package handlers

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jcrlabs/barber-back/internal/domain"
	"github.com/jcrlabs/barber-back/internal/middleware"
	"github.com/jcrlabs/barber-back/internal/repository"
	"github.com/jcrlabs/barber-back/internal/services"
	"golang.org/x/oauth2"
)

const (
	refreshCookie = "navaja_rt"
	refreshPath   = "/api/v1/auth"
	oauthCookie   = "navaja_oauth"
	oauthPath     = "/api/v1/auth/oauth"
)

type AuthHandler struct {
	auth      *services.AuthService
	users     *repository.UserRepository
	oauth     *services.OAuthRegistry
	publicURL string
	secure    bool
}

func NewAuthHandler(auth *services.AuthService, users *repository.UserRepository, oauth *services.OAuthRegistry, publicURL string, secure bool) *AuthHandler {
	return &AuthHandler{auth: auth, users: users, oauth: oauth, publicURL: publicURL, secure: secure}
}

type sessionResponse struct {
	AccessToken string       `json:"access_token"`
	ExpiresAt   time.Time    `json:"expires_at"`
	User        *domain.User `json:"user"`
}

// setCookie centraliza los atributos: siempre HttpOnly; Secure cuando la app se sirve por HTTPS.
func (h *AuthHandler) setCookie(c *gin.Context, name, value, path string, maxAge int, sameSite http.SameSite) {
	http.SetCookie(c.Writer, &http.Cookie{ //nolint:gosec // Secure depende de PUBLIC_URL (https en prod, http en local)
		Name: name, Value: value, Path: path, MaxAge: maxAge,
		HttpOnly: true, Secure: h.secure, SameSite: sameSite,
	})
}

func (h *AuthHandler) writeSession(c *gin.Context, status int, s *services.Session) {
	h.setCookie(c, refreshCookie, s.RefreshToken, refreshPath, int(time.Until(s.RefreshExpiresAt).Seconds()), http.SameSiteStrictMode)
	c.Header("Cache-Control", "no-store")
	c.JSON(status, sessionResponse{AccessToken: s.AccessToken, ExpiresAt: s.AccessExpiresAt, User: s.User})
}

func (h *AuthHandler) clearSession(c *gin.Context) {
	h.setCookie(c, refreshCookie, "", refreshPath, -1, http.SameSiteStrictMode)
}

type registerRequest struct {
	Name     string `json:"name" binding:"required,min=2,max=80"`
	Email    string `json:"email" binding:"required,email,max=254"`
	Password string `json:"password" binding:"required,min=8,max=72"`
	Phone    string `json:"phone" binding:"required,max=30,phone"`
}

func (h *AuthHandler) Register(c *gin.Context) {
	var req registerRequest
	if !bind(c, &req) {
		return
	}
	s, err := h.auth.Register(c.Request.Context(), req.Name, req.Email, req.Password, req.Phone)
	if err != nil {
		fail(c, err)
		return
	}
	h.writeSession(c, http.StatusCreated, s)
}

// loginRequest acepta un email o un nombre de usuario (p.ej. la cuenta "admin" de la barbería).
type loginRequest struct {
	Email    string `json:"email" binding:"required,max=254"`
	Password string `json:"password" binding:"required,max=72"`
}

func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if !bind(c, &req) {
		return
	}
	s, err := h.auth.Login(c.Request.Context(), req.Email, req.Password)
	if err != nil {
		fail(c, err)
		return
	}
	h.writeSession(c, http.StatusOK, s)
}

func (h *AuthHandler) Refresh(c *gin.Context) {
	raw, _ := c.Cookie(refreshCookie)
	s, err := h.auth.Refresh(c.Request.Context(), raw)
	if err != nil {
		h.clearSession(c)
		fail(c, err)
		return
	}
	h.writeSession(c, http.StatusOK, s)
}

func (h *AuthHandler) Logout(c *gin.Context) {
	raw, _ := c.Cookie(refreshCookie)
	if err := h.auth.Logout(c.Request.Context(), raw); err != nil {
		slog.Warn("logout", slog.String("error", err.Error()))
	}
	h.clearSession(c)
	c.Status(http.StatusNoContent)
}

func (h *AuthHandler) LogoutAll(c *gin.Context) {
	if err := h.auth.LogoutAll(c.Request.Context(), middleware.UserID(c)); err != nil {
		fail(c, err)
		return
	}
	h.clearSession(c)
	c.Status(http.StatusNoContent)
}

func (h *AuthHandler) Me(c *gin.Context) {
	u, err := h.users.FindByID(c.Request.Context(), middleware.UserID(c))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, u)
}

type updateMeRequest struct {
	Name  string `json:"name" binding:"required,min=2,max=80"`
	Phone string `json:"phone" binding:"omitempty,max=30,phone"`
}

func (h *AuthHandler) UpdateMe(c *gin.Context) {
	var req updateMeRequest
	if !bind(c, &req) {
		return
	}
	u, err := h.auth.UpdateProfile(c.Request.Context(), middleware.UserID(c), req.Name, req.Phone)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, u)
}

type passwordRequest struct {
	CurrentPassword string `json:"current_password" binding:"max=72"`
	NewPassword     string `json:"new_password" binding:"required,min=8,max=72"`
}

func (h *AuthHandler) ChangePassword(c *gin.Context) {
	var req passwordRequest
	if !bind(c, &req) {
		return
	}
	if err := h.auth.ChangePassword(c.Request.Context(), middleware.UserID(c), req.CurrentPassword, req.NewPassword); err != nil {
		fail(c, err)
		return
	}
	h.clearSession(c)
	c.Status(http.StatusNoContent)
}

func (h *AuthHandler) Providers(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"providers": h.oauth.List()})
}

// OAuthStart redirige al proveedor guardando state + PKCE verifier en una cookie efímera.
func (h *AuthHandler) OAuthStart(c *gin.Context) {
	p, ok := h.oauth.Get(c.Param("provider"))
	if !ok {
		fail(c, domain.ErrNotFound)
		return
	}
	state, err := randomString(24)
	if err != nil {
		fail(c, err)
		return
	}
	verifier := oauth2.GenerateVerifier()
	// Lax: la vuelta desde el proveedor es una navegación top-level cross-site.
	h.setCookie(c, oauthCookie, state+"."+verifier, oauthPath, 600, http.SameSiteLaxMode)
	c.Redirect(http.StatusFound, p.AuthCodeURL(state, verifier))
}

func (h *AuthHandler) OAuthCallback(c *gin.Context) {
	fe := func(reason string) {
		c.Redirect(http.StatusFound, h.publicURL+"/login?error="+url.QueryEscape(reason))
	}
	p, ok := h.oauth.Get(c.Param("provider"))
	if !ok {
		fe("provider")
		return
	}
	cookie, _ := c.Cookie(oauthCookie)
	h.setCookie(c, oauthCookie, "", oauthPath, -1, http.SameSiteLaxMode)

	state, verifier, found := strings.Cut(cookie, ".")
	got := c.Query("state")
	if !found || got == "" || subtle.ConstantTimeCompare([]byte(state), []byte(got)) != 1 {
		fe("state")
		return
	}
	if c.Query("error") != "" {
		fe("denied")
		return
	}
	code := c.Query("code")
	if code == "" {
		fe("code")
		return
	}

	profile, err := p.Exchange(c.Request.Context(), code, verifier)
	if err != nil {
		slog.Warn("oauth exchange failed", slog.String("provider", p.Name), slog.String("error", err.Error()))
		fe("exchange")
		return
	}
	s, err := h.auth.LoginWithProfile(c.Request.Context(), profile)
	if err != nil {
		slog.Warn("oauth login failed", slog.String("provider", p.Name), slog.String("error", err.Error()))
		if errors.Is(err, domain.ErrInvalidCredentials) {
			fe("email")
			return
		}
		fe("server")
		return
	}
	// Solo dejamos el refresh token en cookie httpOnly: el front lo canjea con /auth/refresh,
	// así nunca viajan tokens en la URL.
	h.setCookie(c, refreshCookie, s.RefreshToken, refreshPath, int(time.Until(s.RefreshExpiresAt).Seconds()), http.SameSiteLaxMode)
	c.Redirect(http.StatusFound, h.publicURL+"/auth/callback")
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

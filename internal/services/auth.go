package services

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jcrlabs/barber-back/internal/domain"
	"github.com/jcrlabs/barber-back/internal/repository"
	"golang.org/x/crypto/bcrypt"
)

var bcryptCost = 12 // variable para poder abaratarla en tests

type UserStore interface {
	Create(ctx context.Context, u *domain.User) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.User, error)
	FindByEmail(ctx context.Context, email string) (*domain.User, error)
	FindByIdentity(ctx context.Context, provider, subject string) (*domain.User, error)
	LinkIdentity(ctx context.Context, userID uuid.UUID, provider, subject, email string) error
	UpdateProfile(ctx context.Context, id uuid.UUID, name, phone string) error
	UpdatePassword(ctx context.Context, id uuid.UUID, hash string) error
	FillMissing(ctx context.Context, id uuid.UUID, avatarURL string, verified bool) error
	SetRole(ctx context.Context, id uuid.UUID, role domain.Role) error
}

type TokenStore interface {
	Create(ctx context.Context, userID, familyID uuid.UUID, hash []byte, expiresAt time.Time) error
	FindByHash(ctx context.Context, hash []byte) (*repository.RefreshToken, error)
	Revoke(ctx context.Context, id uuid.UUID) (bool, error)
	RevokeFamily(ctx context.Context, familyID uuid.UUID) error
	RevokeAllForUser(ctx context.Context, userID uuid.UUID) error
}

type Claims struct {
	Role domain.Role `json:"role"`
	jwt.RegisteredClaims
}

type Session struct {
	User             *domain.User
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
}

type AuthService struct {
	users       UserStore
	tokens      TokenStore
	key         *rsa.PrivateKey
	issuer      string
	accessTTL   time.Duration
	refreshTTL  time.Duration
	adminEmails map[string]struct{}
	dummyHash   []byte
	now         func() time.Time
}

type AuthConfig struct {
	Key         *rsa.PrivateKey
	Issuer      string
	AccessTTL   time.Duration
	RefreshTTL  time.Duration
	AdminEmails map[string]struct{}
}

func NewAuthService(users UserStore, tokens TokenStore, cfg AuthConfig) (*AuthService, error) {
	if cfg.Key == nil {
		slog.Warn("JWT_PRIVATE_KEY not set — generating ephemeral key (development only)")
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, err
		}
		cfg.Key = k
	}
	dummy, err := bcrypt.GenerateFromPassword([]byte("timing-equalizer"), bcryptCost)
	if err != nil {
		return nil, err
	}
	return &AuthService{
		users: users, tokens: tokens, key: cfg.Key, issuer: cfg.Issuer,
		accessTTL: cfg.AccessTTL, refreshTTL: cfg.RefreshTTL, adminEmails: cfg.AdminEmails,
		dummyHash: dummy, now: time.Now,
	}, nil
}

// ValidatePassword aplica la política mínima de contraseñas.
func ValidatePassword(p string) error {
	if len(p) < 8 || len(p) > 72 {
		return domain.ErrWeakPassword
	}
	var letter, digit bool
	for _, r := range p {
		switch {
		case unicode.IsLetter(r):
			letter = true
		case unicode.IsDigit(r):
			digit = true
		}
	}
	if !letter || !digit {
		return domain.ErrWeakPassword
	}
	return nil
}

func NormalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func (s *AuthService) HashPassword(p string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(p), bcryptCost)
	return string(h), err
}

func (s *AuthService) Register(ctx context.Context, name, email, password, phone string) (*Session, error) {
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	hash, err := s.HashPassword(password)
	if err != nil {
		return nil, err
	}
	u := &domain.User{
		Email: NormalizeEmail(email), Name: strings.TrimSpace(name), Phone: strings.TrimSpace(phone),
		PasswordHash: &hash, Role: domain.RoleClient,
	}
	if err := s.users.Create(ctx, u); err != nil {
		return nil, err
	}
	return s.issue(ctx, u, uuid.New())
}

func (s *AuthService) Login(ctx context.Context, email, password string) (*Session, error) {
	u, err := s.users.FindByEmail(ctx, NormalizeEmail(email))
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}
	if u == nil || u.PasswordHash == nil {
		// Igualamos el tiempo de respuesta para no revelar qué emails existen.
		_ = bcrypt.CompareHashAndPassword(s.dummyHash, []byte(password))
		return nil, domain.ErrInvalidCredentials
	}
	if bcrypt.CompareHashAndPassword([]byte(*u.PasswordHash), []byte(password)) != nil {
		return nil, domain.ErrInvalidCredentials
	}
	if err := s.maybePromote(ctx, u); err != nil {
		return nil, err
	}
	return s.issue(ctx, u, uuid.New())
}

// Refresh rota el refresh token. Si se presenta un token ya revocado se asume robo
// y se revoca toda la familia de sesiones.
func (s *AuthService) Refresh(ctx context.Context, raw string) (*Session, error) {
	if raw == "" {
		return nil, domain.ErrInvalidToken
	}
	t, err := s.tokens.FindByHash(ctx, hashToken(raw))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, domain.ErrInvalidToken
		}
		return nil, err
	}
	if t.RevokedAt != nil {
		slog.Warn("refresh token reuse detected — revoking family", slog.String("user_id", t.UserID.String()))
		_ = s.tokens.RevokeFamily(ctx, t.FamilyID)
		return nil, domain.ErrInvalidToken
	}
	if s.now().After(t.ExpiresAt) {
		return nil, domain.ErrInvalidToken
	}
	ok, err := s.tokens.Revoke(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	if !ok { // otra petición concurrente lo rotó primero
		_ = s.tokens.RevokeFamily(ctx, t.FamilyID)
		return nil, domain.ErrInvalidToken
	}
	u, err := s.users.FindByID(ctx, t.UserID)
	if err != nil {
		return nil, domain.ErrInvalidToken
	}
	return s.issue(ctx, u, t.FamilyID)
}

func (s *AuthService) Logout(ctx context.Context, raw string) error {
	if raw == "" {
		return nil
	}
	t, err := s.tokens.FindByHash(ctx, hashToken(raw))
	if err != nil {
		return nil
	}
	return s.tokens.RevokeFamily(ctx, t.FamilyID)
}

func (s *AuthService) LogoutAll(ctx context.Context, userID uuid.UUID) error {
	return s.tokens.RevokeAllForUser(ctx, userID)
}

func (s *AuthService) UpdateProfile(ctx context.Context, userID uuid.UUID, name, phone string) (*domain.User, error) {
	if err := s.users.UpdateProfile(ctx, userID, strings.TrimSpace(name), strings.TrimSpace(phone)); err != nil {
		return nil, err
	}
	return s.users.FindByID(ctx, userID)
}

// ChangePassword permite fijar contraseña a cuentas sociales (sin current) o cambiarla.
func (s *AuthService) ChangePassword(ctx context.Context, userID uuid.UUID, current, next string) error {
	u, err := s.users.FindByID(ctx, userID)
	if err != nil {
		return err
	}
	if u.PasswordHash != nil && bcrypt.CompareHashAndPassword([]byte(*u.PasswordHash), []byte(current)) != nil {
		return domain.ErrInvalidCredentials
	}
	if err := ValidatePassword(next); err != nil {
		return err
	}
	hash, err := s.HashPassword(next)
	if err != nil {
		return err
	}
	if err := s.users.UpdatePassword(ctx, userID, hash); err != nil {
		return err
	}
	return s.tokens.RevokeAllForUser(ctx, userID)
}

// LoginWithProfile inicia sesión con una identidad social, vinculándola o creando la cuenta.
func (s *AuthService) LoginWithProfile(ctx context.Context, p *Profile) (*Session, error) {
	u, err := s.users.FindByIdentity(ctx, p.Provider, p.Subject)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}

	if u == nil {
		if !p.EmailVerified || p.Email == "" {
			return nil, fmt.Errorf("%w: provider did not return a verified email", domain.ErrInvalidCredentials)
		}
		email := NormalizeEmail(p.Email)
		u, err = s.users.FindByEmail(ctx, email)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			u = &domain.User{Email: email, Name: p.DisplayName(), AvatarURL: p.AvatarURL, Role: domain.RoleClient, EmailVerified: true}
			if err := s.users.Create(ctx, u); err != nil {
				return nil, err
			}
		case err != nil:
			return nil, err
		case !u.EmailVerified && u.PasswordHash != nil:
			// Evita el "pre-account hijacking": alguien pudo registrar este email sin verificarlo.
			// El dueño real demuestra control del email vía el proveedor, así que invalidamos la contraseña previa.
			if err := s.users.UpdatePassword(ctx, u.ID, ""); err != nil {
				return nil, err
			}
			_ = s.tokens.RevokeAllForUser(ctx, u.ID)
		}
		if err := s.users.LinkIdentity(ctx, u.ID, p.Provider, p.Subject, email); err != nil {
			return nil, err
		}
	}

	if err := s.users.FillMissing(ctx, u.ID, p.AvatarURL, p.EmailVerified); err != nil {
		return nil, err
	}
	if u, err = s.users.FindByID(ctx, u.ID); err != nil {
		return nil, err
	}
	if err := s.maybePromote(ctx, u); err != nil {
		return nil, err
	}
	return s.issue(ctx, u, uuid.New())
}

// maybePromote concede rol admin a los emails de ADMIN_EMAILS, solo si el email está verificado.
func (s *AuthService) maybePromote(ctx context.Context, u *domain.User) error {
	if u.Role == domain.RoleAdmin || !u.EmailVerified {
		return nil
	}
	if _, ok := s.adminEmails[NormalizeEmail(u.Email)]; !ok {
		return nil
	}
	if err := s.users.SetRole(ctx, u.ID, domain.RoleAdmin); err != nil {
		return err
	}
	u.Role = domain.RoleAdmin
	slog.Info("user promoted to admin", slog.String("user_id", u.ID.String()))
	return nil
}

func (s *AuthService) issue(ctx context.Context, u *domain.User, familyID uuid.UUID) (*Session, error) {
	now := s.now()
	accessExp := now.Add(s.accessTTL)
	access, err := jwt.NewWithClaims(jwt.SigningMethodRS256, Claims{
		Role: u.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   u.ID.String(),
			Issuer:    s.issuer,
			Audience:  jwt.ClaimStrings{s.issuer},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(accessExp),
			ID:        uuid.NewString(),
		},
	}).SignedString(s.key)
	if err != nil {
		return nil, err
	}

	raw, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	refreshExp := now.Add(s.refreshTTL)
	if err := s.tokens.Create(ctx, u.ID, familyID, hashToken(raw), refreshExp); err != nil {
		return nil, err
	}
	return &Session{User: u, AccessToken: access, AccessExpiresAt: accessExp, RefreshToken: raw, RefreshExpiresAt: refreshExp}, nil
}

func (s *AuthService) ParseAccessToken(token string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		return &s.key.PublicKey, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
		jwt.WithIssuer(s.issuer),
		jwt.WithAudience(s.issuer),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(s.now),
	)
	if err != nil {
		return nil, domain.ErrInvalidToken
	}
	return claims, nil
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(raw string) []byte {
	h := sha256.Sum256([]byte(raw))
	return h[:]
}

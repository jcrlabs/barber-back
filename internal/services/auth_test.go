package services

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"testing"
	"time"

	"github.com/jcrlabs/barber-back/internal/domain"
)

var testKey, _ = rsa.GenerateKey(rand.Reader, 2048)

func newAuth(t *testing.T, admins ...string) (*AuthService, *fakeUsers, *fakeTokens) {
	t.Helper()
	users, tokens := newFakeUsers(), newFakeTokens()
	adm := map[string]struct{}{}
	for _, a := range admins {
		adm[a] = struct{}{}
	}
	s, err := NewAuthService(users, tokens, AuthConfig{
		Key: testKey, Issuer: "test", AccessTTL: 15 * time.Minute, RefreshTTL: time.Hour, AdminEmails: adm,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, users, tokens
}

func TestValidatePassword(t *testing.T) {
	tests := map[string]bool{
		"short1":                 false,
		"onlyletters":            false,
		"12345678":               false,
		"navaja2026":             true,
		"Ñandú-con-tilde-9":      true,
		string(make([]byte, 73)): false,
	}
	for pw, ok := range tests {
		if err := ValidatePassword(pw); (err == nil) != ok {
			t.Errorf("ValidatePassword(%q) = %v, want ok=%v", pw, err, ok)
		}
	}
}

func TestRegisterLoginAndTokens(t *testing.T) {
	s, _, _ := newAuth(t)
	ctx := context.Background()

	sess, err := s.Register(ctx, "Ana", "  ANA@Example.com ", "navaja2026", "")
	if err != nil {
		t.Fatal(err)
	}
	if sess.User.Email != "ana@example.com" || sess.User.Role != domain.RoleClient {
		t.Fatalf("unexpected user %+v", sess.User)
	}
	if _, err := s.Register(ctx, "Ana", "ana@example.com", "navaja2026", ""); !errors.Is(err, domain.ErrEmailTaken) {
		t.Fatalf("duplicate: %v", err)
	}

	claims, err := s.ParseAccessToken(sess.AccessToken)
	if err != nil || claims.Subject != sess.User.ID.String() || claims.Role != domain.RoleClient {
		t.Fatalf("parse: %v %+v", err, claims)
	}

	if _, err := s.Login(ctx, "ana@example.com", "wrong-pass1"); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := s.Login(ctx, "nobody@example.com", "navaja2026"); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("unknown user: %v", err)
	}
	if _, err := s.Login(ctx, "Ana@example.com", "navaja2026"); err != nil {
		t.Fatalf("login: %v", err)
	}
}

func TestAccessTokenRejections(t *testing.T) {
	s, _, _ := newAuth(t)
	sess, _ := s.Register(context.Background(), "Ana", "ana@example.com", "navaja2026", "")

	s.now = func() time.Time { return time.Now().Add(time.Hour) }
	if _, err := s.ParseAccessToken(sess.AccessToken); err == nil {
		t.Fatal("expired token accepted")
	}
	s.now = time.Now

	other, _, _ := newAuth(t)
	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	other.key = otherKey
	if _, err := other.ParseAccessToken(sess.AccessToken); err == nil {
		t.Fatal("token signed with another key accepted")
	}
	if _, err := s.ParseAccessToken(sess.AccessToken + "x"); err == nil {
		t.Fatal("tampered token accepted")
	}
}

func TestRefreshRotationAndReuseDetection(t *testing.T) {
	s, _, tokens := newAuth(t)
	ctx := context.Background()
	first, _ := s.Register(ctx, "Ana", "ana@example.com", "navaja2026", "")

	second, err := s.Refresh(ctx, first.RefreshToken)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if second.RefreshToken == first.RefreshToken {
		t.Fatal("refresh token was not rotated")
	}

	// Reutilizar el token antiguo debe invalidar toda la familia.
	if _, err := s.Refresh(ctx, first.RefreshToken); !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("reuse: %v", err)
	}
	if _, err := s.Refresh(ctx, second.RefreshToken); !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("family not revoked: %v", err)
	}
	if n := tokens.activeFor(first.User.ID); n != 0 {
		t.Fatalf("%d tokens still active", n)
	}
}

func TestSocialLogin(t *testing.T) {
	ctx := context.Background()

	t.Run("crea cuenta y promueve admin verificado", func(t *testing.T) {
		s, _, _ := newAuth(t, "boss@navaja.es")
		sess, err := s.LoginWithProfile(ctx, &Profile{Provider: "google", Subject: "g1", Email: "Boss@navaja.es", EmailVerified: true, Name: "Boss"})
		if err != nil {
			t.Fatal(err)
		}
		if sess.User.Role != domain.RoleAdmin || !sess.User.EmailVerified {
			t.Fatalf("unexpected %+v", sess.User)
		}
		again, _ := s.LoginWithProfile(ctx, &Profile{Provider: "google", Subject: "g1", Email: "boss@navaja.es", EmailVerified: true})
		if again.User.ID != sess.User.ID {
			t.Fatal("identity not reused")
		}
	})

	t.Run("rechaza email no verificado", func(t *testing.T) {
		s, _, _ := newAuth(t)
		if _, err := s.LoginWithProfile(ctx, &Profile{Provider: "github", Subject: "1", Email: "x@y.z"}); !errors.Is(err, domain.ErrInvalidCredentials) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("registro con password no da admin sin verificar", func(t *testing.T) {
		s, _, _ := newAuth(t, "boss@navaja.es")
		sess, _ := s.Register(ctx, "Fake", "boss@navaja.es", "navaja2026", "")
		if sess.User.Role == domain.RoleAdmin {
			t.Fatal("unverified email promoted to admin")
		}
	})

	t.Run("anula contraseña de cuenta pre-registrada por terceros", func(t *testing.T) {
		s, users, _ := newAuth(t)
		pre, _ := s.Register(ctx, "Attacker", "victim@mail.com", "navaja2026", "")
		sess, err := s.LoginWithProfile(ctx, &Profile{Provider: "google", Subject: "v", Email: "victim@mail.com", EmailVerified: true})
		if err != nil {
			t.Fatal(err)
		}
		if sess.User.ID != pre.User.ID {
			t.Fatal("account not linked")
		}
		u, _ := users.FindByID(ctx, pre.User.ID)
		if u.PasswordHash != nil {
			t.Fatal("attacker password still valid")
		}
		if _, err := s.Login(ctx, "victim@mail.com", "navaja2026"); !errors.Is(err, domain.ErrInvalidCredentials) {
			t.Fatalf("attacker login: %v", err)
		}
	})
}

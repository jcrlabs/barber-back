package server

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jcrlabs/barber-back/internal/domain"
	"github.com/jcrlabs/barber-back/internal/repository"
	"golang.org/x/crypto/bcrypt"
)

// BootstrapAdmin crea la cuenta de administración inicial si todavía no existe.
// Si ya existe no la toca: así el cambio de contraseña desde el perfil no se pierde al reiniciar.
func BootstrapAdmin(ctx context.Context, users *repository.UserRepository, login, password string) error {
	if _, err := users.FindByEmail(ctx, login); err == nil {
		return nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return err
	}
	h := string(hash)
	u := &domain.User{Email: login, Name: "Admin", PasswordHash: &h, Role: domain.RoleAdmin, EmailVerified: true}
	if err := users.Create(ctx, u); err != nil {
		return err
	}
	slog.Info("bootstrap admin created", slog.String("user_id", u.ID.String()))
	return nil
}

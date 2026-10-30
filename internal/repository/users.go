package repository

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jcrlabs/barber-back/internal/domain"
)

type UserRepository struct{ db *pgxpool.Pool }

func NewUserRepository(db *pgxpool.Pool) *UserRepository { return &UserRepository{db: db} }

const userColumns = `u.id, u.email, u.name, u.phone, u.avatar_url, u.password_hash, u.role, u.email_verified, u.created_at,
	COALESCE((SELECT array_agg(provider ORDER BY provider) FROM oauth_identities oi WHERE oi.user_id = u.id), '{}')`

func scanUser(row pgx.Row) (*domain.User, error) {
	var u domain.User
	if err := row.Scan(&u.ID, &u.Email, &u.Name, &u.Phone, &u.AvatarURL, &u.PasswordHash,
		&u.Role, &u.EmailVerified, &u.CreatedAt, &u.Providers); err != nil {
		return nil, mapErr(err)
	}
	u.HasPassword = u.PasswordHash != nil
	return &u, nil
}

func (r *UserRepository) Create(ctx context.Context, u *domain.User) error {
	err := r.db.QueryRow(ctx, `
		INSERT INTO users (email, name, phone, avatar_url, password_hash, role, email_verified)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at`,
		u.Email, u.Name, u.Phone, u.AvatarURL, u.PasswordHash, u.Role, u.EmailVerified,
	).Scan(&u.ID, &u.CreatedAt)
	if err := mapErr(err); errors.Is(err, domain.ErrConflict) {
		return domain.ErrEmailTaken
	} else if err != nil {
		return err
	}
	u.HasPassword = u.PasswordHash != nil
	if u.Providers == nil {
		u.Providers = []string{}
	}
	return nil
}

func (r *UserRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	return scanUser(r.db.QueryRow(ctx, `SELECT `+userColumns+` FROM users u WHERE u.id = $1`, id))
}

func (r *UserRepository) FindByEmail(ctx context.Context, email string) (*domain.User, error) {
	return scanUser(r.db.QueryRow(ctx, `SELECT `+userColumns+` FROM users u WHERE lower(u.email) = lower($1)`, email))
}

func (r *UserRepository) FindByIdentity(ctx context.Context, provider, subject string) (*domain.User, error) {
	return scanUser(r.db.QueryRow(ctx, `
		SELECT `+userColumns+` FROM users u
		JOIN oauth_identities i ON i.user_id = u.id
		WHERE i.provider = $1 AND i.subject = $2`, provider, subject))
}

func (r *UserRepository) LinkIdentity(ctx context.Context, userID uuid.UUID, provider, subject, email string) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO oauth_identities (provider, subject, user_id, email) VALUES ($1, $2, $3, $4)
		ON CONFLICT (provider, subject) DO NOTHING`, provider, subject, userID, email)
	return mapErr(err)
}

func (r *UserRepository) UpdateProfile(ctx context.Context, id uuid.UUID, name, phone string) error {
	tag, err := r.db.Exec(ctx, `UPDATE users SET name = $2, phone = $3, updated_at = now() WHERE id = $1`, id, name, phone)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *UserRepository) UpdatePassword(ctx context.Context, id uuid.UUID, hash string) error {
	_, err := r.db.Exec(ctx, `UPDATE users SET password_hash = NULLIF($2, ''), updated_at = now() WHERE id = $1`, id, hash)
	return mapErr(err)
}

// FillMissing completa avatar y verificación tras un login social sin pisar datos del usuario.
func (r *UserRepository) FillMissing(ctx context.Context, id uuid.UUID, avatarURL string, verified bool) error {
	_, err := r.db.Exec(ctx, `
		UPDATE users SET
			avatar_url = CASE WHEN avatar_url = '' THEN $2 ELSE avatar_url END,
			email_verified = email_verified OR $3,
			updated_at = now()
		WHERE id = $1`, id, avatarURL, verified)
	return mapErr(err)
}

func (r *UserRepository) SetRole(ctx context.Context, id uuid.UUID, role domain.Role) error {
	tag, err := r.db.Exec(ctx, `UPDATE users SET role = $2, updated_at = now() WHERE id = $1`, id, role)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *UserRepository) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	return mapErr(err)
}

// List busca usuarios por nombre o email. El término siempre viaja como parámetro.
func (r *UserRepository) List(ctx context.Context, search string, role domain.Role, limit, offset int) ([]domain.User, int, error) {
	where := `WHERE ($1 = '' OR u.name ILIKE '%' || $1 || '%' OR u.email ILIKE '%' || $1 || '%')`
	args := []any{search}
	if role != "" {
		args = append(args, role)
		where += ` AND u.role = $` + strconv.Itoa(len(args))
	}

	var total int
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM users u `+where, args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}

	args = append(args, limit, offset)
	rows, err := r.db.Query(ctx, `SELECT `+userColumns+` FROM users u `+where+
		` ORDER BY u.created_at DESC LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()

	users := []domain.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, 0, err
		}
		users = append(users, *u)
	}
	return users, total, rows.Err()
}

// ─── Refresh tokens ──────────────────────────────────────────────────────────

type RefreshToken struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	FamilyID  uuid.UUID
	ExpiresAt time.Time
	RevokedAt *time.Time
}

type TokenRepository struct{ db *pgxpool.Pool }

func NewTokenRepository(db *pgxpool.Pool) *TokenRepository { return &TokenRepository{db: db} }

func (r *TokenRepository) Create(ctx context.Context, userID, familyID uuid.UUID, hash []byte, expiresAt time.Time) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO refresh_tokens (user_id, family_id, token_hash, expires_at) VALUES ($1, $2, $3, $4)`,
		userID, familyID, hash, expiresAt)
	return mapErr(err)
}

func (r *TokenRepository) FindByHash(ctx context.Context, hash []byte) (*RefreshToken, error) {
	var t RefreshToken
	err := r.db.QueryRow(ctx, `
		SELECT id, user_id, family_id, expires_at, revoked_at FROM refresh_tokens WHERE token_hash = $1`, hash,
	).Scan(&t.ID, &t.UserID, &t.FamilyID, &t.ExpiresAt, &t.RevokedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &t, nil
}

// Revoke marca un token como revocado. Devuelve false si ya lo estaba (posible reutilización).
func (r *TokenRepository) Revoke(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := r.db.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return false, mapErr(err)
	}
	return tag.RowsAffected() == 1, nil
}

func (r *TokenRepository) RevokeFamily(ctx context.Context, familyID uuid.UUID) error {
	_, err := r.db.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() WHERE family_id = $1 AND revoked_at IS NULL`, familyID)
	return mapErr(err)
}

func (r *TokenRepository) RevokeAllForUser(ctx context.Context, userID uuid.UUID) error {
	_, err := r.db.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID)
	return mapErr(err)
}

func (r *TokenRepository) PurgeExpired(ctx context.Context) (int64, error) {
	tag, err := r.db.Exec(ctx, `DELETE FROM refresh_tokens WHERE expires_at < now() - interval '1 day'`)
	if err != nil {
		return 0, mapErr(err)
	}
	return tag.RowsAffected(), nil
}

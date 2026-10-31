package repository

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jcrlabs/barber-back/internal/domain"
)

type CatalogRepository struct{ db *pgxpool.Pool }

func NewCatalogRepository(db *pgxpool.Pool) *CatalogRepository { return &CatalogRepository{db: db} }

// ─── Services ────────────────────────────────────────────────────────────────

const serviceColumns = `id, name, description, duration_min, price_cents, active, sort_order`

func scanService(row pgx.Row) (*domain.Service, error) {
	var s domain.Service
	if err := row.Scan(&s.ID, &s.Name, &s.Description, &s.DurationMin, &s.PriceCents, &s.Active, &s.SortOrder); err != nil {
		return nil, mapErr(err)
	}
	return &s, nil
}

func (r *CatalogRepository) ListServices(ctx context.Context, includeInactive bool) ([]domain.Service, error) {
	rows, err := r.db.Query(ctx, `SELECT `+serviceColumns+` FROM services
		WHERE active OR $1 ORDER BY sort_order, name`, includeInactive)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []domain.Service{}
	for rows.Next() {
		s, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func (r *CatalogRepository) GetService(ctx context.Context, id uuid.UUID) (*domain.Service, error) {
	return scanService(r.db.QueryRow(ctx, `SELECT `+serviceColumns+` FROM services WHERE id = $1`, id))
}

func (r *CatalogRepository) CreateService(ctx context.Context, s *domain.Service) error {
	return mapErr(r.db.QueryRow(ctx, `
		INSERT INTO services (name, description, duration_min, price_cents, active, sort_order)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		s.Name, s.Description, s.DurationMin, s.PriceCents, s.Active, s.SortOrder).Scan(&s.ID))
}

func (r *CatalogRepository) UpdateService(ctx context.Context, s *domain.Service) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE services SET name = $2, description = $3, duration_min = $4, price_cents = $5,
			active = $6, sort_order = $7, updated_at = now()
		WHERE id = $1`,
		s.ID, s.Name, s.Description, s.DurationMin, s.PriceCents, s.Active, s.SortOrder)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// DeleteService borra el servicio; si tiene citas asociadas lo desactiva.
func (r *CatalogRepository) DeleteService(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, `DELETE FROM services WHERE id = $1`, id)
	if errors.Is(mapErr(err), domain.ErrConflict) {
		_, err = r.db.Exec(ctx, `UPDATE services SET active = false, updated_at = now() WHERE id = $1`, id)
	}
	return mapErr(err)
}

// ─── Barbers ─────────────────────────────────────────────────────────────────

const barberColumns = `id, user_id, name, title, bio, avatar_url, active`

func (r *CatalogRepository) ListBarbers(ctx context.Context, includeInactive bool) ([]domain.Barber, error) {
	rows, err := r.db.Query(ctx, `SELECT `+barberColumns+` FROM barbers WHERE active OR $1 ORDER BY name`, includeInactive)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	out := []domain.Barber{}
	index := map[uuid.UUID]int{}
	ids := []uuid.UUID{}
	for rows.Next() {
		var b domain.Barber
		if err := rows.Scan(&b.ID, &b.UserID, &b.Name, &b.Title, &b.Bio, &b.AvatarURL, &b.Active); err != nil {
			return nil, mapErr(err)
		}
		b.Hours = []domain.WorkingHours{}
		index[b.ID] = len(out)
		ids = append(ids, b.ID)
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	if len(ids) == 0 {
		return out, nil
	}

	hrows, err := r.db.Query(ctx, `SELECT barber_id, weekday, start_min, end_min FROM barber_hours
		WHERE barber_id = ANY($1) ORDER BY weekday, start_min`, ids)
	if err != nil {
		return nil, mapErr(err)
	}
	defer hrows.Close()
	for hrows.Next() {
		var id uuid.UUID
		var h domain.WorkingHours
		if err := hrows.Scan(&id, &h.Weekday, &h.StartMin, &h.EndMin); err != nil {
			return nil, mapErr(err)
		}
		out[index[id]].Hours = append(out[index[id]].Hours, h)
	}
	return out, hrows.Err()
}

func (r *CatalogRepository) GetBarber(ctx context.Context, id uuid.UUID) (*domain.Barber, error) {
	return r.getBarberWhere(ctx, `id = $1`, id)
}

// getBarberWhere recibe una condición fija definida en este paquete; el valor siempre va como parámetro.
func (r *CatalogRepository) getBarberWhere(ctx context.Context, cond string, arg any) (*domain.Barber, error) {
	var b domain.Barber
	err := r.db.QueryRow(ctx, `SELECT `+barberColumns+` FROM barbers WHERE `+cond, arg).
		Scan(&b.ID, &b.UserID, &b.Name, &b.Title, &b.Bio, &b.AvatarURL, &b.Active)
	if err != nil {
		return nil, mapErr(err)
	}
	hours, err := r.HoursFor(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	b.Hours = hours
	return &b, nil
}

func (r *CatalogRepository) HoursFor(ctx context.Context, barberID uuid.UUID) ([]domain.WorkingHours, error) {
	rows, err := r.db.Query(ctx, `SELECT weekday, start_min, end_min FROM barber_hours
		WHERE barber_id = $1 ORDER BY weekday, start_min`, barberID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []domain.WorkingHours{}
	for rows.Next() {
		var h domain.WorkingHours
		if err := rows.Scan(&h.Weekday, &h.StartMin, &h.EndMin); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (r *CatalogRepository) CreateBarber(ctx context.Context, b *domain.Barber) error {
	return pgx.BeginFunc(ctx, r.db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO barbers (user_id, name, title, bio, avatar_url, active)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			b.UserID, b.Name, b.Title, b.Bio, b.AvatarURL, b.Active).Scan(&b.ID); err != nil {
			return mapErr(err)
		}
		return replaceHours(ctx, tx, b.ID, b.Hours)
	})
}

func (r *CatalogRepository) UpdateBarber(ctx context.Context, b *domain.Barber) error {
	return pgx.BeginFunc(ctx, r.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE barbers SET user_id = $2, name = $3, title = $4, bio = $5, avatar_url = $6,
				active = $7, updated_at = now()
			WHERE id = $1`, b.ID, b.UserID, b.Name, b.Title, b.Bio, b.AvatarURL, b.Active)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return replaceHours(ctx, tx, b.ID, b.Hours)
	})
}

func replaceHours(ctx context.Context, tx pgx.Tx, barberID uuid.UUID, hours []domain.WorkingHours) error {
	if _, err := tx.Exec(ctx, `DELETE FROM barber_hours WHERE barber_id = $1`, barberID); err != nil {
		return mapErr(err)
	}
	for _, h := range hours {
		if _, err := tx.Exec(ctx, `INSERT INTO barber_hours (barber_id, weekday, start_min, end_min)
			VALUES ($1, $2, $3, $4)`, barberID, h.Weekday, h.StartMin, h.EndMin); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

// DeleteBarber borra el barbero; si tiene historial de citas lo desactiva.
func (r *CatalogRepository) DeleteBarber(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, `DELETE FROM barbers WHERE id = $1`, id)
	if errors.Is(mapErr(err), domain.ErrConflict) {
		_, err = r.db.Exec(ctx, `UPDATE barbers SET active = false, updated_at = now() WHERE id = $1`, id)
	}
	return mapErr(err)
}

func (r *CatalogRepository) IsEmpty(ctx context.Context) (bool, error) {
	var n int
	err := r.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM services) + (SELECT count(*) FROM barbers)`).Scan(&n)
	return n == 0, mapErr(err)
}

// ─── Excepciones de horario ──────────────────────────────────────────────────

const overrideColumns = `id, barber_id, to_char(day, 'YYYY-MM-DD'), kind, start_min, end_min, note`

// ListOverrides devuelve las excepciones con fecha en [from, to] (YYYY-MM-DD, inclusive).
func (r *CatalogRepository) ListOverrides(ctx context.Context, from, to string) ([]domain.DayOverride, error) {
	rows, err := r.db.Query(ctx, `SELECT `+overrideColumns+` FROM schedule_overrides
		WHERE day BETWEEN $1::date AND $2::date ORDER BY day, start_min NULLS FIRST`, from, to)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []domain.DayOverride{}
	for rows.Next() {
		var o domain.DayOverride
		if err := rows.Scan(&o.ID, &o.BarberID, &o.Day, &o.Kind, &o.StartMin, &o.EndMin, &o.Note); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, o)
	}
	return out, mapErr(rows.Err())
}

func (r *CatalogRepository) CreateOverride(ctx context.Context, o *domain.DayOverride) error {
	return mapErr(r.db.QueryRow(ctx, `
		INSERT INTO schedule_overrides (barber_id, day, kind, start_min, end_min, note)
		VALUES ($1, $2::date, $3, $4, $5, $6) RETURNING id`,
		o.BarberID, o.Day, o.Kind, o.StartMin, o.EndMin, o.Note).Scan(&o.ID))
}

func (r *CatalogRepository) DeleteOverride(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM schedule_overrides WHERE id = $1`, id)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

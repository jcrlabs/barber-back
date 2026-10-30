package repository

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jcrlabs/barber-back/internal/domain"
)

type AppointmentRepository struct{ db *pgxpool.Pool }

func NewAppointmentRepository(db *pgxpool.Pool) *AppointmentRepository {
	return &AppointmentRepository{db: db}
}

const appointmentSelect = `
	SELECT a.id, a.client_id, a.barber_id, a.service_id, a.starts_at, a.ends_at, a.status,
	       a.price_cents, a.notes, a.created_at, u.name, u.email, u.phone, b.name, s.name
	FROM appointments a
	JOIN users u ON u.id = a.client_id
	JOIN barbers b ON b.id = a.barber_id
	JOIN services s ON s.id = a.service_id`

func scanAppointment(row pgx.Row) (*domain.Appointment, error) {
	var a domain.Appointment
	if err := row.Scan(&a.ID, &a.ClientID, &a.BarberID, &a.ServiceID, &a.StartsAt, &a.EndsAt, &a.Status,
		&a.PriceCents, &a.Notes, &a.CreatedAt, &a.ClientName, &a.ClientEmail, &a.ClientPhone,
		&a.BarberName, &a.ServiceName); err != nil {
		return nil, mapErr(err)
	}
	return &a, nil
}

func collect(rows pgx.Rows, err error) ([]domain.Appointment, error) {
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []domain.Appointment{}
	for rows.Next() {
		a, err := scanAppointment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, mapErr(rows.Err())
}

func (r *AppointmentRepository) Create(ctx context.Context, a *domain.Appointment) error {
	return mapErr(r.db.QueryRow(ctx, `
		INSERT INTO appointments (client_id, barber_id, service_id, starts_at, ends_at, status, price_cents, notes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id, created_at`,
		a.ClientID, a.BarberID, a.ServiceID, a.StartsAt, a.EndsAt, a.Status, a.PriceCents, a.Notes,
	).Scan(&a.ID, &a.CreatedAt))
}

func (r *AppointmentRepository) Get(ctx context.Context, id uuid.UUID) (*domain.Appointment, error) {
	return scanAppointment(r.db.QueryRow(ctx, appointmentSelect+` WHERE a.id = $1`, id))
}

// BusyIntervals devuelve los intervalos ocupados de un barbero que solapan [from, to).
func (r *AppointmentRepository) BusyIntervals(ctx context.Context, barberID uuid.UUID, from, to time.Time) ([]domain.Interval, error) {
	rows, err := r.db.Query(ctx, `
		SELECT starts_at, ends_at FROM appointments
		WHERE barber_id = $1 AND status IN ('confirmed', 'completed')
		  AND starts_at < $3 AND ends_at > $2
		ORDER BY starts_at`, barberID, from, to)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []domain.Interval{}
	for rows.Next() {
		var iv domain.Interval
		if err := rows.Scan(&iv.Start, &iv.End); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, iv)
	}
	return out, mapErr(rows.Err())
}

func (r *AppointmentRepository) CountUpcomingForClient(ctx context.Context, clientID uuid.UUID, now time.Time) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `SELECT count(*) FROM appointments
		WHERE client_id = $1 AND status = 'confirmed' AND starts_at > $2`, clientID, now).Scan(&n)
	return n, mapErr(err)
}

func (r *AppointmentRepository) ListForClient(ctx context.Context, clientID uuid.UUID) ([]domain.Appointment, error) {
	return collect(r.db.Query(ctx, appointmentSelect+` WHERE a.client_id = $1 ORDER BY a.starts_at DESC LIMIT 100`, clientID))
}

type AppointmentFilter struct {
	BarberID *uuid.UUID
	Status   domain.AppointmentStatus
	From     *time.Time
	To       *time.Time
	Limit    int
	Offset   int
}

// List construye un WHERE dinámico: solo se interpolan índices de placeholder, nunca valores.
func (r *AppointmentRepository) List(ctx context.Context, f AppointmentFilter) ([]domain.Appointment, int, error) {
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	if f.BarberID != nil {
		add("a.barber_id = ?", *f.BarberID)
	}
	if f.Status != "" {
		add("a.status = ?", f.Status)
	}
	if f.From != nil {
		add("a.starts_at >= ?", *f.From)
	}
	if f.To != nil {
		add("a.starts_at < ?", *f.To)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	var total int
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM appointments a`+where, args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	args = append(args, f.Limit, f.Offset)
	list, err := collect(r.db.Query(ctx, appointmentSelect+where+` ORDER BY a.starts_at ASC LIMIT $`+
		strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...))
	return list, total, err
}

func (r *AppointmentRepository) SetStatus(ctx context.Context, id uuid.UUID, status domain.AppointmentStatus) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE appointments SET status = $2, updated_at = now(),
			cancelled_at = CASE WHEN $2 = 'cancelled' THEN now() ELSE cancelled_at END
		WHERE id = $1`, id, status)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

type Stats struct {
	TodayCount        int    `json:"today_count"`
	UpcomingCount     int    `json:"upcoming_count"`
	WeekRevenueCents  int    `json:"week_revenue_cents"`
	MonthRevenueCents int    `json:"month_revenue_cents"`
	ClientsCount      int    `json:"clients_count"`
	NoShowRatePct     int    `json:"no_show_rate_pct"`
	TopService        string `json:"top_service"`
}

func (r *AppointmentRepository) Stats(ctx context.Context, dayStart, dayEnd, weekStart, monthStart, now time.Time) (*Stats, error) {
	var s Stats
	err := r.db.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM appointments WHERE starts_at >= $1 AND starts_at < $2 AND status <> 'cancelled'),
			(SELECT count(*) FROM appointments WHERE starts_at > $5 AND status = 'confirmed'),
			(SELECT COALESCE(sum(price_cents), 0) FROM appointments WHERE starts_at >= $3 AND starts_at < $5 AND status IN ('completed', 'confirmed')),
			(SELECT COALESCE(sum(price_cents), 0) FROM appointments WHERE starts_at >= $4 AND starts_at < $5 AND status IN ('completed', 'confirmed')),
			(SELECT count(*) FROM users WHERE role = 'client'),
			(SELECT COALESCE(round(100.0 * count(*) FILTER (WHERE status = 'no_show') / NULLIF(count(*) FILTER (WHERE status IN ('completed', 'no_show')), 0)), 0)::int
			   FROM appointments WHERE starts_at >= $4),
			COALESCE((SELECT s.name FROM appointments a JOIN services s ON s.id = a.service_id
			   WHERE a.starts_at >= $4 AND a.status <> 'cancelled'
			   GROUP BY s.name ORDER BY count(*) DESC LIMIT 1), '')`,
		dayStart, dayEnd, weekStart, monthStart, now,
	).Scan(&s.TodayCount, &s.UpcomingCount, &s.WeekRevenueCents, &s.MonthRevenueCents,
		&s.ClientsCount, &s.NoShowRatePct, &s.TopService)
	return &s, mapErr(err)
}

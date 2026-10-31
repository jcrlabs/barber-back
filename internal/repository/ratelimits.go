package repository

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RateLimitRepository guarda los intentos en Postgres para que el límite sea el mismo en todas las réplicas.
type RateLimitRepository struct{ db *pgxpool.Pool }

func NewRateLimitRepository(db *pgxpool.Pool) *RateLimitRepository {
	return &RateLimitRepository{db: db}
}

// window cuenta los intentos vivos de la clave dentro de la transacción (serializada por clave).
func window(ctx context.Context, tx pgx.Tx, bucket, key string, win time.Duration) (int, time.Time, error) {
	// Serializa a las réplicas que tocan la misma clave a la vez.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1 || ':' || $2))`, bucket, key); err != nil {
		return 0, time.Time{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM rate_limit_hits WHERE bucket = $1 AND key = $2 AND at <= now() - make_interval(secs => $3)`,
		bucket, key, win.Seconds()); err != nil {
		return 0, time.Time{}, err
	}
	var n int
	var oldest *time.Time
	if err := tx.QueryRow(ctx, `SELECT count(*), min(at) FROM rate_limit_hits WHERE bucket = $1 AND key = $2`, bucket, key).
		Scan(&n, &oldest); err != nil {
		return 0, time.Time{}, err
	}
	if oldest == nil {
		return n, time.Time{}, nil
	}
	return n, *oldest, nil
}

func retryAfter(oldest time.Time, win time.Duration) time.Duration {
	if d := time.Until(oldest.Add(win)); d > 0 {
		return d
	}
	return time.Second
}

// Hit registra un intento si queda cupo. Devuelve false y la espera si ya no quedaba.
func (r *RateLimitRepository) Hit(ctx context.Context, bucket, key string, maxHits int, win time.Duration) (bool, time.Duration, error) {
	allowed, retry := false, time.Duration(0)
	err := pgx.BeginFunc(ctx, r.db, func(tx pgx.Tx) error {
		n, oldest, err := window(ctx, tx, bucket, key, win)
		if err != nil {
			return err
		}
		if n >= maxHits {
			retry = retryAfter(oldest, win)
			return nil
		}
		allowed = true
		_, err = tx.Exec(ctx, `INSERT INTO rate_limit_hits (bucket, key) VALUES ($1, $2)`, bucket, key)
		return err
	})
	return allowed, retry, err
}

// Blocked consulta si la clave agotó su cupo, sin registrar un intento.
func (r *RateLimitRepository) Blocked(ctx context.Context, bucket, key string, maxHits int, win time.Duration) (bool, time.Duration, error) {
	blocked, retry := false, time.Duration(0)
	err := pgx.BeginFunc(ctx, r.db, func(tx pgx.Tx) error {
		n, oldest, err := window(ctx, tx, bucket, key, win)
		if err != nil {
			return err
		}
		if n >= maxHits {
			blocked, retry = true, retryAfter(oldest, win)
		}
		return nil
	})
	return blocked, retry, err
}

func (r *RateLimitRepository) Reset(ctx context.Context, bucket, key string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM rate_limit_hits WHERE bucket = $1 AND key = $2`, bucket, key)
	return err
}

// Purge borra los intentos de más de un día (las ventanas más largas son de una hora).
func (r *RateLimitRepository) Purge(ctx context.Context) error {
	_, err := r.db.Exec(ctx, `DELETE FROM rate_limit_hits WHERE at < now() - interval '1 day'`)
	return err
}

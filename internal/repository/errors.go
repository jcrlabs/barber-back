package repository

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jcrlabs/barber-back/internal/domain"
)

const (
	pgUniqueViolation    = "23505"
	pgExclusionViolation = "23P01"
	pgForeignKeyViolate  = "23503"
)

// mapErr traduce errores de pgx a errores de dominio.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case pgUniqueViolation:
			return domain.ErrConflict
		case pgExclusionViolation:
			return domain.ErrSlotTaken
		case pgForeignKeyViolate:
			return domain.ErrConflict
		}
	}
	return err
}

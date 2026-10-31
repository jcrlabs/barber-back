// Package domain contiene las entidades y errores de negocio compartidos por todas las capas.
package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

type Role string

const (
	RoleClient Role = "client"
	RoleAdmin  Role = "admin"
)

func (r Role) Valid() bool { return r == RoleClient || r == RoleAdmin }

type AppointmentStatus string

const (
	StatusConfirmed AppointmentStatus = "confirmed"
	StatusCompleted AppointmentStatus = "completed"
	StatusCancelled AppointmentStatus = "cancelled"
	StatusNoShow    AppointmentStatus = "no_show"
)

func (s AppointmentStatus) Valid() bool {
	switch s {
	case StatusConfirmed, StatusCompleted, StatusCancelled, StatusNoShow:
		return true
	}
	return false
}

type User struct {
	ID            uuid.UUID `json:"id"`
	Email         string    `json:"email"`
	Name          string    `json:"name"`
	Phone         string    `json:"phone"`
	AvatarURL     string    `json:"avatar_url"`
	PasswordHash  *string   `json:"-"`
	Role          Role      `json:"role"`
	EmailVerified bool      `json:"email_verified"`
	HasPassword   bool      `json:"has_password"`
	Providers     []string  `json:"providers"`
	CreatedAt     time.Time `json:"created_at"`
}

type Service struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	DurationMin int       `json:"duration_min"`
	PriceCents  int       `json:"price_cents"`
	Active      bool      `json:"active"`
	SortOrder   int       `json:"sort_order"`
}

type WorkingHours struct {
	Weekday  int `json:"weekday"` // 0 = domingo (time.Weekday)
	StartMin int `json:"start_min"`
	EndMin   int `json:"end_min"`
}

type Barber struct {
	ID        uuid.UUID      `json:"id"`
	UserID    *uuid.UUID     `json:"user_id,omitempty"`
	Name      string         `json:"name"`
	Title     string         `json:"title"`
	Bio       string         `json:"bio"`
	AvatarURL string         `json:"avatar_url"`
	Active    bool           `json:"active"`
	Hours     []WorkingHours `json:"hours"`
}

type Appointment struct {
	ID          uuid.UUID         `json:"id"`
	ClientID    uuid.UUID         `json:"client_id"`
	BarberID    uuid.UUID         `json:"barber_id"`
	ServiceID   uuid.UUID         `json:"service_id"`
	StartsAt    time.Time         `json:"starts_at"`
	EndsAt      time.Time         `json:"ends_at"`
	Status      AppointmentStatus `json:"status"`
	PriceCents  int               `json:"price_cents"`
	Notes       string            `json:"notes"`
	CreatedAt   time.Time         `json:"created_at"`
	ClientName  string            `json:"client_name"`
	ClientEmail string            `json:"client_email,omitempty"`
	ClientPhone string            `json:"client_phone,omitempty"`
	BarberName  string            `json:"barber_name"`
	ServiceName string            `json:"service_name"`
}

type OverrideKind string

const (
	OverrideClosed OverrideKind = "closed" // no se trabaja ese día
	OverrideOpen   OverrideKind = "open"   // horario especial que sustituye al semanal
	OverrideBlock  OverrideKind = "block"  // franja bloqueada dentro del horario
)

// DayOverride es una excepción al horario semanal en una fecha concreta.
// BarberID nil aplica a toda la barbería.
type DayOverride struct {
	ID       uuid.UUID    `json:"id"`
	BarberID *uuid.UUID   `json:"barber_id"`
	Day      string       `json:"day"` // YYYY-MM-DD en la zona horaria de la barbería
	Kind     OverrideKind `json:"kind"`
	StartMin *int         `json:"start_min"`
	EndMin   *int         `json:"end_min"`
	Note     string       `json:"note"`
}

// Interval es un rango semiabierto [Start, End).
type Interval struct {
	Start time.Time
	End   time.Time
}

func (i Interval) Overlaps(o Interval) bool { return i.Start.Before(o.End) && o.Start.Before(i.End) }

var (
	ErrNotFound           = errors.New("not found")
	ErrConflict           = errors.New("conflict")
	ErrEmailTaken         = errors.New("email already registered")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrInvalidToken       = errors.New("invalid or expired token")
	ErrForbidden          = errors.New("forbidden")
	ErrSlotTaken          = errors.New("slot no longer available")
	ErrSlotInvalid        = errors.New("slot is not available")
	ErrTooManyActive      = errors.New("too many upcoming appointments")
	ErrTooLateToCancel    = errors.New("too late to cancel")
	ErrInvalidState       = errors.New("invalid appointment state")
	ErrPhoneRequired      = errors.New("a contact phone is required")
	ErrWeakPassword       = errors.New("password must be 8-72 characters and include a letter and a number")
)

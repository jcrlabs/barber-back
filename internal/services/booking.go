package services

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jcrlabs/barber-back/internal/domain"
	"github.com/jcrlabs/barber-back/internal/repository"
)

type CatalogStore interface {
	GetService(ctx context.Context, id uuid.UUID) (*domain.Service, error)
	GetBarber(ctx context.Context, id uuid.UUID) (*domain.Barber, error)
	ListBarbers(ctx context.Context, includeInactive bool) ([]domain.Barber, error)
}

type AppointmentStore interface {
	Create(ctx context.Context, a *domain.Appointment) error
	Get(ctx context.Context, id uuid.UUID) (*domain.Appointment, error)
	BusyIntervals(ctx context.Context, barberID uuid.UUID, from, to time.Time) ([]domain.Interval, error)
	CountUpcomingForClient(ctx context.Context, clientID uuid.UUID, now time.Time) (int, error)
	SetStatus(ctx context.Context, id uuid.UUID, status domain.AppointmentStatus) error
}

type BookingRules struct {
	Location      *time.Location
	SlotStep      time.Duration
	LeadTime      time.Duration
	WindowDays    int
	CancelWindow  time.Duration
	MaxActiveAppt int
}

type BookingService struct {
	catalog CatalogStore
	appts   AppointmentStore
	rules   BookingRules
	now     func() time.Time
}

func NewBookingService(catalog CatalogStore, appts AppointmentStore, rules BookingRules) *BookingService {
	return &BookingService{catalog: catalog, appts: appts, rules: rules, now: time.Now}
}

func (s *BookingService) Rules() BookingRules { return s.rules }

// Slot es un hueco reservable y los barberos que lo tienen libre.
type Slot struct {
	StartsAt  time.Time   `json:"starts_at"`
	EndsAt    time.Time   `json:"ends_at"`
	BarberIDs []uuid.UUID `json:"barber_ids"`
}

type DaySummary struct {
	Date  string `json:"date"`
	Free  int    `json:"free"`
	Open  bool   `json:"open"`
	Total int    `json:"-"`
}

// ComputeSlots calcula los huecos de `duration` dentro de las franjas de trabajo de un día,
// alineados a `step`, sin solapar con `busy` y nunca antes de `notBefore`.
// Las franjas se interpretan en la zona horaria `loc`, lo que respeta los cambios de horario.
func ComputeSlots(day time.Time, loc *time.Location, hours []domain.WorkingHours, busy []domain.Interval,
	duration, step time.Duration, notBefore time.Time) []domain.Interval {
	y, m, d := day.In(loc).Date()
	weekday := time.Date(y, m, d, 12, 0, 0, 0, loc).Weekday()

	out := []domain.Interval{}
	for _, h := range hours {
		if h.Weekday != int(weekday) {
			continue
		}
		winStart := time.Date(y, m, d, 0, h.StartMin, 0, 0, loc)
		winEnd := time.Date(y, m, d, 0, h.EndMin, 0, 0, loc)
		for t := winStart; !t.Add(duration).After(winEnd); t = t.Add(step) {
			if t.Before(notBefore) {
				continue
			}
			slot := domain.Interval{Start: t, End: t.Add(duration)}
			free := true
			for _, b := range busy {
				if slot.Overlaps(b) {
					free = false
					break
				}
			}
			if free {
				out = append(out, slot)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

// ParseDay interpreta YYYY-MM-DD en la zona horaria de la barbería.
func (s *BookingService) ParseDay(v string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", v, s.rules.Location)
}

func (s *BookingService) dayBounds(day time.Time) (time.Time, time.Time) {
	y, m, d := day.In(s.rules.Location).Date()
	start := time.Date(y, m, d, 0, 0, 0, 0, s.rules.Location)
	return start, start.AddDate(0, 0, 1)
}

func (s *BookingService) inWindow(day time.Time) bool {
	today, _ := s.dayBounds(s.now())
	last := today.AddDate(0, 0, s.rules.WindowDays)
	return !day.Before(today) && day.Before(last)
}

func (s *BookingService) barbersFor(ctx context.Context, barberID *uuid.UUID) ([]domain.Barber, error) {
	if barberID != nil {
		b, err := s.catalog.GetBarber(ctx, *barberID)
		if err != nil {
			return nil, err
		}
		if !b.Active {
			return nil, domain.ErrNotFound
		}
		return []domain.Barber{*b}, nil
	}
	return s.catalog.ListBarbers(ctx, false)
}

func (s *BookingService) activeService(ctx context.Context, id uuid.UUID) (*domain.Service, error) {
	svc, err := s.catalog.GetService(ctx, id)
	if err != nil {
		return nil, err
	}
	if !svc.Active {
		return nil, domain.ErrNotFound
	}
	return svc, nil
}

// Availability devuelve los huecos de un día. Si barberID es nil agrega todos los barberos.
func (s *BookingService) Availability(ctx context.Context, barberID *uuid.UUID, serviceID uuid.UUID, day time.Time) ([]Slot, error) {
	svc, err := s.activeService(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	barbers, err := s.barbersFor(ctx, barberID)
	if err != nil {
		return nil, err
	}
	if !s.inWindow(day) {
		return []Slot{}, nil
	}
	from, to := s.dayBounds(day)
	notBefore := s.now().Add(s.rules.LeadTime)
	duration := time.Duration(svc.DurationMin) * time.Minute

	merged := map[int64]*Slot{}
	for _, b := range barbers {
		busy, err := s.appts.BusyIntervals(ctx, b.ID, from, to)
		if err != nil {
			return nil, err
		}
		for _, iv := range ComputeSlots(day, s.rules.Location, b.Hours, busy, duration, s.rules.SlotStep, notBefore) {
			key := iv.Start.Unix()
			if sl, ok := merged[key]; ok {
				sl.BarberIDs = append(sl.BarberIDs, b.ID)
				continue
			}
			merged[key] = &Slot{StartsAt: iv.Start, EndsAt: iv.End, BarberIDs: []uuid.UUID{b.ID}}
		}
	}
	out := make([]Slot, 0, len(merged))
	for _, sl := range merged {
		out = append(out, *sl)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartsAt.Before(out[j].StartsAt) })
	return out, nil
}

// Days resume cuántos huecos libres hay en cada uno de los próximos `n` días.
func (s *BookingService) Days(ctx context.Context, barberID *uuid.UUID, serviceID uuid.UUID, from time.Time, n int) ([]DaySummary, error) {
	svc, err := s.activeService(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	barbers, err := s.barbersFor(ctx, barberID)
	if err != nil {
		return nil, err
	}
	rangeStart, _ := s.dayBounds(from)
	rangeEnd := rangeStart.AddDate(0, 0, n)
	notBefore := s.now().Add(s.rules.LeadTime)
	duration := time.Duration(svc.DurationMin) * time.Minute

	busyBy := map[uuid.UUID][]domain.Interval{}
	for _, b := range barbers {
		busy, err := s.appts.BusyIntervals(ctx, b.ID, rangeStart, rangeEnd)
		if err != nil {
			return nil, err
		}
		busyBy[b.ID] = busy
	}

	out := make([]DaySummary, 0, n)
	for i := 0; i < n; i++ {
		day := rangeStart.AddDate(0, 0, i)
		sum := DaySummary{Date: day.Format("2006-01-02")}
		if s.inWindow(day) {
			starts := map[int64]struct{}{}
			for _, b := range barbers {
				for _, h := range b.Hours {
					if h.Weekday == int(day.Weekday()) {
						sum.Open = true
					}
				}
				for _, iv := range ComputeSlots(day, s.rules.Location, b.Hours, busyBy[b.ID], duration, s.rules.SlotStep, notBefore) {
					starts[iv.Start.Unix()] = struct{}{}
				}
			}
			sum.Free = len(starts)
		}
		out = append(out, sum)
	}
	return out, nil
}

type BookRequest struct {
	ClientID  uuid.UUID
	BarberID  uuid.UUID
	ServiceID uuid.UUID
	StartsAt  time.Time
	Notes     string
}

func (s *BookingService) Book(ctx context.Context, req BookRequest) (*domain.Appointment, error) {
	svc, err := s.activeService(ctx, req.ServiceID)
	if err != nil {
		return nil, err
	}
	barber, err := s.catalog.GetBarber(ctx, req.BarberID)
	if err != nil {
		return nil, err
	}
	if !barber.Active {
		return nil, domain.ErrNotFound
	}

	now := s.now()
	if s.rules.MaxActiveAppt > 0 {
		n, err := s.appts.CountUpcomingForClient(ctx, req.ClientID, now)
		if err != nil {
			return nil, err
		}
		if n >= s.rules.MaxActiveAppt {
			return nil, domain.ErrTooManyActive
		}
	}

	// Validamos que la hora pedida sea exactamente uno de los huecos calculados:
	// alineada, dentro del horario, respetando antelación mínima y sin solapes.
	day := req.StartsAt.In(s.rules.Location)
	if !s.inWindow(day) {
		return nil, domain.ErrSlotInvalid
	}
	from, to := s.dayBounds(day)
	busy, err := s.appts.BusyIntervals(ctx, barber.ID, from, to)
	if err != nil {
		return nil, err
	}
	duration := time.Duration(svc.DurationMin) * time.Minute
	valid := false
	for _, iv := range ComputeSlots(day, s.rules.Location, barber.Hours, busy, duration, s.rules.SlotStep, now.Add(s.rules.LeadTime)) {
		if iv.Start.Equal(req.StartsAt) {
			valid = true
			break
		}
	}
	if !valid {
		return nil, domain.ErrSlotInvalid
	}

	a := &domain.Appointment{
		ClientID: req.ClientID, BarberID: barber.ID, ServiceID: svc.ID,
		StartsAt: req.StartsAt.UTC(), EndsAt: req.StartsAt.Add(duration).UTC(),
		Status: domain.StatusConfirmed, PriceCents: svc.PriceCents, Notes: strings.TrimSpace(req.Notes),
	}
	// La restricción EXCLUDE de Postgres resuelve la carrera entre dos reservas simultáneas.
	if err := s.appts.Create(ctx, a); err != nil {
		return nil, err
	}
	return s.appts.Get(ctx, a.ID)
}

// CancelByClient permite al cliente anular su cita respetando la ventana de cancelación.
func (s *BookingService) CancelByClient(ctx context.Context, clientID, apptID uuid.UUID) (*domain.Appointment, error) {
	a, err := s.appts.Get(ctx, apptID)
	if err != nil {
		return nil, err
	}
	if a.ClientID != clientID {
		return nil, domain.ErrNotFound
	}
	if a.Status != domain.StatusConfirmed {
		return nil, domain.ErrInvalidState
	}
	if a.StartsAt.Sub(s.now()) < s.rules.CancelWindow {
		return nil, domain.ErrTooLateToCancel
	}
	if err := s.appts.SetStatus(ctx, a.ID, domain.StatusCancelled); err != nil {
		return nil, err
	}
	a.Status = domain.StatusCancelled
	return a, nil
}

// SetStatusByStaff cambia el estado de una cita confirmada. barberID != nil limita al barbero dueño.
func (s *BookingService) SetStatusByStaff(ctx context.Context, apptID uuid.UUID, barberID *uuid.UUID, status domain.AppointmentStatus) (*domain.Appointment, error) {
	if status == domain.StatusConfirmed || !status.Valid() {
		return nil, domain.ErrInvalidState
	}
	a, err := s.appts.Get(ctx, apptID)
	if err != nil {
		return nil, err
	}
	if barberID != nil && a.BarberID != *barberID {
		return nil, domain.ErrNotFound
	}
	if a.Status != domain.StatusConfirmed {
		return nil, domain.ErrInvalidState
	}
	if (status == domain.StatusCompleted || status == domain.StatusNoShow) && s.now().Before(a.StartsAt) {
		return nil, domain.ErrInvalidState
	}
	if err := s.appts.SetStatus(ctx, a.ID, status); err != nil {
		return nil, err
	}
	a.Status = status
	return a, nil
}

// IsBookingError indica si el error es un fallo de negocio esperable (4xx).
func IsBookingError(err error) bool {
	for _, e := range []error{domain.ErrSlotTaken, domain.ErrSlotInvalid, domain.ErrTooManyActive,
		domain.ErrTooLateToCancel, domain.ErrInvalidState} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

var _ AppointmentStore = (*repository.AppointmentRepository)(nil)
var _ CatalogStore = (*repository.CatalogRepository)(nil)
var _ UserStore = (*repository.UserRepository)(nil)
var _ TokenStore = (*repository.TokenRepository)(nil)

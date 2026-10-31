package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jcrlabs/barber-back/internal/domain"
)

var madrid, _ = time.LoadLocation("Europe/Madrid")

func at(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, madrid)
}

func TestComputeSlots(t *testing.T) {
	// 2026-11-03 es martes (weekday 2)
	day := at(2026, 11, 3, 0, 0)
	morning := []domain.WorkingHours{{Weekday: 2, StartMin: 10 * 60, EndMin: 11 * 60}}
	past := at(2000, 1, 1, 0, 0)

	tests := []struct {
		name      string
		day       time.Time
		hours     []domain.WorkingHours
		busy      []domain.Interval
		duration  time.Duration
		notBefore time.Time
		want      []string
	}{
		{"jornada libre", day, morning, nil, 30 * time.Minute, past, []string{"10:00", "10:15", "10:30"}},
		{"el servicio no cabe al final", day, morning, nil, 45 * time.Minute, past, []string{"10:00", "10:15"}},
		{"cita ocupada bloquea solapes", day, morning,
			[]domain.Interval{{Start: at(2026, 11, 3, 10, 15), End: at(2026, 11, 3, 10, 30)}},
			30 * time.Minute, past, []string{"10:30"}},
		{"antelación mínima", day, morning, nil, 30 * time.Minute, at(2026, 11, 3, 10, 10), []string{"10:15", "10:30"}},
		{"día no laborable", at(2026, 11, 4, 0, 0), morning, nil, 30 * time.Minute, past, nil},
		{"dos franjas", day, []domain.WorkingHours{
			{Weekday: 2, StartMin: 10 * 60, EndMin: 10*60 + 30},
			{Weekday: 2, StartMin: 16 * 60, EndMin: 16*60 + 30},
		}, nil, 30 * time.Minute, past, []string{"10:00", "16:00"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeSlots(tc.day, madrid, tc.hours, tc.busy, tc.duration, 15*time.Minute, tc.notBefore)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d slots %v, want %v", len(got), got, tc.want)
			}
			for i, s := range got {
				if s.Start.In(madrid).Format("15:04") != tc.want[i] {
					t.Errorf("slot %d = %s, want %s", i, s.Start.In(madrid).Format("15:04"), tc.want[i])
				}
			}
		})
	}
}

func TestComputeSlotsDST(t *testing.T) {
	// 2026-10-25: cambio de hora en Europa (domingo, 03:00 -> 02:00). Las franjas siguen en hora local.
	hours := []domain.WorkingHours{{Weekday: 0, StartMin: 10 * 60, EndMin: 11 * 60}}
	got := ComputeSlots(at(2026, 10, 25, 0, 0), madrid, hours, nil, time.Hour, 15*time.Minute, time.Time{})
	if len(got) != 1 || got[0].Start.In(madrid).Hour() != 10 {
		t.Fatalf("unexpected DST slots: %v", got)
	}
}

type bookingFixture struct {
	svc      *BookingService
	appts    *fakeAppts
	service  *domain.Service
	barber   *domain.Barber
	client   uuid.UUID
	catalog  *fakeCatalog
	notifier *fakeNotifier
	tuesday  time.Time
	tenAM    time.Time
	nowValue time.Time
}

func newBookingFixture(t *testing.T) *bookingFixture {
	t.Helper()
	service := &domain.Service{ID: uuid.New(), Name: "Corte", DurationMin: 30, PriceCents: 1800, Active: true}
	barber := &domain.Barber{ID: uuid.New(), Name: "Marco", Active: true,
		Hours: []domain.WorkingHours{{Weekday: 2, StartMin: 10 * 60, EndMin: 14 * 60}}}
	appts := &fakeAppts{items: map[uuid.UUID]*domain.Appointment{}}
	catalog := &fakeCatalog{
		services: map[uuid.UUID]*domain.Service{service.ID: service},
		barbers:  map[uuid.UUID]*domain.Barber{barber.ID: barber},
	}
	f := &bookingFixture{
		appts: appts, service: service, barber: barber, client: uuid.New(), catalog: catalog, notifier: &fakeNotifier{},
		tuesday: at(2026, 11, 3, 0, 0), tenAM: at(2026, 11, 3, 10, 0),
		nowValue: at(2026, 11, 1, 12, 0),
	}
	f.svc = NewBookingService(catalog, appts, BookingRules{
		Location: madrid, SlotStep: 15 * time.Minute, LeadTime: time.Hour,
		WindowDays: 30, CancelWindow: 2 * time.Hour, MaxActiveAppt: 2,
	})
	f.svc.WithNotifier(f.notifier)
	f.svc.now = func() time.Time { return f.nowValue }
	return f
}

func (f *bookingFixture) book(start time.Time) (*domain.Appointment, error) {
	return f.svc.Book(context.Background(), BookRequest{ClientID: f.client, BarberID: f.barber.ID, ServiceID: f.service.ID, StartsAt: start, Phone: "+34 600 000 000"})
}

func TestBookAndAvailability(t *testing.T) {
	f := newBookingFixture(t)
	ctx := context.Background()

	slots, err := f.svc.Availability(ctx, nil, f.service.ID, f.tuesday)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 15 { // 10:00..13:30 cada 15 min
		t.Fatalf("got %d slots, want 15", len(slots))
	}

	a, err := f.book(f.tenAM)
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	if a.PriceCents != 1800 || !a.EndsAt.Equal(f.tenAM.Add(30*time.Minute)) {
		t.Fatalf("unexpected appointment: %+v", a)
	}

	slots, _ = f.svc.Availability(ctx, &f.barber.ID, f.service.ID, f.tuesday)
	for _, s := range slots {
		if s.StartsAt.Before(f.tenAM.Add(30 * time.Minute)) {
			t.Fatalf("slot %v overlaps booked appointment", s.StartsAt)
		}
	}
}

func TestBookRejectsInvalidSlots(t *testing.T) {
	tests := []struct {
		name  string
		start func(f *bookingFixture) time.Time
		want  error
	}{
		{"desalineado", func(f *bookingFixture) time.Time { return f.tenAM.Add(7 * time.Minute) }, domain.ErrSlotInvalid},
		{"fuera de horario", func(f *bookingFixture) time.Time { return f.tenAM.Add(-time.Hour) }, domain.ErrSlotInvalid},
		{"en el pasado", func(f *bookingFixture) time.Time { return f.nowValue.Add(-24 * time.Hour) }, domain.ErrSlotInvalid},
		{"fuera de ventana", func(f *bookingFixture) time.Time { return f.tenAM.AddDate(0, 2, 0) }, domain.ErrSlotInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newBookingFixture(t)
			if _, err := f.book(tc.start(f)); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestBookConflictsAndLimits(t *testing.T) {
	f := newBookingFixture(t)
	if _, err := f.book(f.tenAM); err != nil {
		t.Fatal(err)
	}
	if _, err := f.book(f.tenAM); !errors.Is(err, domain.ErrSlotInvalid) {
		t.Fatalf("double booking: got %v", err)
	}
	if _, err := f.book(f.tenAM.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.book(f.tenAM.Add(2 * time.Hour)); !errors.Is(err, domain.ErrTooManyActive) {
		t.Fatalf("limit: got %v", err)
	}
}

func TestCancelAndStaffStatus(t *testing.T) {
	f := newBookingFixture(t)
	ctx := context.Background()
	a, err := f.book(f.tenAM)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.svc.CancelByClient(ctx, uuid.New(), a.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign cancel: got %v", err)
	}
	if _, err := f.svc.SetStatusByStaff(ctx, a.ID, domain.StatusCompleted); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("complete before start: got %v", err)
	}

	f.nowValue = f.tenAM.Add(-time.Hour) // dentro de la ventana de 2h
	if _, err := f.svc.CancelByClient(ctx, f.client, a.ID); !errors.Is(err, domain.ErrTooLateToCancel) {
		t.Fatalf("late cancel: got %v", err)
	}

	f.nowValue = f.tenAM.Add(35 * time.Minute)
	got, err := f.svc.SetStatusByStaff(ctx, a.ID, domain.StatusCompleted)
	if err != nil || got.Status != domain.StatusCompleted {
		t.Fatalf("complete: %v %v", got, err)
	}
	if _, err := f.svc.SetStatusByStaff(ctx, a.ID, domain.StatusNoShow); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("re-transition: got %v", err)
	}
}

func TestDaysSummary(t *testing.T) {
	f := newBookingFixture(t)
	days, err := f.svc.Days(context.Background(), nil, f.service.ID, f.nowValue, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 7 {
		t.Fatalf("got %d days", len(days))
	}
	for _, d := range days {
		isTuesday := d.Date == "2026-11-03"
		if d.Open != isTuesday || (d.Free > 0) != isTuesday {
			t.Errorf("day %s open=%v free=%d", d.Date, d.Open, d.Free)
		}
	}
}

func TestBookRequiresPhoneAndNotifies(t *testing.T) {
	f := newBookingFixture(t)
	_, err := f.svc.Book(context.Background(), BookRequest{ClientID: f.client, BarberID: f.barber.ID, ServiceID: f.service.ID, StartsAt: f.tenAM, Phone: "  "})
	if !errors.Is(err, domain.ErrPhoneRequired) {
		t.Fatalf("no phone: got %v", err)
	}
	a, err := f.book(f.tenAM)
	if err != nil {
		t.Fatal(err)
	}
	if a.ClientPhone != "+34 600 000 000" || len(f.notifier.got) != 1 {
		t.Fatalf("phone=%q notified=%d", a.ClientPhone, len(f.notifier.got))
	}
}

func TestScheduleOverrides(t *testing.T) {
	ptr := func(v int) *int { return &v }
	ctx := context.Background()
	slotsOn := func(f *bookingFixture) int {
		t.Helper()
		slots, err := f.svc.Availability(ctx, nil, f.service.ID, f.tuesday)
		if err != nil {
			t.Fatal(err)
		}
		return len(slots)
	}

	base := newBookingFixture(t)
	normal := slotsOn(base) // 10:00-14:00, servicio de 30 min cada 15 → 15 huecos
	if normal != 15 {
		t.Fatalf("baseline slots = %d", normal)
	}

	tests := []struct {
		name string
		ov   domain.DayOverride
		want int
	}{
		{"barbería cerrada", domain.DayOverride{Day: "2026-11-03", Kind: domain.OverrideClosed}, 0},
		{"otro día no afecta", domain.DayOverride{Day: "2026-11-04", Kind: domain.OverrideClosed}, 15},
		{"bloqueo 11:00-12:00", domain.DayOverride{Day: "2026-11-03", Kind: domain.OverrideBlock, StartMin: ptr(660), EndMin: ptr(720)}, 10},
		{"horario especial 16:00-17:00", domain.DayOverride{Day: "2026-11-03", Kind: domain.OverrideOpen, StartMin: ptr(960), EndMin: ptr(1020)}, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newBookingFixture(t)
			f.catalog.overrides = []domain.DayOverride{tc.ov}
			if got := slotsOn(f); got != tc.want {
				t.Fatalf("got %d slots, want %d", got, tc.want)
			}
		})
	}

	t.Run("cierre de otro barbero no afecta", func(t *testing.T) {
		f := newBookingFixture(t)
		other := uuid.New()
		f.catalog.overrides = []domain.DayOverride{{BarberID: &other, Day: "2026-11-03", Kind: domain.OverrideClosed}}
		if got := slotsOn(f); got != 15 {
			t.Fatalf("got %d", got)
		}
	})

	t.Run("no se puede reservar un día cerrado", func(t *testing.T) {
		f := newBookingFixture(t)
		f.catalog.overrides = []domain.DayOverride{{BarberID: &f.barber.ID, Day: "2026-11-03", Kind: domain.OverrideClosed}}
		if _, err := f.book(f.tenAM); !errors.Is(err, domain.ErrSlotInvalid) {
			t.Fatalf("got %v", err)
		}
	})
}

package services

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jcrlabs/barber-back/internal/domain"
	"github.com/jcrlabs/barber-back/internal/repository"
)

// ─── Usuarios / tokens en memoria ────────────────────────────────────────────

type fakeUsers struct {
	mu         sync.Mutex
	byID       map[uuid.UUID]*domain.User
	identities map[string]uuid.UUID
}

func newFakeUsers() *fakeUsers {
	return &fakeUsers{byID: map[uuid.UUID]*domain.User{}, identities: map[string]uuid.UUID{}}
}

func (f *fakeUsers) Create(_ context.Context, u *domain.User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.byID {
		if x.Email == u.Email {
			return domain.ErrEmailTaken
		}
	}
	u.ID = uuid.New()
	cp := *u
	f.byID[u.ID] = &cp
	return nil
}

func (f *fakeUsers) FindByID(_ context.Context, id uuid.UUID) (*domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	cp := *u
	cp.HasPassword = cp.PasswordHash != nil
	return &cp, nil
}

func (f *fakeUsers) FindByEmail(ctx context.Context, email string) (*domain.User, error) {
	f.mu.Lock()
	var id uuid.UUID
	for _, u := range f.byID {
		if u.Email == email {
			id = u.ID
		}
	}
	f.mu.Unlock()
	if id == uuid.Nil {
		return nil, domain.ErrNotFound
	}
	return f.FindByID(ctx, id)
}

func (f *fakeUsers) FindByIdentity(ctx context.Context, provider, subject string) (*domain.User, error) {
	f.mu.Lock()
	id, ok := f.identities[provider+"|"+subject]
	f.mu.Unlock()
	if !ok {
		return nil, domain.ErrNotFound
	}
	return f.FindByID(ctx, id)
}

func (f *fakeUsers) LinkIdentity(_ context.Context, userID uuid.UUID, provider, subject, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.identities[provider+"|"+subject] = userID
	return nil
}

func (f *fakeUsers) UpdateProfile(_ context.Context, id uuid.UUID, name, phone string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byID[id].Name, f.byID[id].Phone = name, phone
	return nil
}

func (f *fakeUsers) UpdatePassword(_ context.Context, id uuid.UUID, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if hash == "" {
		f.byID[id].PasswordHash = nil
	} else {
		f.byID[id].PasswordHash = &hash
	}
	return nil
}

func (f *fakeUsers) FillMissing(_ context.Context, id uuid.UUID, avatar string, verified bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.byID[id].AvatarURL == "" {
		f.byID[id].AvatarURL = avatar
	}
	f.byID[id].EmailVerified = f.byID[id].EmailVerified || verified
	return nil
}

func (f *fakeUsers) SetRole(_ context.Context, id uuid.UUID, role domain.Role) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byID[id].Role = role
	return nil
}

type fakeTokens struct {
	mu   sync.Mutex
	byID map[uuid.UUID]*repository.RefreshToken
	hash map[string]uuid.UUID
}

func newFakeTokens() *fakeTokens {
	return &fakeTokens{byID: map[uuid.UUID]*repository.RefreshToken{}, hash: map[string]uuid.UUID{}}
}

func (f *fakeTokens) Create(_ context.Context, userID, familyID uuid.UUID, hash []byte, exp time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := uuid.New()
	f.byID[id] = &repository.RefreshToken{ID: id, UserID: userID, FamilyID: familyID, ExpiresAt: exp}
	f.hash[string(hash)] = id
	return nil
}

func (f *fakeTokens) FindByHash(_ context.Context, hash []byte) (*repository.RefreshToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.hash[string(hash)]
	if !ok {
		return nil, domain.ErrNotFound
	}
	cp := *f.byID[id]
	return &cp, nil
}

func (f *fakeTokens) Revoke(_ context.Context, id uuid.UUID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.byID[id]
	if t.RevokedAt != nil {
		return false, nil
	}
	now := time.Now()
	t.RevokedAt = &now
	return true, nil
}

func (f *fakeTokens) RevokeFamily(_ context.Context, family uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	for _, t := range f.byID {
		if t.FamilyID == family && t.RevokedAt == nil {
			t.RevokedAt = &now
		}
	}
	return nil
}

func (f *fakeTokens) RevokeAllForUser(_ context.Context, userID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	for _, t := range f.byID {
		if t.UserID == userID && t.RevokedAt == nil {
			t.RevokedAt = &now
		}
	}
	return nil
}

func (f *fakeTokens) activeFor(userID uuid.UUID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, t := range f.byID {
		if t.UserID == userID && t.RevokedAt == nil {
			n++
		}
	}
	return n
}

// ─── Catálogo / citas en memoria ─────────────────────────────────────────────

type fakeCatalog struct {
	services  map[uuid.UUID]*domain.Service
	barbers   map[uuid.UUID]*domain.Barber
	overrides []domain.DayOverride
}

func (f *fakeCatalog) ListOverrides(_ context.Context, from, to string) ([]domain.DayOverride, error) {
	out := []domain.DayOverride{}
	for _, o := range f.overrides {
		if o.Day >= from && o.Day <= to {
			out = append(out, o)
		}
	}
	return out, nil
}

type fakeNotifier struct{ got []*domain.Appointment }

func (n *fakeNotifier) NewBooking(a *domain.Appointment) { n.got = append(n.got, a) }

func (f *fakeCatalog) GetService(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	if s, ok := f.services[id]; ok {
		return s, nil
	}
	return nil, domain.ErrNotFound
}

func (f *fakeCatalog) GetBarber(_ context.Context, id uuid.UUID) (*domain.Barber, error) {
	if b, ok := f.barbers[id]; ok {
		return b, nil
	}
	return nil, domain.ErrNotFound
}

func (f *fakeCatalog) ListBarbers(_ context.Context, includeInactive bool) ([]domain.Barber, error) {
	out := []domain.Barber{}
	for _, b := range f.barbers {
		if b.Active || includeInactive {
			out = append(out, *b)
		}
	}
	return out, nil
}

type fakeAppts struct {
	mu    sync.Mutex
	items map[uuid.UUID]*domain.Appointment
}

func (f *fakeAppts) Create(_ context.Context, a *domain.Appointment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	iv := domain.Interval{Start: a.StartsAt, End: a.EndsAt}
	for _, x := range f.items {
		if x.BarberID == a.BarberID && x.Status == domain.StatusConfirmed && iv.Overlaps(domain.Interval{Start: x.StartsAt, End: x.EndsAt}) {
			return domain.ErrSlotTaken
		}
	}
	a.ID = uuid.New()
	cp := *a
	f.items[a.ID] = &cp
	return nil
}

func (f *fakeAppts) Get(_ context.Context, id uuid.UUID) (*domain.Appointment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.items[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	cp := *a
	return &cp, nil
}

func (f *fakeAppts) BusyIntervals(_ context.Context, barberID uuid.UUID, from, to time.Time) ([]domain.Interval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.Interval{}
	win := domain.Interval{Start: from, End: to}
	for _, a := range f.items {
		iv := domain.Interval{Start: a.StartsAt, End: a.EndsAt}
		if a.BarberID == barberID && (a.Status == domain.StatusConfirmed || a.Status == domain.StatusCompleted) && iv.Overlaps(win) {
			out = append(out, iv)
		}
	}
	return out, nil
}

func (f *fakeAppts) CountUpcomingForClient(_ context.Context, clientID uuid.UUID, now time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, a := range f.items {
		if a.ClientID == clientID && a.Status == domain.StatusConfirmed && a.StartsAt.After(now) {
			n++
		}
	}
	return n, nil
}

func (f *fakeAppts) SetStatus(_ context.Context, id uuid.UUID, s domain.AppointmentStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[id].Status = s
	return nil
}

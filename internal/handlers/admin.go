package handlers

import (
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jcrlabs/barber-back/internal/domain"
	"github.com/jcrlabs/barber-back/internal/middleware"
	"github.com/jcrlabs/barber-back/internal/repository"
)

type AdminHandler struct {
	catalog *repository.CatalogRepository
	appts   *repository.AppointmentRepository
	users   *repository.UserRepository
	loc     *time.Location
}

func NewAdminHandler(c *repository.CatalogRepository, a *repository.AppointmentRepository, u *repository.UserRepository, loc *time.Location) *AdminHandler {
	return &AdminHandler{catalog: c, appts: a, users: u, loc: loc}
}

// ─── Services ────────────────────────────────────────────────────────────────

type serviceRequest struct {
	Name        string `json:"name" binding:"required,min=2,max=80"`
	Description string `json:"description" binding:"max=500"`
	DurationMin int    `json:"duration_min" binding:"required,min=5,max=480"`
	PriceCents  int    `json:"price_cents" binding:"min=0,max=1000000"`
	Active      *bool  `json:"active" binding:"required"`
	SortOrder   int    `json:"sort_order" binding:"min=0,max=1000"`
}

func (r serviceRequest) toDomain(id uuid.UUID) *domain.Service {
	return &domain.Service{ID: id, Name: r.Name, Description: r.Description, DurationMin: r.DurationMin,
		PriceCents: r.PriceCents, Active: *r.Active, SortOrder: r.SortOrder}
}

func (h *AdminHandler) ListServices(c *gin.Context) {
	list, err := h.catalog.ListServices(c.Request.Context(), true)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, list)
}

func (h *AdminHandler) CreateService(c *gin.Context) {
	var req serviceRequest
	if !bind(c, &req) {
		return
	}
	s := req.toDomain(uuid.Nil)
	if err := h.catalog.CreateService(c.Request.Context(), s); err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, s)
}

func (h *AdminHandler) UpdateService(c *gin.Context) {
	id, ok := paramUUID(c, "id")
	if !ok {
		return
	}
	var req serviceRequest
	if !bind(c, &req) {
		return
	}
	s := req.toDomain(id)
	if err := h.catalog.UpdateService(c.Request.Context(), s); err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, s)
}

func (h *AdminHandler) DeleteService(c *gin.Context) {
	id, ok := paramUUID(c, "id")
	if !ok {
		return
	}
	if err := h.catalog.DeleteService(c.Request.Context(), id); err != nil {
		fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ─── Barbers ─────────────────────────────────────────────────────────────────

type hoursRequest struct {
	Weekday  int `json:"weekday" binding:"min=0,max=6"`
	StartMin int `json:"start_min" binding:"min=0,max=1439"`
	EndMin   int `json:"end_min" binding:"min=1,max=1440"`
}

type barberRequest struct {
	UserID    *uuid.UUID     `json:"user_id"`
	Name      string         `json:"name" binding:"required,min=2,max=80"`
	Title     string         `json:"title" binding:"max=80"`
	Bio       string         `json:"bio" binding:"max=600"`
	AvatarURL string         `json:"avatar_url" binding:"omitempty,url,max=500"`
	Active    *bool          `json:"active" binding:"required"`
	Hours     []hoursRequest `json:"hours" binding:"max=28,dive"`
}

// validHours comprueba que las franjas sean coherentes y no se solapen dentro del mismo día.
func validHours(hours []hoursRequest) bool {
	byDay := map[int][]hoursRequest{}
	for _, h := range hours {
		if h.StartMin >= h.EndMin {
			return false
		}
		byDay[h.Weekday] = append(byDay[h.Weekday], h)
	}
	for _, list := range byDay {
		sort.Slice(list, func(i, j int) bool { return list[i].StartMin < list[j].StartMin })
		for i := 1; i < len(list); i++ {
			if list[i].StartMin < list[i-1].EndMin {
				return false
			}
		}
	}
	return true
}

func (h *AdminHandler) barberFromRequest(c *gin.Context, id uuid.UUID) (*domain.Barber, bool) {
	var req barberRequest
	if !bind(c, &req) {
		return nil, false
	}
	if !safeURL(req.AvatarURL) {
		badRequest(c, "avatar_url must be http(s)")
		return nil, false
	}
	if !validHours(req.Hours) {
		badRequest(c, "working hours overlap or are invalid")
		return nil, false
	}
	if req.UserID != nil {
		u, err := h.users.FindByID(c.Request.Context(), *req.UserID)
		if err != nil {
			fail(c, err)
			return nil, false
		}
		if u.Role == domain.RoleClient {
			if err := h.users.SetRole(c.Request.Context(), u.ID, domain.RoleBarber); err != nil {
				fail(c, err)
				return nil, false
			}
		}
	}
	b := &domain.Barber{ID: id, UserID: req.UserID, Name: req.Name, Title: req.Title, Bio: req.Bio,
		AvatarURL: req.AvatarURL, Active: *req.Active, Hours: make([]domain.WorkingHours, 0, len(req.Hours))}
	for _, hr := range req.Hours {
		b.Hours = append(b.Hours, domain.WorkingHours(hr))
	}
	return b, true
}

func (h *AdminHandler) ListBarbers(c *gin.Context) {
	list, err := h.catalog.ListBarbers(c.Request.Context(), true)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, list)
}

func (h *AdminHandler) CreateBarber(c *gin.Context) {
	b, ok := h.barberFromRequest(c, uuid.Nil)
	if !ok {
		return
	}
	if err := h.catalog.CreateBarber(c.Request.Context(), b); err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, b)
}

func (h *AdminHandler) UpdateBarber(c *gin.Context) {
	id, ok := paramUUID(c, "id")
	if !ok {
		return
	}
	b, ok := h.barberFromRequest(c, id)
	if !ok {
		return
	}
	if err := h.catalog.UpdateBarber(c.Request.Context(), b); err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, b)
}

func (h *AdminHandler) DeleteBarber(c *gin.Context) {
	id, ok := paramUUID(c, "id")
	if !ok {
		return
	}
	if err := h.catalog.DeleteBarber(c.Request.Context(), id); err != nil {
		fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ─── Appointments / users / stats ────────────────────────────────────────────

func (h *AdminHandler) ListAppointments(c *gin.Context) {
	f := repository.AppointmentFilter{}
	f.Limit, f.Offset = pagination(c)
	var ok bool
	if f.BarberID, ok = queryUUID(c, "barber_id"); !ok {
		return
	}
	if s := domain.AppointmentStatus(c.Query("status")); s != "" {
		if !s.Valid() {
			badRequest(c, "invalid status")
			return
		}
		f.Status = s
	}
	for name, dst := range map[string]**time.Time{"from": &f.From, "to": &f.To} {
		if v := c.Query(name); v != "" {
			t, err := time.ParseInLocation("2006-01-02", v, h.loc)
			if err != nil {
				badRequest(c, "invalid "+name+" (YYYY-MM-DD)")
				return
			}
			*dst = &t
		}
	}
	list, total, err := h.appts.List(c.Request.Context(), f)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": list, "total": total})
}

func (h *AdminHandler) ListUsers(c *gin.Context) {
	limit, offset := pagination(c)
	role := domain.Role(c.Query("role"))
	if role != "" && !role.Valid() {
		badRequest(c, "invalid role")
		return
	}
	q := c.Query("q")
	if len(q) > 100 {
		badRequest(c, "query too long")
		return
	}
	list, total, err := h.users.List(c.Request.Context(), q, role, limit, offset)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": list, "total": total})
}

type roleRequest struct {
	Role domain.Role `json:"role" binding:"required,oneof=client barber admin"`
}

func (h *AdminHandler) SetUserRole(c *gin.Context) {
	id, ok := paramUUID(c, "id")
	if !ok {
		return
	}
	var req roleRequest
	if !bind(c, &req) {
		return
	}
	if id == middleware.UserID(c) && req.Role != domain.RoleAdmin {
		badRequest(c, "you cannot remove your own admin role")
		return
	}
	if err := h.users.SetRole(c.Request.Context(), id, req.Role); err != nil {
		fail(c, err)
		return
	}
	u, err := h.users.FindByID(c.Request.Context(), id)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, u)
}

func (h *AdminHandler) Stats(c *gin.Context) {
	now := time.Now().In(h.loc)
	y, m, d := now.Date()
	dayStart := time.Date(y, m, d, 0, 0, 0, 0, h.loc)
	weekday := (int(now.Weekday()) + 6) % 7 // lunes = 0
	weekStart := dayStart.AddDate(0, 0, -weekday)
	monthStart := time.Date(y, m, 1, 0, 0, 0, 0, h.loc)
	s, err := h.appts.Stats(c.Request.Context(), dayStart, dayStart.AddDate(0, 0, 1), weekStart, monthStart, now)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, s)
}

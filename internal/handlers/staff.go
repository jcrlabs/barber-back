package handlers

import (
	"context"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jcrlabs/barber-back/internal/domain"
	"github.com/jcrlabs/barber-back/internal/middleware"
	"github.com/jcrlabs/barber-back/internal/repository"
	"github.com/jcrlabs/barber-back/internal/services"
)

// StaffNotifier es la parte del notificador que usa el panel del barbero.
type StaffNotifier interface {
	Channels() []string
	EmailEnabled() bool
	NtfyTopicURL(topic string) string
	Test(ctx context.Context, barberName string, r *domain.BarberNotify) map[string]string
}

// StaffHandler sirve agenda, avisos y horario. El admin ve toda la barbería;
// un barbero solo su propia agenda y su propio horario.
type StaffHandler struct {
	booking  *services.BookingService
	appts    *repository.AppointmentRepository
	catalog  *repository.CatalogRepository
	notifier StaffNotifier
}

func NewStaffHandler(b *services.BookingService, a *repository.AppointmentRepository, c *repository.CatalogRepository,
	n StaffNotifier) *StaffHandler {
	return &StaffHandler{booking: b, appts: a, catalog: c, notifier: n}
}

// scope devuelve el barbero al que se limita la petición: el propio para un barbero,
// o el filtro opcional `barber_id` para un admin (nil = toda la barbería).
func (h *StaffHandler) scope(c *gin.Context) (*uuid.UUID, bool) {
	if middleware.Role(c) == domain.RoleAdmin {
		return queryUUID(c, "barber_id")
	}
	b, err := h.catalog.GetBarberByUser(c.Request.Context(), middleware.UserID(c))
	if err != nil {
		fail(c, err)
		return nil, false
	}
	return &b.ID, true
}

func (h *StaffHandler) Agenda(c *gin.Context) {
	scope, ok := h.scope(c)
	if !ok {
		return
	}
	loc := h.booking.Rules().Location
	from, err := time.ParseInLocation("2006-01-02", c.DefaultQuery("from", time.Now().In(loc).Format("2006-01-02")), loc)
	if err != nil {
		badRequest(c, "invalid from (YYYY-MM-DD)")
		return
	}
	to := from.AddDate(0, 0, 1)
	if v := c.Query("to"); v != "" {
		if to, err = time.ParseInLocation("2006-01-02", v, loc); err != nil || !to.After(from) || to.Sub(from) > 62*24*time.Hour {
			badRequest(c, "invalid to (YYYY-MM-DD, max 62 days after from)")
			return
		}
	}
	list, _, err := h.appts.List(c.Request.Context(), repository.AppointmentFilter{
		BarberID: scope, From: &from, To: &to, Limit: 500,
	})
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, list)
}

type statusRequest struct {
	Status domain.AppointmentStatus `json:"status" binding:"required,oneof=completed cancelled no_show"`
}

func (h *StaffHandler) SetStatus(c *gin.Context) {
	id, ok := paramUUID(c, "id")
	if !ok {
		return
	}
	var req statusRequest
	if !bind(c, &req) {
		return
	}
	scope, ok := h.scope(c)
	if !ok {
		return
	}
	if middleware.Role(c) == domain.RoleAdmin {
		scope = nil // el admin gestiona cualquier cita
	}
	a, err := h.booking.SetStatusByStaff(c.Request.Context(), id, req.Status, scope)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, a)
}

// ─── Avisos de reservas nuevas ───────────────────────────────────────────────

// Notifications devuelve las citas reservadas después de `since` (RFC 3339), como mucho de los
// últimos 7 días. El panel lo consulta periódicamente para avisar de reservas nuevas.
func (h *StaffHandler) Notifications(c *gin.Context) {
	scope, ok := h.scope(c)
	if !ok {
		return
	}
	now := time.Now()
	since := now.Add(-24 * time.Hour)
	if v := c.Query("since"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			badRequest(c, "invalid since (RFC 3339)")
			return
		}
		since = t
	}
	if floor := now.Add(-7 * 24 * time.Hour); since.Before(floor) {
		since = floor
	}
	list, _, err := h.appts.List(c.Request.Context(), repository.AppointmentFilter{
		BarberID: scope, Status: domain.StatusConfirmed, CreatedAfter: &since, NewestFirst: true, Limit: 50,
	})
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": list, "server_time": now.UTC(), "channels": h.notifier.Channels()})
}

// ─── Excepciones de horario ──────────────────────────────────────────────────

type overrideRequest struct {
	BarberID *uuid.UUID          `json:"barber_id"`
	Day      string              `json:"day" binding:"required,datetime=2006-01-02"`
	Kind     domain.OverrideKind `json:"kind" binding:"required,oneof=closed open block"`
	StartMin *int                `json:"start_min" binding:"omitempty,min=0,max=1439"`
	EndMin   *int                `json:"end_min" binding:"omitempty,min=1,max=1440"`
	Note     string              `json:"note" binding:"max=200"`
}

// ListOverrides devuelve las excepciones del rango. Un barbero ve las suyas y los cierres de toda la barbería.
func (h *StaffHandler) ListOverrides(c *gin.Context) {
	scope, ok := h.scope(c)
	if !ok {
		return
	}
	loc := h.booking.Rules().Location
	today := time.Now().In(loc)
	from := c.DefaultQuery("from", today.Format("2006-01-02"))
	to := c.DefaultQuery("to", today.AddDate(0, 0, 90).Format("2006-01-02"))
	f, err1 := time.Parse("2006-01-02", from)
	t, err2 := time.Parse("2006-01-02", to)
	if err1 != nil || err2 != nil || t.Before(f) || t.Sub(f) > 400*24*time.Hour {
		badRequest(c, "invalid from/to (YYYY-MM-DD, max 400 days)")
		return
	}
	list, err := h.catalog.ListOverrides(c.Request.Context(), from, to)
	if err != nil {
		fail(c, err)
		return
	}
	if scope != nil {
		mine := make([]domain.DayOverride, 0, len(list))
		for _, o := range list {
			if o.BarberID == nil || *o.BarberID == *scope {
				mine = append(mine, o)
			}
		}
		list = mine
	}
	c.JSON(http.StatusOK, list)
}

func (h *StaffHandler) CreateOverride(c *gin.Context) {
	var req overrideRequest
	if !bind(c, &req) {
		return
	}
	if req.Kind == domain.OverrideClosed {
		req.StartMin, req.EndMin = nil, nil
	} else if req.StartMin == nil || req.EndMin == nil || *req.StartMin >= *req.EndMin {
		badRequest(c, "start_min and end_min are required and start must be before end")
		return
	}
	if middleware.Role(c) != domain.RoleAdmin {
		// Un barbero solo crea excepciones sobre su propio horario.
		b, err := h.catalog.GetBarberByUser(c.Request.Context(), middleware.UserID(c))
		if err != nil {
			fail(c, err)
			return
		}
		req.BarberID = &b.ID
	} else if req.BarberID != nil {
		if _, err := h.catalog.GetBarber(c.Request.Context(), *req.BarberID); err != nil {
			fail(c, err)
			return
		}
	}
	o := &domain.DayOverride{BarberID: req.BarberID, Day: req.Day, Kind: req.Kind,
		StartMin: req.StartMin, EndMin: req.EndMin, Note: req.Note}
	if err := h.catalog.CreateOverride(c.Request.Context(), o); err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, o)
}

func (h *StaffHandler) DeleteOverride(c *gin.Context) {
	id, ok := paramUUID(c, "id")
	if !ok {
		return
	}
	var scope *uuid.UUID
	if middleware.Role(c) != domain.RoleAdmin {
		var ok bool
		if scope, ok = h.scope(c); !ok {
			return
		}
	}
	if err := h.catalog.DeleteOverride(c.Request.Context(), id, scope); err != nil {
		fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ─── Mi perfil de barbero ────────────────────────────────────────────────────

type notifySettings struct {
	*domain.BarberNotify
	NtfyURL      string `json:"ntfy_url"`
	EmailEnabled bool   `json:"email_enabled"`
}

func (h *StaffHandler) me(c *gin.Context) (*domain.Barber, bool) {
	b, err := h.catalog.GetBarberByUser(c.Request.Context(), middleware.UserID(c))
	if err != nil {
		fail(c, err)
		return nil, false
	}
	return b, true
}

func (h *StaffHandler) settings(n *domain.BarberNotify) notifySettings {
	return notifySettings{BarberNotify: n, NtfyURL: h.notifier.NtfyTopicURL(n.NtfyTopic), EmailEnabled: h.notifier.EmailEnabled()}
}

// Me devuelve el barbero enlazado a la cuenta y sus canales de aviso.
func (h *StaffHandler) Me(c *gin.Context) {
	b, ok := h.me(c)
	if !ok {
		return
	}
	n, err := h.catalog.GetNotify(c.Request.Context(), b.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"barber": b, "notify": h.settings(n)})
}

type myHoursRequest struct {
	Hours []hoursRequest `json:"hours" binding:"max=28,dive"`
}

func (h *StaffHandler) UpdateMyHours(c *gin.Context) {
	b, ok := h.me(c)
	if !ok {
		return
	}
	var req myHoursRequest
	if !bind(c, &req) {
		return
	}
	if !validHours(req.Hours) {
		badRequest(c, "working hours overlap or are invalid")
		return
	}
	hours := make([]domain.WorkingHours, 0, len(req.Hours))
	for _, hr := range req.Hours {
		hours = append(hours, domain.WorkingHours(hr))
	}
	if err := h.catalog.ReplaceHours(c.Request.Context(), b.ID, hours); err != nil {
		fail(c, err)
		return
	}
	b.Hours = hours
	c.JSON(http.StatusOK, b)
}

type notifyRequest struct {
	Email         string `json:"email" binding:"omitempty,email,max=254"`
	WhatsAppPhone string `json:"whatsapp_phone" binding:"omitempty,phone"`
	CallMeBotKey  string `json:"callmebot_key" binding:"omitempty,alphanum,max=40"`
}

func (h *StaffHandler) UpdateMyNotify(c *gin.Context) {
	b, ok := h.me(c)
	if !ok {
		return
	}
	var req notifyRequest
	if !bind(c, &req) {
		return
	}
	if req.Email != "" {
		if _, err := mail.ParseAddress(req.Email); err != nil {
			badRequest(c, "invalid email")
			return
		}
	}
	n, err := h.catalog.GetNotify(c.Request.Context(), b.ID)
	if err != nil {
		fail(c, err)
		return
	}
	n.Email = strings.TrimSpace(req.Email)
	n.WhatsAppPhone = strings.TrimSpace(req.WhatsAppPhone)
	n.CallMeBotKey = strings.TrimSpace(req.CallMeBotKey)
	if err := h.catalog.UpdateNotify(c.Request.Context(), b.ID, n); err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, h.settings(n))
}

// TestMyNotify envía un aviso de prueba por los canales del barbero y devuelve el resultado por canal.
func (h *StaffHandler) TestMyNotify(c *gin.Context) {
	b, ok := h.me(c)
	if !ok {
		return
	}
	n, err := h.catalog.GetNotify(c.Request.Context(), b.ID)
	if err != nil {
		fail(c, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	c.JSON(http.StatusOK, gin.H{"results": h.notifier.Test(ctx, b.Name, n)})
}

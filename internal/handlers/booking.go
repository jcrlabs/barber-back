package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jcrlabs/barber-back/internal/domain"
	"github.com/jcrlabs/barber-back/internal/middleware"
	"github.com/jcrlabs/barber-back/internal/repository"
	"github.com/jcrlabs/barber-back/internal/services"
)

type BookingHandler struct {
	booking *services.BookingService
	catalog *repository.CatalogRepository
	appts   *repository.AppointmentRepository
}

func NewBookingHandler(b *services.BookingService, c *repository.CatalogRepository, a *repository.AppointmentRepository) *BookingHandler {
	return &BookingHandler{booking: b, catalog: c, appts: a}
}

// Meta expone las reglas públicas de reserva para que el front las muestre.
func (h *BookingHandler) Meta(c *gin.Context) {
	r := h.booking.Rules()
	c.JSON(http.StatusOK, gin.H{
		"timezone":           r.Location.String(),
		"slot_step_min":      int(r.SlotStep.Minutes()),
		"lead_time_min":      int(r.LeadTime.Minutes()),
		"booking_window_day": r.WindowDays,
		"cancel_window_min":  int(r.CancelWindow.Minutes()),
		"max_active":         r.MaxActiveAppt,
	})
}

func (h *BookingHandler) Services(c *gin.Context) {
	list, err := h.catalog.ListServices(c.Request.Context(), false)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, list)
}

type publicBarber struct {
	ID        uuid.UUID             `json:"id"`
	Name      string                `json:"name"`
	Title     string                `json:"title"`
	Bio       string                `json:"bio"`
	AvatarURL string                `json:"avatar_url"`
	Hours     []domain.WorkingHours `json:"hours"`
}

func (h *BookingHandler) Barbers(c *gin.Context) {
	list, err := h.catalog.ListBarbers(c.Request.Context(), false)
	if err != nil {
		fail(c, err)
		return
	}
	out := make([]publicBarber, 0, len(list))
	for _, b := range list {
		out = append(out, publicBarber{ID: b.ID, Name: b.Name, Title: b.Title, Bio: b.Bio, AvatarURL: b.AvatarURL, Hours: b.Hours})
	}
	c.JSON(http.StatusOK, out)
}

func (h *BookingHandler) availabilityParams(c *gin.Context) (*uuid.UUID, uuid.UUID, bool) {
	serviceID, err := uuid.Parse(c.Query("service_id"))
	if err != nil {
		badRequest(c, "invalid service_id")
		return nil, uuid.Nil, false
	}
	barberID, ok := queryUUID(c, "barber_id")
	return barberID, serviceID, ok
}

func (h *BookingHandler) Availability(c *gin.Context) {
	barberID, serviceID, ok := h.availabilityParams(c)
	if !ok {
		return
	}
	day, err := h.booking.ParseDay(c.Query("date"))
	if err != nil {
		badRequest(c, "invalid date (YYYY-MM-DD)")
		return
	}
	slots, err := h.booking.Availability(c.Request.Context(), barberID, serviceID, day)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"date": c.Query("date"), "slots": slots})
}

func (h *BookingHandler) Days(c *gin.Context) {
	barberID, serviceID, ok := h.availabilityParams(c)
	if !ok {
		return
	}
	from := time.Now()
	if v := c.Query("from"); v != "" {
		d, err := h.booking.ParseDay(v)
		if err != nil {
			badRequest(c, "invalid from (YYYY-MM-DD)")
			return
		}
		from = d
	}
	n, _ := strconv.Atoi(c.DefaultQuery("days", "14"))
	if n < 1 || n > 31 {
		n = 14
	}
	days, err := h.booking.Days(c.Request.Context(), barberID, serviceID, from, n)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, days)
}

type bookRequest struct {
	ServiceID uuid.UUID `json:"service_id" binding:"required"`
	BarberID  uuid.UUID `json:"barber_id" binding:"required"`
	StartsAt  time.Time `json:"starts_at" binding:"required"`
	Notes     string    `json:"notes" binding:"max=500"`
}

func (h *BookingHandler) Book(c *gin.Context) {
	var req bookRequest
	if !bind(c, &req) {
		return
	}
	a, err := h.booking.Book(c.Request.Context(), services.BookRequest{
		ClientID: middleware.UserID(c), BarberID: req.BarberID, ServiceID: req.ServiceID,
		StartsAt: req.StartsAt, Notes: req.Notes,
	})
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, a)
}

func (h *BookingHandler) Mine(c *gin.Context) {
	list, err := h.appts.ListForClient(c.Request.Context(), middleware.UserID(c))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, list)
}

func (h *BookingHandler) Cancel(c *gin.Context) {
	id, ok := paramUUID(c, "id")
	if !ok {
		return
	}
	a, err := h.booking.CancelByClient(c.Request.Context(), middleware.UserID(c), id)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, a)
}

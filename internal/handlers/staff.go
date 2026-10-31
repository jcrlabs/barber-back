package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jcrlabs/barber-back/internal/domain"
	"github.com/jcrlabs/barber-back/internal/repository"
	"github.com/jcrlabs/barber-back/internal/services"
)

// StaffHandler sirve la agenda del día a los administradores.
type StaffHandler struct {
	booking *services.BookingService
	appts   *repository.AppointmentRepository
}

func NewStaffHandler(b *services.BookingService, a *repository.AppointmentRepository) *StaffHandler {
	return &StaffHandler{booking: b, appts: a}
}

func (h *StaffHandler) Agenda(c *gin.Context) {
	scope, ok := queryUUID(c, "barber_id")
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
	a, err := h.booking.SetStatusByStaff(c.Request.Context(), id, req.Status)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, a)
}

package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"github.com/jcrlabs/barber-back/internal/domain"
)

type apiError struct {
	status int
	code   string
}

var errorMap = map[error]apiError{
	domain.ErrNotFound:           {http.StatusNotFound, "not_found"},
	domain.ErrConflict:           {http.StatusConflict, "conflict"},
	domain.ErrEmailTaken:         {http.StatusConflict, "email_taken"},
	domain.ErrInvalidCredentials: {http.StatusUnauthorized, "invalid_credentials"},
	domain.ErrInvalidToken:       {http.StatusUnauthorized, "invalid_token"},
	domain.ErrForbidden:          {http.StatusForbidden, "forbidden"},
	domain.ErrSlotTaken:          {http.StatusConflict, "slot_taken"},
	domain.ErrSlotInvalid:        {http.StatusUnprocessableEntity, "slot_invalid"},
	domain.ErrTooManyActive:      {http.StatusUnprocessableEntity, "too_many_active"},
	domain.ErrTooLateToCancel:    {http.StatusUnprocessableEntity, "too_late_to_cancel"},
	domain.ErrInvalidState:       {http.StatusUnprocessableEntity, "invalid_state"},
	domain.ErrWeakPassword:       {http.StatusBadRequest, "weak_password"},
}

// fail traduce errores de dominio a HTTP. Los errores desconocidos nunca se filtran al cliente.
func fail(c *gin.Context, err error) {
	for target, e := range errorMap {
		if errors.Is(err, target) {
			c.AbortWithStatusJSON(e.status, gin.H{"error": target.Error(), "code": e.code})
			return
		}
	}
	slog.Error("internal error",
		slog.String("request_id", c.GetString("request_id")),
		slog.String("route", c.FullPath()),
		slog.String("error", err.Error()))
	c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal server error", "code": "internal"})
}

func badRequest(c *gin.Context, msg string) {
	c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": msg, "code": "bad_request"})
}

// bind valida el body JSON y devuelve los campos erróneos para que el front los pinte.
func bind(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		var ve validator.ValidationErrors
		if errors.As(err, &ve) {
			fields := map[string]string{}
			for _, fe := range ve {
				fields[toSnake(fe.Field())] = fe.Tag()
			}
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "validation failed", "code": "validation", "fields": fields})
			return false
		}
		badRequest(c, "invalid JSON body")
		return false
	}
	return true
}

func paramUUID(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		badRequest(c, "invalid "+name)
		return uuid.Nil, false
	}
	return id, true
}

func queryUUID(c *gin.Context, name string) (*uuid.UUID, bool) {
	v := c.Query(name)
	if v == "" {
		return nil, true
	}
	id, err := uuid.Parse(v)
	if err != nil {
		badRequest(c, "invalid "+name)
		return nil, false
	}
	return &id, true
}

func pagination(c *gin.Context) (limit, offset int) {
	limit, _ = strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ = strconv.Atoi(c.DefaultQuery("offset", "0"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func toSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// safeURL acepta solo URLs http(s) para avatares (evita esquemas como javascript:).
func safeURL(u string) bool {
	return u == "" || strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")
}

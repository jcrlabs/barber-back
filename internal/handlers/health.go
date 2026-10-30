package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

type HealthHandler struct{ db *pgxpool.Pool }

func NewHealthHandler(db *pgxpool.Pool) *HealthHandler { return &HealthHandler{db: db} }

// Health es la readiness probe: comprueba la base de datos.
func (h *HealthHandler) Health(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	if err := h.db.Ping(ctx); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "database": "down"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "database": "up"})
}

// Live es la liveness probe: solo indica que el proceso responde.
func (h *HealthHandler) Live(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) }

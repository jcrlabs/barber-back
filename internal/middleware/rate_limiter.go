package middleware

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// slidingWindow registra los instantes de las peticiones dentro de la ventana.
type slidingWindow struct {
	mu       sync.Mutex
	attempts []time.Time
}

func (sw *slidingWindow) allow(now time.Time, maxHits int, window time.Duration) (bool, time.Duration) {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	cutoff := now.Add(-window)
	valid := sw.attempts[:0]
	for _, t := range sw.attempts {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	sw.attempts = valid

	if len(sw.attempts) >= maxHits {
		return false, sw.attempts[0].Add(window).Sub(now)
	}
	sw.attempts = append(sw.attempts, now)
	return true, 0
}

func (sw *slidingWindow) idle(now time.Time, window time.Duration) bool {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return len(sw.attempts) == 0 || !sw.attempts[len(sw.attempts)-1].After(now.Add(-window))
}

// RateLimiter limita peticiones por IP con ventana deslizante.
type RateLimiter struct {
	mu      sync.Mutex
	entries map[string]*slidingWindow
	maxHits int
	window  time.Duration
	now     func() time.Time
}

func NewRateLimiter(maxHits int, window time.Duration) *RateLimiter {
	l := &RateLimiter{entries: map[string]*slidingWindow{}, maxHits: maxHits, window: window, now: time.Now}
	go func() {
		ticker := time.NewTicker(window)
		defer ticker.Stop()
		for range ticker.C {
			l.cleanup()
		}
	}()
	return l
}

func (l *RateLimiter) cleanup() {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, sw := range l.entries {
		if sw.idle(now, l.window) {
			delete(l.entries, k)
		}
	}
}

func (l *RateLimiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	sw, ok := l.entries[key]
	if !ok {
		sw = &slidingWindow{}
		l.entries[key] = sw
	}
	l.mu.Unlock()
	return sw.allow(l.now(), l.maxHits, l.window)
}

// Middleware devuelve 429 con Retry-After cuando la IP supera el límite.
func (l *RateLimiter) Middleware(message string) gin.HandlerFunc {
	return func(c *gin.Context) {
		ok, retry := l.Allow(c.ClientIP())
		if !ok {
			c.Header("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": message})
			return
		}
		c.Next()
	}
}

// RateLimit es un atajo para crear un limitador y su middleware.
func RateLimit(maxHits int, window time.Duration, message string) gin.HandlerFunc {
	return NewRateLimiter(maxHits, window).Middleware(message)
}

package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
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

// blocked dice si la clave ya agotó su cupo, sin registrar un intento nuevo.
func (sw *slidingWindow) blocked(now time.Time, maxHits int, window time.Duration) (bool, time.Duration) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	cutoff := now.Add(-window)
	n := 0
	var oldest time.Time
	for _, t := range sw.attempts {
		if t.After(cutoff) {
			if n == 0 {
				oldest = t
			}
			n++
		}
	}
	if n >= maxHits {
		return true, oldest.Add(window).Sub(now)
	}
	return false, 0
}

func (sw *slidingWindow) idle(now time.Time, window time.Duration) bool {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return len(sw.attempts) == 0 || !sw.attempts[len(sw.attempts)-1].After(now.Add(-window))
}

// HitStore es un almacén de intentos compartido entre réplicas (Postgres).
type HitStore interface {
	Hit(ctx context.Context, bucket, key string, maxHits int, window time.Duration) (bool, time.Duration, error)
	Blocked(ctx context.Context, bucket, key string, maxHits int, window time.Duration) (bool, time.Duration, error)
	Reset(ctx context.Context, bucket, key string) error
}

// RateLimiter limita peticiones con ventana deslizante. En memoria por defecto; con store,
// el cupo es común a todas las réplicas (si el store falla, seguimos con la memoria local).
type RateLimiter struct {
	mu      sync.Mutex
	entries map[string]*slidingWindow
	maxHits int
	window  time.Duration
	now     func() time.Time
	store   HitStore
	bucket  string
}

// NewSharedLimiter crea un limitador respaldado por un store compartido.
func NewSharedLimiter(store HitStore, bucket string, maxHits int, window time.Duration) *RateLimiter {
	l := NewRateLimiter(maxHits, window)
	l.store, l.bucket = store, bucket
	return l
}

func storeCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*time.Second)
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

func (l *RateLimiter) entry(key string) *slidingWindow {
	l.mu.Lock()
	defer l.mu.Unlock()
	sw, ok := l.entries[key]
	if !ok {
		sw = &slidingWindow{}
		l.entries[key] = sw
	}
	return sw
}

func (l *RateLimiter) Allow(key string) (bool, time.Duration) {
	if l.store != nil {
		ctx, cancel := storeCtx()
		defer cancel()
		ok, retry, err := l.store.Hit(ctx, l.bucket, key, l.maxHits, l.window)
		if err == nil {
			return ok, retry
		}
		slog.Warn("shared rate limit unavailable, using local memory", "bucket", l.bucket, "err", err)
	}
	return l.entry(key).allow(l.now(), l.maxHits, l.window)
}

// Blocked consulta si la clave está bloqueada sin consumir cupo.
func (l *RateLimiter) Blocked(key string) (bool, time.Duration) {
	if l.store != nil {
		ctx, cancel := storeCtx()
		defer cancel()
		if b, retry, err := l.store.Blocked(ctx, l.bucket, key, l.maxHits, l.window); err == nil {
			return b, retry
		}
	}
	return l.entry(key).blocked(l.now(), l.maxHits, l.window)
}

// Reset olvida los intentos de la clave (p.ej. tras un login correcto).
func (l *RateLimiter) Reset(key string) {
	if l.store != nil {
		ctx, cancel := storeCtx()
		defer cancel()
		_ = l.store.Reset(ctx, l.bucket, key)
	}
	l.mu.Lock()
	delete(l.entries, key)
	l.mu.Unlock()
}

// TooMany responde 429 con Retry-After.
func TooMany(c *gin.Context, retry time.Duration, message string) {
	c.Header("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
	c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": message, "code": "rate_limited"})
}

func (l *RateLimiter) limit(c *gin.Context, key, message string) {
	if ok, retry := l.Allow(key); !ok {
		TooMany(c, retry, message)
		return
	}
	c.Next()
}

// Middleware devuelve 429 con Retry-After cuando la IP supera el límite.
func (l *RateLimiter) Middleware(message string) gin.HandlerFunc {
	return func(c *gin.Context) { l.limit(c, "ip:"+c.ClientIP(), message) }
}

// RateLimitUser limita por usuario autenticado (va después de AuthRequired); sin usuario, por IP.
// Así un usuario no puede saltarse el límite cambiando de IP, ni varios usuarios tras la misma IP se bloquean entre sí.
func RateLimitUser(maxHits int, window time.Duration, message string) gin.HandlerFunc {
	l := NewRateLimiter(maxHits, window)
	return func(c *gin.Context) {
		key := "ip:" + c.ClientIP()
		if id := UserID(c); id != uuid.Nil {
			key = "u:" + id.String()
		}
		l.limit(c, key, message)
	}
}

// SharedRateLimit limita por IP con un cupo común a todas las réplicas.
func SharedRateLimit(store HitStore, bucket string, maxHits int, window time.Duration, message string) gin.HandlerFunc {
	return NewSharedLimiter(store, bucket, maxHits, window).Middleware(message)
}

// RateLimit es un atajo para crear un limitador y su middleware.
func RateLimit(maxHits int, window time.Duration, message string) gin.HandlerFunc {
	return NewRateLimiter(maxHits, window).Middleware(message)
}

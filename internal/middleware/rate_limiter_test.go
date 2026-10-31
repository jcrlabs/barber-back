package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestRateLimiterSlidingWindow(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := &RateLimiter{entries: map[string]*slidingWindow{}, maxHits: 3, window: time.Minute, now: func() time.Time { return now }}

	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("1.1.1.1"); !ok {
			t.Fatalf("hit %d blocked", i)
		}
	}
	ok, retry := l.Allow("1.1.1.1")
	if ok || retry <= 0 {
		t.Fatalf("4th hit allowed=%v retry=%v", ok, retry)
	}
	if ok, _ := l.Allow("2.2.2.2"); !ok {
		t.Fatal("other IP blocked")
	}

	now = now.Add(61 * time.Second)
	if ok, _ := l.Allow("1.1.1.1"); !ok {
		t.Fatal("window did not slide")
	}
	l.cleanup()
	if _, ok := l.entries["2.2.2.2"]; ok {
		t.Fatal("idle entry not cleaned")
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/login", RateLimit(2, time.Minute, "slow down"), func(c *gin.Context) { c.Status(http.StatusOK) })

	codes := []int{}
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/login", nil))
		codes = append(codes, w.Code)
		if w.Code == http.StatusTooManyRequests && w.Header().Get("Retry-After") == "" {
			t.Fatal("missing Retry-After")
		}
	}
	if codes[0] != 200 || codes[1] != 200 || codes[2] != 429 {
		t.Fatalf("codes = %v", codes)
	}
}

func TestCSRFGuard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/refresh", CSRFGuard([]string{"https://barber.jcrlabs.net"}), func(c *gin.Context) { c.Status(http.StatusOK) })

	tests := []struct {
		name, header, origin string
		want                 int
	}{
		{"sin cabecera", "", "", http.StatusForbidden},
		{"origen ajeno", "navaja", "https://evil.example", http.StatusForbidden},
		{"mismo origen", "navaja", "https://barber.jcrlabs.net", http.StatusOK},
		{"sin origin", "navaja", "", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/refresh", nil)
			if tc.header != "" {
				req.Header.Set("X-Requested-With", tc.header)
			}
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("got %d, want %d", w.Code, tc.want)
			}
		})
	}
}

func TestBlockedAndReset(t *testing.T) {
	l := &RateLimiter{entries: map[string]*slidingWindow{}, maxHits: 2, window: time.Minute, now: time.Now}
	if b, _ := l.Blocked("acct:admin"); b {
		t.Fatal("blocked before any failure")
	}
	l.Allow("acct:admin")
	l.Allow("acct:admin")
	if b, retry := l.Blocked("acct:admin"); !b || retry <= 0 {
		t.Fatalf("not blocked after max failures (retry %v)", retry)
	}
	// Consultar no consume cupo ni alarga el bloqueo.
	if b, _ := l.Blocked("acct:admin"); !b {
		t.Fatal("peek changed state")
	}
	l.Reset("acct:admin")
	if b, _ := l.Blocked("acct:admin"); b {
		t.Fatal("still blocked after reset")
	}
}

func TestRateLimitUserKeysByUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if u := c.GetHeader("X-User"); u != "" {
			c.Set(ctxUserID, uuid.MustParse(u))
		}
	}, RateLimitUser(2, time.Minute, "slow down"))
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	do := func(user string) int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "9.9.9.9:1234" // misma IP para todos
		if user != "" {
			req.Header.Set("X-User", user)
		}
		r.ServeHTTP(w, req)
		return w.Code
	}
	a, b := uuid.NewString(), uuid.NewString()
	got := []int{do(a), do(a), do(a)}
	if got[0] != 200 || got[1] != 200 || got[2] != 429 {
		t.Fatalf("user a: got %v, want [200 200 429]", got)
	}
	if do(b) != 200 {
		t.Fatal("user b blocked by user a sharing the IP")
	}
}

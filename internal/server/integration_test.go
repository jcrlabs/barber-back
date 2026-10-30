package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jcrlabs/barber-back/internal/config"
	"github.com/jcrlabs/barber-back/internal/database"
	"github.com/jcrlabs/barber-back/internal/repository"
)

// Tests de integración end-to-end contra Postgres real.
// Se ejecutan solo si TEST_DATABASE_URL está definida (CI levanta un servicio postgres).

var ipSeq = 0

type client struct {
	ip     string
	t      *testing.T
	h      http.Handler
	token  string
	cookie *http.Cookie
}

func (c *client) do(method, path string, body any) (int, map[string]any, []any) {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if c.ip == "" {
		ipSeq++
		c.ip = "203.0.113." + strconv.Itoa(ipSeq)
	}
	req.RemoteAddr = c.ip + ":40000"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "navaja")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	w := httptest.NewRecorder()
	c.h.ServeHTTP(w, req)
	for _, ck := range w.Result().Cookies() {
		if ck.Name == "navaja_rt" {
			c.cookie = ck
		}
	}
	var obj map[string]any
	var arr []any
	raw := w.Body.Bytes()
	if len(raw) > 0 && raw[0] == '[' {
		_ = json.Unmarshal(raw, &arr)
	} else {
		_ = json.Unmarshal(raw, &obj)
	}
	if tok, ok := obj["access_token"].(string); ok {
		c.token = tok
	}
	return w.Code, obj, arr
}

func setup(t *testing.T) (http.Handler, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	db, err := database.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	// Idempotencia: una segunda ejecución no debe fallar.
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("BOOKING_LEAD_MIN", "0")
	t.Setenv("MAX_ACTIVE_APPOINTMENTS", "5")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(cfg, db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return app.Router, db
}

func TestEndToEndBookingFlow(t *testing.T) {
	h, db := setup(t)
	ctx := context.Background()

	if err := SeedDemo(ctx, repository.NewCatalogRepository(db)); err != nil {
		t.Fatal(err)
	}

	anon := &client{t: t, h: h}
	code, _, services := anon.do("GET", "/api/v1/services", nil)
	if code != 200 || len(services) != 5 {
		t.Fatalf("services: %d %v", code, services)
	}
	serviceID := services[0].(map[string]any)["id"].(string)

	code, _, barbers := anon.do("GET", "/api/v1/barbers", nil)
	if code != 200 || len(barbers) != 3 {
		t.Fatalf("barbers: %d", code)
	}

	// Buscamos el primer día con huecos para "cualquier barbero".
	code, _, days := anon.do("GET", "/api/v1/availability/days?days=14&service_id="+serviceID, nil)
	if code != 200 {
		t.Fatalf("days: %d", code)
	}
	var date string
	for _, d := range days {
		if dm := d.(map[string]any); dm["free"].(float64) > 0 {
			date = dm["date"].(string)
			break
		}
	}
	if date == "" {
		t.Fatal("no available day in two weeks")
	}
	code, avail, _ := anon.do("GET", "/api/v1/availability?service_id="+serviceID+"&date="+date, nil)
	slots := avail["slots"].([]any)
	if code != 200 || len(slots) == 0 {
		t.Fatalf("availability: %d %v", code, avail)
	}
	slot := slots[len(slots)/2].(map[string]any)
	barberID := slot["barber_ids"].([]any)[0].(string)
	startsAt := slot["starts_at"].(string)

	// Reservar exige sesión.
	if code, _, _ := anon.do("POST", "/api/v1/appointments", map[string]any{}); code != 401 {
		t.Fatalf("anon booking: %d", code)
	}

	// Registro: validación, campos extra y alta correcta.
	ana := &client{t: t, h: h}
	if code, body, _ := ana.do("POST", "/api/v1/auth/register", map[string]any{"name": "A", "email": "nope", "password": "x"}); code != 400 || body["code"] != "validation" {
		t.Fatalf("validation: %d %v", code, body)
	}
	if code, _, _ := ana.do("POST", "/api/v1/auth/register", map[string]any{"name": "Ana", "email": "ana@test.es", "password": "navaja2026", "role": "admin"}); code != 400 {
		t.Fatalf("unknown field accepted: %d", code)
	}
	if code, body, _ := ana.do("POST", "/api/v1/auth/register", map[string]any{"name": "Ana", "email": "ana@test.es", "password": "navaja2026", "phone": "+34 600 000 000"}); code != 201 {
		t.Fatalf("register: %d %v", code, body)
	}
	if ana.cookie == nil || !ana.cookie.HttpOnly || ana.cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("refresh cookie not hardened: %+v", ana.cookie)
	}

	// Dos clientes intentan el mismo hueco a la vez: exactamente uno gana.
	bob := &client{t: t, h: h}
	bob.do("POST", "/api/v1/auth/register", map[string]any{"name": "Bob", "email": "bob@test.es", "password": "navaja2026"})
	book := map[string]any{"service_id": serviceID, "barber_id": barberID, "starts_at": startsAt, "notes": "degradado bajo"}
	var wg sync.WaitGroup
	results := make([]int, 2)
	var apptID string
	var mu sync.Mutex
	for i, c := range []*client{ana, bob} {
		wg.Add(1)
		go func(i int, c *client) {
			defer wg.Done()
			code, body, _ := c.do("POST", "/api/v1/appointments", book)
			results[i] = code
			if code == 201 {
				mu.Lock()
				apptID = body["id"].(string)
				mu.Unlock()
			}
		}(i, c)
	}
	wg.Wait()
	created, conflicts := 0, 0
	for _, r := range results {
		switch r {
		case 201:
			created++
		case 409, 422:
			conflicts++
		}
	}
	if created != 1 || conflicts != 1 {
		t.Fatalf("concurrent booking results: %v", results)
	}

	winner := ana
	if results[1] == 201 {
		winner = bob
	}
	code, _, mine := winner.do("GET", "/api/v1/appointments/mine", nil)
	if code != 200 || len(mine) != 1 {
		t.Fatalf("mine: %d %v", code, mine)
	}

	// El hueco desaparece de la disponibilidad de ese barbero.
	_, avail, _ = anon.do("GET", "/api/v1/availability?service_id="+serviceID+"&barber_id="+barberID+"&date="+date, nil)
	for _, s := range avail["slots"].([]any) {
		if s.(map[string]any)["starts_at"] == startsAt {
			t.Fatal("booked slot still offered")
		}
	}

	// El cliente no puede ver el panel de admin ni cancelar citas ajenas.
	loser := bob
	if winner == bob {
		loser = ana
	}
	if code, _, _ := loser.do("GET", "/api/v1/admin/stats", nil); code != 403 {
		t.Fatalf("client reached admin: %d", code)
	}
	if code, _, _ := loser.do("POST", "/api/v1/appointments/"+apptID+"/cancel", nil); code != 404 {
		t.Fatalf("foreign cancel: %d", code)
	}

	// Refresh: rota cookie; reutilizar la antigua revoca la sesión.
	old := *winner.cookie
	if code, _, _ := winner.do("POST", "/api/v1/auth/refresh", nil); code != 200 {
		t.Fatalf("refresh: %d", code)
	}
	replay := &client{t: t, h: h, cookie: &old}
	if code, _, _ := replay.do("POST", "/api/v1/auth/refresh", nil); code != 401 {
		t.Fatalf("replayed refresh accepted: %d", code)
	}
	if code, _, _ := winner.do("POST", "/api/v1/auth/refresh", nil); code != 401 {
		t.Fatalf("family not revoked after reuse: %d", code)
	}

	// Login de nuevo y cancelación por el dueño.
	email := map[*client]string{ana: "ana@test.es", bob: "bob@test.es"}[winner]
	if code, _, _ := winner.do("POST", "/api/v1/auth/login", map[string]any{"email": strings.ToUpper(email), "password": "navaja2026"}); code != 200 {
		t.Fatalf("login: %d", code)
	}
	if code, body, _ := winner.do("POST", "/api/v1/appointments/"+apptID+"/cancel", nil); code != 200 || body["status"] != "cancelled" {
		t.Fatalf("cancel: %d %v", code, body)
	}

	// Admin: promoción vía rol en BD y acceso al panel.
	if _, err := db.Exec(ctx, `UPDATE users SET role = 'admin' WHERE email = $1`, email); err != nil {
		t.Fatal(err)
	}
	winner.do("POST", "/api/v1/auth/login", map[string]any{"email": email, "password": "navaja2026"})
	if code, body, _ := winner.do("GET", "/api/v1/admin/stats", nil); code != 200 || body["clients_count"] == nil {
		t.Fatalf("admin stats: %d %v", code, body)
	}
	if code, body, _ := winner.do("GET", "/api/v1/admin/users?q=%27%20OR%201=1--", nil); code != 200 || body["total"].(float64) != 0 {
		t.Fatalf("search injection: %d %v", code, body)
	}
	if code, body, _ := winner.do("GET", "/api/v1/admin/appointments?status=cancelled", nil); code != 200 || body["total"].(float64) != 1 {
		t.Fatalf("admin appointments: %d %v", code, body)
	}
}

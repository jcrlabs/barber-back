package notify

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jcrlabs/barber-back/internal/domain"
)

func TestDialablePhone(t *testing.T) {
	cases := map[string]string{"+34 600 11-22 33": "+34600112233", "(600) 112 233": "600112233", "6+00": "600", "": ""}
	for in, want := range cases {
		if got := DialablePhone(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestNtfySendsCallAction(t *testing.T) {
	got := make(chan *http.Request, 1)
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		got <- r
	}))
	defer srv.Close()

	loc, _ := time.LoadLocation("Europe/Madrid")
	n := New(Config{NtfyURL: srv.URL, PublicURL: "https://x.test", Location: loc}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	n.NewBooking(&domain.Appointment{ClientName: "Ana", ClientPhone: "+34 600 112 233", ServiceName: "Corte",
		BarberName: "Edgar", StartsAt: time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)})

	select {
	case r := <-got:
		if a := r.Header.Get("Actions"); !strings.Contains(a, "tel:+34600112233") || !strings.Contains(a, "wa.me/34600112233") {
			t.Fatalf("actions: %q", a)
		}
		if !strings.Contains(body, "Ana") || !strings.Contains(body, "10:00") {
			t.Fatalf("body: %q", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no notification sent")
	}
}

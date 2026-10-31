package notify

import (
	"context"
	"net/mail"

	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

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

func TestWhatsAppNumber(t *testing.T) {
	cases := map[string]string{"600 11 22 33": "34600112233", "+44 7700 900123": "447700900123", "0034600112233": "0034600112233"}
	for in, want := range cases {
		if got := WhatsAppNumber(in); got != want {
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

func TestBarberChannels(t *testing.T) {
	hits := make(chan *http.Request, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits <- r }))
	defer srv.Close()

	loc, _ := time.LoadLocation("Europe/Madrid")
	n := New(Config{NtfyBase: srv.URL, CallMeBotURL: srv.URL + "/wa", PublicURL: "https://x.test", Location: loc},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	rc := &domain.BarberNotify{NtfyTopic: "edgarbarber-abc", WhatsAppPhone: "600 112 233", CallMeBotKey: "123456", Email: "e@x.test"}
	n.WithRecipients(func(context.Context, uuid.UUID) (*domain.BarberNotify, error) { return rc, nil })

	res := n.Test(context.Background(), "Edgar", rc)
	if res["ntfy"] != "ok" || res["whatsapp"] != "ok" || res["email"] != "off" {
		t.Fatalf("results: %v", res)
	}
	seen := map[string]bool{}
	for range 2 {
		r := <-hits
		seen[r.URL.Path] = true
		if r.URL.Path == "/wa" && (r.URL.Query().Get("phone") != "+34600112233" || r.URL.Query().Get("apikey") != "123456") {
			t.Fatalf("callmebot query: %v", r.URL.RawQuery)
		}
	}
	if !seen["/edgarbarber-abc"] || !seen["/wa"] {
		t.Fatalf("channels hit: %v", seen)
	}

	n.Cancelled(&domain.Appointment{ClientName: "Ana", ServiceName: "Corte", BarberName: "Edgar", StartsAt: time.Now()})
	for range 2 {
		select {
		case <-hits:
		case <-time.After(3 * time.Second):
			t.Fatal("cancellation not sent to barber")
		}
	}
}

func TestEmailHeadersSanitized(t *testing.T) {
	from := &mail.Address{Address: "a@x.test"}
	to := &mail.Address{Address: "b@x.test"}
	msg := buildMessage(from, to, "Hola\r\nBcc: evil@x.test", "cuerpo")
	if strings.Contains(msg, "\r\nBcc:") {
		t.Fatalf("header injection: %q", msg)
	}
}

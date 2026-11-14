package notify

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jcrlabs/barber-back/internal/domain"
)

func TestReminderEmailContent(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Madrid")
	n := New(Config{PublicURL: "https://x.test", Location: loc}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	subject, body := n.composeReminder(&domain.Appointment{ClientName: "Ana", ServiceName: "Pelo + barba",
		BarberName: "Edgar", StartsAt: time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC)})
	if !strings.Contains(subject, "10:30") {
		t.Fatalf("subject: %q", subject)
	}
	for _, want := range []string{"Hola Ana", "Pelo + barba con Edgar", "Martes 03/11/2026 a las 10:30", "https://x.test/mis-citas"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body misses %q: %q", want, body)
		}
	}
}

func TestReminderNeedsSMTPAndEmail(t *testing.T) {
	n := New(Config{Location: time.UTC}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := n.Reminder(context.Background(), &domain.Appointment{ClientEmail: "ana@test.es"}); !errors.Is(err, errNoEmail) {
		t.Fatalf("without SMTP: %v", err)
	}
	n = New(Config{Location: time.UTC, SMTP: SMTPConfig{Host: "smtp.test", From: "a@test.es"}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := n.Reminder(context.Background(), &domain.Appointment{}); !errors.Is(err, errNoEmail) {
		t.Fatalf("without client email: %v", err)
	}
}

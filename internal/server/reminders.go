package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/jcrlabs/barber-back/internal/notify"
	"github.com/jcrlabs/barber-back/internal/repository"
)

// sendDueReminders reclama las citas que entran en la ventana de recordatorio y avisa a cada cliente.
// Una cita reclamada no se reintenta: preferimos perder un recordatorio a mandarlo dos veces.
func sendDueReminders(ctx context.Context, appts *repository.AppointmentRepository, n *notify.Notifier, lead time.Duration, logger *slog.Logger) {
	due, err := appts.ClaimDueReminders(ctx, time.Now(), int(lead/time.Minute))
	if err != nil {
		logger.Warn("reminder claim failed", "err", err)
		return
	}
	for i := range due {
		a := &due[i]
		sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		if err := n.Reminder(sendCtx, a); err != nil {
			logger.Warn("reminder failed", "err", err, "appointment_id", a.ID)
		} else {
			logger.Info("reminder sent", "appointment_id", a.ID)
		}
		cancel()
	}
}

func runReminders(appts *repository.AppointmentRepository, n *notify.Notifier, lead time.Duration, logger *slog.Logger) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		sendDueReminders(context.Background(), appts, n, lead, logger)
	}
}

package notify

import (
	"context"
	"errors"
	"fmt"

	"github.com/jcrlabs/barber-back/internal/domain"
)

var errNoEmail = errors.New("email disabled or client without email")

// Reminder envía al cliente un email recordándole su cita. Es síncrono: lo llama el worker de recordatorios.
func (n *Notifier) Reminder(ctx context.Context, a *domain.Appointment) error {
	if !n.cfg.SMTP.Enabled() || a.ClientEmail == "" {
		return errNoEmail
	}
	subject, body := n.composeReminder(a)
	return n.cfg.SMTP.Send(ctx, a.ClientEmail, subject, body)
}

func (n *Notifier) composeReminder(a *domain.Appointment) (subject, body string) {
	start := a.StartsAt.In(n.cfg.Location)
	subject = fmt.Sprintf("Recordatorio: tu cita en Edgar Barber a las %s", start.Format("15:04"))
	body = fmt.Sprintf("Hola %s,\n\nTe recordamos tu cita de hoy:\n\n%s con %s\n%s %s a las %s\n\n"+
		"Si no puedes venir, avísanos cuanto antes.\nTus citas: %s/mis-citas\n",
		a.ClientName, a.ServiceName, a.BarberName,
		weekdays[start.Weekday()], start.Format("02/01/2006"), start.Format("15:04"), n.cfg.PublicURL)
	return subject, body
}

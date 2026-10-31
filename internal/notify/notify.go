// Package notify avisa de las reservas nuevas y anuladas por canales externos.
//
// Hay dos destinatarios:
//   - la barbería (admin): topic de ntfy y chat de Telegram configurados por entorno;
//   - el barbero de la cita: su propio topic de ntfy, su email (si hay SMTP) y su WhatsApp (CallMeBot).
//
// Los envíos son asíncronos y con timeout: un canal caído nunca bloquea ni rompe una reserva.
package notify

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jcrlabs/barber-back/internal/domain"
)

type Config struct {
	NtfyURL       string // topic de la barbería, p.ej. https://ntfy.sh/edgarbarber-xxxx
	NtfyBase      string // servidor ntfy para los topics de cada barbero (por defecto https://ntfy.sh)
	NtfyToken     string // opcional, para topics protegidos
	TelegramToken string
	TelegramChat  string
	CallMeBotURL  string // API de WhatsApp de CallMeBot (configurable para tests)
	SMTP          SMTPConfig
	PublicURL     string // para enlazar al panel
	Location      *time.Location
}

// Recipients resuelve los canales del barbero de una cita.
type Recipients func(ctx context.Context, barberID uuid.UUID) (*domain.BarberNotify, error)

type Notifier struct {
	cfg        Config
	client     *http.Client
	logger     *slog.Logger
	recipients Recipients
}

func New(cfg Config, logger *slog.Logger) *Notifier {
	if cfg.NtfyBase == "" {
		cfg.NtfyBase = "https://ntfy.sh"
	}
	cfg.NtfyBase = strings.TrimRight(cfg.NtfyBase, "/")
	if cfg.CallMeBotURL == "" {
		cfg.CallMeBotURL = "https://api.callmebot.com/whatsapp.php"
	}
	return &Notifier{cfg: cfg, client: &http.Client{Timeout: 10 * time.Second}, logger: logger}
}

// WithRecipients activa los avisos personales a cada barbero.
func (n *Notifier) WithRecipients(r Recipients) *Notifier {
	n.recipients = r
	return n
}

// Channels lista los canales de la barbería configurados (para el panel de admin).
func (n *Notifier) Channels() []string {
	out := []string{}
	if n.cfg.NtfyURL != "" {
		out = append(out, "ntfy")
	}
	if n.cfg.TelegramToken != "" && n.cfg.TelegramChat != "" {
		out = append(out, "telegram")
	}
	return out
}

// EmailEnabled indica si hay servidor SMTP para los avisos por email.
func (n *Notifier) EmailEnabled() bool { return n.cfg.SMTP.Enabled() }

// NtfyTopicURL es la URL a la que se suscribe el barbero en la app de ntfy.
func (n *Notifier) NtfyTopicURL(topic string) string {
	if topic == "" {
		return ""
	}
	return n.cfg.NtfyBase + "/" + topic
}

// DialablePhone deja solo dígitos y el + inicial, apto para tel: y wa.me.
func DialablePhone(p string) string {
	var b strings.Builder
	for i, r := range strings.TrimSpace(p) {
		if (r >= '0' && r <= '9') || (r == '+' && i == 0) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// WhatsAppNumber devuelve el número para wa.me: sin +, y con prefijo 34 si es un número nacional de 9 cifras.
func WhatsAppNumber(p string) string {
	d := DialablePhone(p)
	if strings.HasPrefix(d, "+") {
		return d[1:]
	}
	if len(d) == 9 {
		return "34" + d
	}
	return d
}

var weekdays = [...]string{"Domingo", "Lunes", "Martes", "Miércoles", "Jueves", "Viernes", "Sábado"}

type event struct {
	verb string // "Nueva cita", "Cita anulada"...
	tags string // emojis de ntfy
}

var (
	evBooked    = event{verb: "Nueva cita", tags: "scissors,calendar"}
	evCancelled = event{verb: "Cita anulada", tags: "x,calendar"}
	evTest      = event{verb: "Prueba de avisos", tags: "bell"}
)

type message struct {
	title, body string
	phone       string // teléfono del cliente, para los botones de llamar/WhatsApp
}

func (n *Notifier) compose(ev event, a *domain.Appointment) message {
	start := a.StartsAt.In(n.cfg.Location)
	m := message{phone: a.ClientPhone}
	m.title = fmt.Sprintf("%s · %s %s", ev.verb, start.Format("02/01"), start.Format("15:04"))
	m.body = fmt.Sprintf("%s\n%s con %s\n%s %s a las %s\nTel: %s",
		a.ClientName, a.ServiceName, a.BarberName, weekdays[start.Weekday()], start.Format("02/01/2006"), start.Format("15:04"), a.ClientPhone)
	if a.Notes != "" {
		m.body += "\nNota: " + a.Notes
	}
	return m
}

func (n *Notifier) NewBooking(a *domain.Appointment) { n.dispatch(evBooked, a) }

func (n *Notifier) Cancelled(a *domain.Appointment) { n.dispatch(evCancelled, a) }

func (n *Notifier) dispatch(ev event, a *domain.Appointment) {
	if a == nil {
		return
	}
	cp := *a
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		m := n.compose(ev, &cp)
		n.toShop(ctx, ev, m, cp.ID)
		if n.recipients == nil {
			return
		}
		r, err := n.recipients(ctx, cp.BarberID)
		if err != nil {
			n.logger.Warn("barber recipients lookup failed", "err", err, "barber_id", cp.BarberID)
			return
		}
		for ch, err := range n.toBarber(ctx, ev, m, r) {
			n.logger.Warn("barber notification failed", "channel", ch, "err", err, "appointment_id", cp.ID)
		}
	}()
}

func (n *Notifier) toShop(ctx context.Context, ev event, m message, id uuid.UUID) {
	if n.cfg.NtfyURL != "" {
		if err := n.ntfy(ctx, n.cfg.NtfyURL, ev, m); err != nil {
			n.logger.Warn("ntfy notification failed", "err", err, "appointment_id", id)
		}
	}
	if n.cfg.TelegramToken != "" && n.cfg.TelegramChat != "" {
		if err := n.telegram(ctx, m); err != nil {
			n.logger.Warn("telegram notification failed", "err", err, "appointment_id", id)
		}
	}
}

// toBarber envía por cada canal configurado del barbero y devuelve los errores por canal.
func (n *Notifier) toBarber(ctx context.Context, ev event, m message, r *domain.BarberNotify) map[string]error {
	errs := map[string]error{}
	// Si el topic del barbero es el mismo que el de la barbería no lo duplicamos.
	if topic := n.NtfyTopicURL(r.NtfyTopic); topic != "" && topic != n.cfg.NtfyURL {
		if err := n.ntfy(ctx, topic, ev, m); err != nil {
			errs["ntfy"] = err
		}
	}
	if r.Email != "" && n.cfg.SMTP.Enabled() {
		if err := n.cfg.SMTP.Send(ctx, r.Email, m.title, n.emailBody(m)); err != nil {
			errs["email"] = err
		}
	}
	if r.WhatsAppPhone != "" && r.CallMeBotKey != "" {
		if err := n.callmebot(ctx, r, m); err != nil {
			errs["whatsapp"] = err
		}
	}
	return errs
}

// Test envía un aviso de prueba por todos los canales del barbero y devuelve el resultado de cada uno.
func (n *Notifier) Test(ctx context.Context, barberName string, r *domain.BarberNotify) map[string]string {
	m := message{
		title: evTest.verb,
		body:  fmt.Sprintf("Hola %s, así te llegarán las reservas nuevas.\nPanel: %s/agenda", barberName, n.cfg.PublicURL),
	}
	errs := n.toBarber(ctx, evTest, m, r)
	out := map[string]string{}
	mark := func(ch string, enabled bool) {
		switch {
		case !enabled:
			out[ch] = "off"
		case errs[ch] != nil:
			out[ch] = "error"
		default:
			out[ch] = "ok"
		}
	}
	mark("ntfy", r.NtfyTopic != "")
	mark("email", r.Email != "" && n.cfg.SMTP.Enabled())
	mark("whatsapp", r.WhatsAppPhone != "" && r.CallMeBotKey != "")
	return out
}

func (n *Notifier) emailBody(m message) string {
	b := m.body + "\n\nAbrir agenda: " + n.cfg.PublicURL + "/agenda\n"
	if tel := DialablePhone(m.phone); tel != "" {
		b += "Llamar: tel:" + tel + "\n"
		if wa := WhatsAppNumber(m.phone); wa != "" {
			b += "WhatsApp: https://wa.me/" + wa + "\n"
		}
	}
	return b
}

func (n *Notifier) ntfy(ctx context.Context, topicURL string, ev event, m message) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, topicURL, strings.NewReader(m.body))
	if err != nil {
		return err
	}
	// Las cabeceras HTTP no admiten saltos de línea ni bytes no ASCII fiables: el título va como RFC 2047.
	req.Header.Set("Title", "=?UTF-8?B?"+b64(m.title)+"?=")
	req.Header.Set("Tags", ev.tags)
	req.Header.Set("Priority", "high")
	req.Header.Set("Click", n.cfg.PublicURL+"/agenda")
	if tel := DialablePhone(m.phone); tel != "" {
		actions := "view, Llamar, tel:" + tel
		if wa := WhatsAppNumber(m.phone); wa != "" {
			actions += "; view, WhatsApp, https://wa.me/" + wa
		}
		req.Header.Set("Actions", actions)
	}
	if n.cfg.NtfyToken != "" && strings.HasPrefix(topicURL, n.cfg.NtfyBase) {
		req.Header.Set("Authorization", "Bearer "+n.cfg.NtfyToken)
	}
	return n.do(req)
}

func (n *Notifier) telegram(ctx context.Context, m message) error {
	payload := map[string]any{"chat_id": n.cfg.TelegramChat, "text": "✂️ " + m.title + "\n\n" + m.body}
	buttons := []map[string]string{{"text": "Abrir agenda", "url": n.cfg.PublicURL + "/agenda"}}
	if wa := WhatsAppNumber(m.phone); wa != "" {
		buttons = append(buttons, map[string]string{"text": "WhatsApp", "url": "https://wa.me/" + wa})
	}
	payload["reply_markup"] = map[string]any{"inline_keyboard": [][]map[string]string{buttons}}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.telegram.org/bot"+n.cfg.TelegramToken+"/sendMessage", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return n.do(req)
}

// callmebot manda un WhatsApp al propio barbero con la API gratuita de CallMeBot.
func (n *Notifier) callmebot(ctx context.Context, r *domain.BarberNotify, m message) error {
	text := "*" + m.title + "*\n" + m.body
	if wa := WhatsAppNumber(m.phone); wa != "" {
		text += "\nhttps://wa.me/" + wa
	}
	q := url.Values{"phone": {"+" + WhatsAppNumber(r.WhatsAppPhone)}, "text": {text}, "apikey": {r.CallMeBotKey}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, n.cfg.CallMeBotURL+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	return n.do(req)
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

var errUpstream = errors.New("upstream error")

func (n *Notifier) do(req *http.Request) error {
	res, err := n.client.Do(req) //nolint:gosec // destino fijado por configuración del operador o del barbero
	if err != nil {
		// No propagamos la URL: la de Telegram y la de CallMeBot llevan credenciales.
		return fmt.Errorf("request to %s failed: %w", req.URL.Host, errUpstream)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s responded %d: %w", req.URL.Host, res.StatusCode, errUpstream)
	}
	return nil
}

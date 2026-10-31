// Package notify avisa a la barbería de las reservas nuevas por canales externos (ntfy y Telegram).
// Los envíos son asíncronos y con timeout: un canal caído nunca bloquea ni rompe una reserva.
package notify

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jcrlabs/barber-back/internal/domain"
)

type Config struct {
	NtfyURL       string // URL completa del topic, p.ej. https://ntfy.sh/edgarbarber-xxxx
	NtfyToken     string // opcional, para topics protegidos
	TelegramToken string
	TelegramChat  string
	PublicURL     string // para enlazar al panel
	Location      *time.Location
}

type Notifier struct {
	cfg    Config
	client *http.Client
	logger *slog.Logger
}

func New(cfg Config, logger *slog.Logger) *Notifier {
	return &Notifier{cfg: cfg, client: &http.Client{Timeout: 10 * time.Second}, logger: logger}
}

// Channels lista los canales configurados (para el panel de admin).
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

func (n *Notifier) message(a *domain.Appointment) (title, body string) {
	start := a.StartsAt.In(n.cfg.Location)
	title = fmt.Sprintf("Nueva cita · %s %s", start.Format("02/01"), start.Format("15:04"))
	body = fmt.Sprintf("%s\n%s con %s\n%s a las %s\nTel: %s",
		a.ClientName, a.ServiceName, a.BarberName, start.Format("Mon 02/01/2006"), start.Format("15:04"), a.ClientPhone)
	if a.Notes != "" {
		body += "\nNota: " + a.Notes
	}
	return title, body
}

func (n *Notifier) NewBooking(a *domain.Appointment) {
	if len(n.Channels()) == 0 || a == nil {
		return
	}
	cp := *a
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		title, body := n.message(&cp)
		if n.cfg.NtfyURL != "" {
			if err := n.ntfy(ctx, title, body, &cp); err != nil {
				n.logger.Warn("ntfy notification failed", "err", err, "appointment_id", cp.ID)
			}
		}
		if n.cfg.TelegramToken != "" && n.cfg.TelegramChat != "" {
			if err := n.telegram(ctx, title, body, &cp); err != nil {
				n.logger.Warn("telegram notification failed", "err", err, "appointment_id", cp.ID)
			}
		}
	}()
}

func (n *Notifier) ntfy(ctx context.Context, title, body string, a *domain.Appointment) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.cfg.NtfyURL, strings.NewReader(body))
	if err != nil {
		return err
	}
	// Las cabeceras HTTP no admiten saltos de línea ni bytes no ASCII fiables: el título va como RFC 2047.
	req.Header.Set("Title", "=?UTF-8?B?"+b64(title)+"?=")
	req.Header.Set("Tags", "scissors,calendar")
	req.Header.Set("Priority", "high")
	req.Header.Set("Click", n.cfg.PublicURL+"/agenda")
	if tel := DialablePhone(a.ClientPhone); tel != "" {
		actions := "view, Llamar, tel:" + tel
		if wa := WhatsAppNumber(a.ClientPhone); wa != "" {
			actions += "; view, WhatsApp, https://wa.me/" + wa
		}
		req.Header.Set("Actions", actions)
	}
	if n.cfg.NtfyToken != "" {
		req.Header.Set("Authorization", "Bearer "+n.cfg.NtfyToken)
	}
	return n.do(req)
}

func (n *Notifier) telegram(ctx context.Context, title, body string, a *domain.Appointment) error {
	payload := map[string]any{"chat_id": n.cfg.TelegramChat, "text": "✂️ " + title + "\n\n" + body}
	buttons := []map[string]string{{"text": "Abrir agenda", "url": n.cfg.PublicURL + "/agenda"}}
	if wa := WhatsAppNumber(a.ClientPhone); wa != "" {
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

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func (n *Notifier) do(req *http.Request) error {
	res, err := n.client.Do(req) //nolint:gosec // destino fijado por configuración del operador
	if err != nil {
		// No propagamos la URL: la de Telegram contiene el token del bot.
		return fmt.Errorf("request to %s failed", req.URL.Host)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s responded %d", req.URL.Host, res.StatusCode)
	}
	return nil
}

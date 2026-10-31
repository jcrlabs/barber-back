package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// SMTPConfig configura el envío de emails. Con el puerto 465 se usa TLS implícito; con el resto, STARTTLS.
type SMTPConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	From     string
}

func (c SMTPConfig) Enabled() bool { return c.Host != "" && c.From != "" }

var errBadAddress = errors.New("invalid email address")

// Send manda un email de texto plano. Asunto y destinatario se sanean para evitar inyección de cabeceras.
func (c SMTPConfig) Send(ctx context.Context, to, subject, body string) error {
	rcpt, err := mail.ParseAddress(to)
	if err != nil {
		return errBadAddress
	}
	from, err := mail.ParseAddress(c.From)
	if err != nil {
		return errBadAddress
	}
	port := c.Port
	if port == "" {
		port = "587"
	}
	addr := net.JoinHostPort(c.Host, port)
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(15 * time.Second)
	}
	dialer := &net.Dialer{Deadline: deadline}
	tlsCfg := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}

	var conn net.Conn
	if port == "465" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("smtp dial %s: %w", c.Host, errUpstream)
	}
	_ = conn.SetDeadline(deadline)
	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp handshake: %w", errUpstream)
	}
	defer func() { _ = cl.Close() }()
	if port != "465" {
		if ok, _ := cl.Extension("STARTTLS"); ok {
			if err := cl.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("smtp starttls: %w", errUpstream)
			}
		}
	}
	if c.User != "" {
		if err := cl.Auth(smtp.PlainAuth("", c.User, c.Password, c.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", errUpstream)
		}
	}
	if err := cl.Mail(from.Address); err != nil {
		return fmt.Errorf("smtp from: %w", errUpstream)
	}
	if err := cl.Rcpt(rcpt.Address); err != nil {
		return fmt.Errorf("smtp rcpt: %w", errUpstream)
	}
	w, err := cl.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", errUpstream)
	}
	if _, err := w.Write([]byte(buildMessage(from, rcpt, subject, body))); err != nil {
		return fmt.Errorf("smtp write: %w", errUpstream)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close: %w", errUpstream)
	}
	return cl.Quit()
}

func buildMessage(from, to *mail.Address, subject, body string) string {
	subject = strings.NewReplacer("\r", " ", "\n", " ").Replace(subject)
	var b strings.Builder
	b.WriteString("From: " + from.String() + "\r\n")
	b.WriteString("To: " + to.String() + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n"))
	return b.String()
}

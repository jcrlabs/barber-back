package config

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type OAuthProvider struct {
	ClientID     string
	ClientSecret string
}

func (p OAuthProvider) Enabled() bool { return p.ClientID != "" && p.ClientSecret != "" }

type Config struct {
	Env            string
	Port           string
	DatabaseURL    string
	PublicURL      string // URL pública de la app (front), p.ej. https://barber.jcrlabs.net
	AllowedOrigins []string
	TrustedProxies []string
	// TrustedPlatform="cloudflare" toma la IP real del cliente de CF-Connecting-IP (solo si todo el tráfico entra por Cloudflare).
	TrustedPlatform string
	MaxRequestSize  int64

	JWTPrivateKey *rsa.PrivateKey
	JWTIssuer     string
	AccessTTL     time.Duration
	RefreshTTL    time.Duration

	AdminEmails map[string]struct{}

	// Cuenta admin inicial (usuario + contraseña). Solo se crea si no existe; luego se cambia desde el perfil.
	BootstrapAdminLogin    string
	BootstrapAdminPassword string

	Google OAuthProvider
	GitHub OAuthProvider

	ShopTimezone  *time.Location
	SlotStep      time.Duration
	BookingLead   time.Duration
	BookingWindow int // días hacia delante que se pueden reservar
	CancelWindow  time.Duration
	MaxActiveAppt int
	SeedDemoData  bool

	// Avisos de reservas nuevas (opcionales).
	NtfyURL       string
	NtfyToken     string
	TelegramToken string
	TelegramChat  string
	// NtfyBase es el servidor ntfy de los topics personales de cada barbero.
	NtfyBase string
	SMTP     SMTPConfig
}

// SMTPConfig habilita los avisos por email a los barberos (p.ej. Gmail con contraseña de aplicación).
type SMTPConfig struct {
	Host, Port, User, Password, From string
}

func (c *Config) IsProduction() bool { return c.Env == "production" }

// CookieSecure indica si las cookies deben marcarse como Secure.
func (c *Config) CookieSecure() bool { return strings.HasPrefix(c.PublicURL, "https://") }

func Load() (*Config, error) {
	cfg := &Config{
		Env:                    getEnv("APP_ENV", "development"),
		Port:                   getEnv("PORT", "8080"),
		PublicURL:              strings.TrimRight(getEnv("PUBLIC_URL", "http://localhost:5173"), "/"),
		AllowedOrigins:         splitList(getEnv("CORS_ALLOWED_ORIGINS", "http://localhost:5173")),
		TrustedProxies:         splitList(getEnv("TRUSTED_PROXIES", "10.0.0.0/8,127.0.0.1/32")),
		TrustedPlatform:        strings.ToLower(os.Getenv("TRUSTED_PLATFORM")),
		MaxRequestSize:         int64(getInt("MAX_REQUEST_SIZE_BYTES", 1<<20)),
		JWTIssuer:              getEnv("JWT_ISSUER", "barber-back"),
		AccessTTL:              time.Duration(getInt("JWT_ACCESS_TTL_MIN", 15)) * time.Minute,
		RefreshTTL:             time.Duration(getInt("JWT_REFRESH_TTL_DAYS", 30)) * 24 * time.Hour,
		AdminEmails:            map[string]struct{}{},
		BootstrapAdminLogin:    strings.ToLower(strings.TrimSpace(os.Getenv("BOOTSTRAP_ADMIN_LOGIN"))),
		BootstrapAdminPassword: os.Getenv("BOOTSTRAP_ADMIN_PASSWORD"),
		Google:                 OAuthProvider{ClientID: os.Getenv("GOOGLE_CLIENT_ID"), ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET")},
		GitHub:                 OAuthProvider{ClientID: os.Getenv("GITHUB_CLIENT_ID"), ClientSecret: os.Getenv("GITHUB_CLIENT_SECRET")},
		SlotStep:               time.Duration(getInt("SLOT_STEP_MIN", 30)) * time.Minute,
		BookingLead:            time.Duration(getInt("BOOKING_LEAD_MIN", 60)) * time.Minute,
		BookingWindow:          getInt("BOOKING_WINDOW_DAYS", 45),
		CancelWindow:           time.Duration(getInt("CANCEL_WINDOW_MIN", 120)) * time.Minute,
		MaxActiveAppt:          getInt("MAX_ACTIVE_APPOINTMENTS", 3),
		SeedDemoData:           getEnv("SEED_DEMO_DATA", "true") == "true",
		NtfyURL:                strings.TrimSpace(os.Getenv("NTFY_URL")),
		NtfyToken:              os.Getenv("NTFY_TOKEN"),
		TelegramToken:          os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChat:           os.Getenv("TELEGRAM_CHAT_ID"),
		NtfyBase:               strings.TrimRight(getEnv("NTFY_BASE_URL", "https://ntfy.sh"), "/"),
		SMTP: SMTPConfig{
			Host: strings.TrimSpace(os.Getenv("SMTP_HOST")), Port: getEnv("SMTP_PORT", "587"),
			User: os.Getenv("SMTP_USER"), Password: os.Getenv("SMTP_PASSWORD"), From: os.Getenv("SMTP_FROM"),
		},
	}

	for _, e := range splitList(os.Getenv("ADMIN_EMAILS")) {
		cfg.AdminEmails[strings.ToLower(e)] = struct{}{}
	}

	loc, err := time.LoadLocation(getEnv("SHOP_TIMEZONE", "Europe/Madrid"))
	if err != nil {
		return nil, fmt.Errorf("SHOP_TIMEZONE: %w", err)
	}
	cfg.ShopTimezone = loc

	cfg.DatabaseURL, err = databaseURL()
	if err != nil {
		return nil, err
	}

	cfg.JWTPrivateKey, err = loadPrivateKey()
	if err != nil {
		return nil, err
	}
	if cfg.JWTPrivateKey == nil && cfg.IsProduction() {
		return nil, errors.New("JWT_PRIVATE_KEY is required in production")
	}

	if _, err := url.Parse(cfg.PublicURL); err != nil {
		return nil, fmt.Errorf("PUBLIC_URL: %w", err)
	}
	if cfg.NtfyURL != "" && !strings.HasPrefix(cfg.NtfyURL, "https://") && !strings.HasPrefix(cfg.NtfyURL, "http://") {
		return nil, errors.New("NTFY_URL must be an http(s) URL")
	}
	if !strings.HasPrefix(cfg.NtfyBase, "https://") && !strings.HasPrefix(cfg.NtfyBase, "http://") {
		return nil, errors.New("NTFY_BASE_URL must be an http(s) URL")
	}
	if (cfg.SMTP.Host == "") != (cfg.SMTP.From == "") {
		return nil, errors.New("SMTP_HOST and SMTP_FROM must be set together")
	}
	if cfg.TrustedPlatform != "" && cfg.TrustedPlatform != "cloudflare" {
		return nil, errors.New("TRUSTED_PLATFORM must be empty or cloudflare")
	}
	if cfg.SlotStep <= 0 || cfg.BookingWindow <= 0 {
		return nil, errors.New("SLOT_STEP_MIN and BOOKING_WINDOW_DAYS must be positive")
	}
	return cfg, nil
}

// databaseURL usa DATABASE_URL o la compone a partir de DB_* (formato del chart).
func databaseURL() (string, error) {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v, nil
	}
	user, pass := os.Getenv("DB_USER"), os.Getenv("DB_PASSWORD")
	if user == "" {
		return "", errors.New("DATABASE_URL or DB_USER/DB_PASSWORD must be set")
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, pass),
		Host:     getEnv("DB_HOST", "localhost") + ":" + getEnv("DB_PORT", "5432"),
		Path:     "/" + getEnv("DB_NAME", "barber"),
		RawQuery: "sslmode=" + getEnv("DB_SSLMODE", "disable"),
	}
	return u.String(), nil
}

// loadPrivateKey acepta JWT_PRIVATE_KEY (PEM o PEM en base64) o JWT_PRIVATE_KEY_FILE.
// Devuelve nil si no hay clave configurada (solo válido fuera de producción).
func loadPrivateKey() (*rsa.PrivateKey, error) {
	raw := os.Getenv("JWT_PRIVATE_KEY")
	if raw == "" {
		if path := os.Getenv("JWT_PRIVATE_KEY_FILE"); path != "" {
			b, err := os.ReadFile(path) //nolint:gosec // ruta definida por el operador, no por usuarios
			if err != nil {
				return nil, fmt.Errorf("JWT_PRIVATE_KEY_FILE: %w", err)
			}
			raw = string(b)
		}
	}
	if raw == "" {
		return nil, nil
	}
	if !strings.Contains(raw, "-----BEGIN") {
		dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("JWT_PRIVATE_KEY: invalid base64: %w", err)
		}
		raw = string(dec)
	}
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		return nil, errors.New("JWT_PRIVATE_KEY: invalid PEM")
	}
	rk, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		k, err8 := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err8 != nil {
			return nil, fmt.Errorf("JWT_PRIVATE_KEY: %w", err8)
		}
		var ok bool
		if rk, ok = k.(*rsa.PrivateKey); !ok {
			return nil, errors.New("JWT_PRIVATE_KEY: not an RSA key")
		}
	}
	if rk.N.BitLen() < 2048 {
		return nil, errors.New("JWT_PRIVATE_KEY: RSA key must be at least 2048 bits")
	}
	return rk, nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func splitList(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Package server cablea dependencias y rutas HTTP.
package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jcrlabs/barber-back/internal/config"
	"github.com/jcrlabs/barber-back/internal/domain"
	"github.com/jcrlabs/barber-back/internal/handlers"
	mw "github.com/jcrlabs/barber-back/internal/middleware"
	"github.com/jcrlabs/barber-back/internal/notify"
	"github.com/jcrlabs/barber-back/internal/repository"
	"github.com/jcrlabs/barber-back/internal/services"
)

type App struct {
	Router *gin.Engine
	Tokens *repository.TokenRepository
	Users  *repository.UserRepository
}

func New(cfg *config.Config, db *pgxpool.Pool, logger *slog.Logger) (*App, error) {
	handlers.RegisterValidators()

	users := repository.NewUserRepository(db)
	tokens := repository.NewTokenRepository(db)
	catalog := repository.NewCatalogRepository(db)
	appts := repository.NewAppointmentRepository(db)

	authSvc, err := services.NewAuthService(users, tokens, services.AuthConfig{
		Key: cfg.JWTPrivateKey, Issuer: cfg.JWTIssuer, AccessTTL: cfg.AccessTTL,
		RefreshTTL: cfg.RefreshTTL, AdminEmails: cfg.AdminEmails,
	})
	if err != nil {
		return nil, err
	}
	notifier := notify.New(notify.Config{
		NtfyURL: cfg.NtfyURL, NtfyToken: cfg.NtfyToken, TelegramToken: cfg.TelegramToken,
		TelegramChat: cfg.TelegramChat, PublicURL: cfg.PublicURL, Location: cfg.ShopTimezone,
		NtfyBase: cfg.NtfyBase, SMTP: notify.SMTPConfig(cfg.SMTP),
	}, logger).WithRecipients(func(ctx context.Context, barberID uuid.UUID) (*domain.BarberNotify, error) {
		return catalog.GetNotify(ctx, barberID)
	})
	bookingSvc := services.NewBookingService(catalog, appts, services.BookingRules{
		Location: cfg.ShopTimezone, SlotStep: cfg.SlotStep, LeadTime: cfg.BookingLead,
		WindowDays: cfg.BookingWindow, CancelWindow: cfg.CancelWindow, MaxActiveAppt: cfg.MaxActiveAppt,
	}).WithNotifier(notifier)
	oauth := services.NewOAuthRegistry(cfg.PublicURL,
		services.OAuthCredentials{ClientID: cfg.Google.ClientID, ClientSecret: cfg.Google.ClientSecret},
		services.OAuthCredentials{ClientID: cfg.GitHub.ClientID, ClientSecret: cfg.GitHub.ClientSecret},
	)

	authH := handlers.NewAuthHandler(authSvc, users, oauth, cfg.PublicURL, cfg.CookieSecure())
	bookingH := handlers.NewBookingHandler(bookingSvc, catalog, appts, users)
	staffH := handlers.NewStaffHandler(bookingSvc, appts, catalog, notifier)
	adminH := handlers.NewAdminHandler(catalog, appts, users, cfg.ShopTimezone)
	healthH := handlers.NewHealthHandler(db)

	if cfg.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	if err := r.SetTrustedProxies(cfg.TrustedProxies); err != nil {
		return nil, err
	}
	if cfg.TrustedPlatform == "cloudflare" {
		// Todo el tráfico llega por el túnel de Cloudflare: sin esto, todas las peticiones
		// parecen venir del balanceador y los límites por IP serían compartidos por todos.
		r.TrustedPlatform = gin.PlatformCloudflare
	}
	r.Use(gin.Recovery(), mw.RequestID(), mw.RequestLogger(logger), mw.SecurityHeaders(cfg.CookieSecure()))
	r.Use(cors.New(cors.Config{
		AllowOrigins:     cfg.AllowedOrigins,
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "X-Request-ID", "X-Requested-With"},
		ExposeHeaders:    []string{"X-Request-ID", "Retry-After"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))
	r.Use(mw.BodyLimit(cfg.MaxRequestSize))

	r.GET("/health", healthH.Health)
	r.GET("/livez", healthH.Live)

	api := r.Group("/api/v1")
	api.Use(mw.RateLimit(200, time.Minute, "too many requests"))
	api.GET("/health", healthH.Health)

	// ── Auth (públicas, con límites estrictos) ──
	authG := api.Group("/auth")
	csrf := mw.CSRFGuard(cfg.AllowedOrigins)
	authG.POST("/register", mw.RateLimit(3, time.Hour, "too many registrations, try again later"), authH.Register)
	authG.POST("/login", mw.RateLimit(10, 15*time.Minute, "too many login attempts, try again later"), authH.Login)
	authG.POST("/refresh", mw.RateLimit(60, 15*time.Minute, "too many refresh attempts"), csrf, authH.Refresh)
	authG.POST("/logout", csrf, authH.Logout)
	authG.GET("/providers", authH.Providers)
	oauthLimit := mw.RateLimit(20, 15*time.Minute, "too many social login attempts")
	authG.GET("/oauth/:provider/start", oauthLimit, authH.OAuthStart)
	authG.GET("/oauth/:provider/callback", oauthLimit, authH.OAuthCallback)

	// ── Catálogo público ──
	api.GET("/meta", bookingH.Meta)
	api.GET("/services", bookingH.Services)
	api.GET("/barbers", bookingH.Barbers)
	api.GET("/availability", bookingH.Availability)
	api.GET("/availability/days", bookingH.Days)

	// ── Usuario autenticado ──
	authed := api.Group("")
	// Además del límite por IP, cada usuario tiene su propio cupo: no se lo salta cambiando de IP.
	authed.Use(mw.AuthRequired(authSvc), mw.RateLimitUser(300, time.Minute, "too many requests"))
	authed.GET("/auth/me", authH.Me)
	authed.PATCH("/auth/me", mw.RateLimitUser(20, time.Hour, "too many profile changes"), authH.UpdateMe)
	authed.POST("/auth/me/password", mw.RateLimit(5, 15*time.Minute, "too many password changes"),
		mw.RateLimitUser(5, 15*time.Minute, "too many password changes"), authH.ChangePassword)
	authed.POST("/auth/logout-all", mw.RateLimitUser(10, time.Hour, "too many requests"), authH.LogoutAll)

	authed.GET("/appointments/mine", bookingH.Mine)
	authed.POST("/appointments", mw.RateLimit(20, time.Hour, "too many bookings, try again later"),
		mw.RateLimitUser(10, time.Hour, "too many bookings, try again later"), bookingH.Book)
	authed.POST("/appointments/:id/cancel", mw.RateLimitUser(20, time.Hour, "too many cancellations"), bookingH.Cancel)

	// ── Panel de barbero (barberos ven lo suyo; el admin, toda la barbería) ──
	staff := authed.Group("/staff", mw.RequireRole(domain.RoleBarber, domain.RoleAdmin))
	staff.GET("/agenda", staffH.Agenda)
	staffWrite := mw.RateLimitUser(300, time.Hour, "too many changes, slow down")
	staff.POST("/appointments/:id/status", staffWrite, staffH.SetStatus)
	staff.GET("/notifications", staffH.Notifications)
	staff.GET("/overrides", staffH.ListOverrides)
	staff.POST("/overrides", staffWrite, staffH.CreateOverride)
	staff.DELETE("/overrides/:id", staffWrite, staffH.DeleteOverride)
	staff.GET("/me", staffH.Me)
	staff.PUT("/me/hours", staffWrite, staffH.UpdateMyHours)
	staff.PUT("/me/notify", staffWrite, staffH.UpdateMyNotify)
	staff.POST("/me/notify/test", mw.RateLimitUser(5, 15*time.Minute, "too many test notifications"), staffH.TestMyNotify)

	// ── Admin ──
	admin := authed.Group("/admin", mw.RequireRole(domain.RoleAdmin), mw.RateLimitUser(600, time.Hour, "too many admin requests"))
	admin.GET("/stats", adminH.Stats)
	admin.GET("/services", adminH.ListServices)
	admin.POST("/services", adminH.CreateService)
	admin.PUT("/services/:id", adminH.UpdateService)
	admin.DELETE("/services/:id", adminH.DeleteService)
	admin.GET("/barbers", adminH.ListBarbers)
	admin.POST("/barbers", adminH.CreateBarber)
	admin.PUT("/barbers/:id", adminH.UpdateBarber)
	admin.DELETE("/barbers/:id", adminH.DeleteBarber)
	admin.GET("/appointments", adminH.ListAppointments)
	admin.GET("/users", adminH.ListUsers)
	admin.PATCH("/users/:id/role", adminH.SetUserRole)

	return &App{Router: r, Tokens: tokens, Users: users}, nil
}

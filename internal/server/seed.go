package server

import (
	"context"
	"log/slog"

	"github.com/jcrlabs/barber-back/internal/domain"
	"github.com/jcrlabs/barber-back/internal/repository"
)

// SeedDemo crea el catálogo inicial de la barbería si está vacío: un único servicio (corte de 30 min)
// y un único barbero (Edgar) con su horario. Desde el panel de admin se ajusta todo después.
func SeedDemo(ctx context.Context, catalog *repository.CatalogRepository) error {
	empty, err := catalog.IsEmpty(ctx)
	if err != nil || !empty {
		return err
	}

	corte := domain.Service{Name: "Corte", Description: "Corte de pelo con Edgar.", DurationMin: 30, PriceCents: 1500, SortOrder: 1, Active: true}
	if err := catalog.CreateService(ctx, &corte); err != nil {
		return err
	}

	var hours []domain.WorkingHours
	for _, d := range []int{2, 3, 4, 5, 6} { // martes a sábado
		hours = append(hours, domain.WorkingHours{Weekday: d, StartMin: 10 * 60, EndMin: 14 * 60})
		if d != 6 {
			hours = append(hours, domain.WorkingHours{Weekday: d, StartMin: 16 * 60, EndMin: 20 * 60})
		}
	}
	edgar := domain.Barber{Name: "Edgar", Title: "Barbero", Hours: hours, Active: true}
	if err := catalog.CreateBarber(ctx, &edgar); err != nil {
		return err
	}
	slog.Info("initial catalog seeded")
	return nil
}

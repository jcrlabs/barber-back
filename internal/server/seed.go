package server

import (
	"context"
	"log/slog"

	"github.com/jcrlabs/barber-back/internal/domain"
	"github.com/jcrlabs/barber-back/internal/repository"
)

// SeedDemo crea una carta de servicios y un equipo de ejemplo si el catálogo está vacío.
func SeedDemo(ctx context.Context, catalog *repository.CatalogRepository) error {
	empty, err := catalog.IsEmpty(ctx)
	if err != nil || !empty {
		return err
	}

	services := []domain.Service{
		{Name: "Corte clásico", Description: "Tijera y máquina, lavado y peinado.", DurationMin: 30, PriceCents: 1800, SortOrder: 1},
		{Name: "Skin fade", Description: "Degradado a piel con acabado a navaja.", DurationMin: 45, PriceCents: 2200, SortOrder: 2},
		{Name: "Arreglo de barba", Description: "Perfilado, toalla caliente y aceite.", DurationMin: 20, PriceCents: 1200, SortOrder: 3},
		{Name: "Afeitado ritual", Description: "Navaja libre, toallas calientes y bálsamo.", DurationMin: 40, PriceCents: 2500, SortOrder: 4},
		{Name: "Corte + barba", Description: "El combo completo de la casa.", DurationMin: 60, PriceCents: 3000, SortOrder: 5},
	}
	for i := range services {
		services[i].Active = true
		if err := catalog.CreateService(ctx, &services[i]); err != nil {
			return err
		}
	}

	weekdays := func(start, end int, days ...int) []domain.WorkingHours {
		var out []domain.WorkingHours
		for _, d := range days {
			out = append(out, domain.WorkingHours{Weekday: d, StartMin: start, EndMin: end})
		}
		return out
	}
	split := append(weekdays(10*60, 14*60, 2, 3, 4, 5, 6), weekdays(16*60, 20*60+30, 2, 3, 4, 5)...)
	barbers := []domain.Barber{
		{Name: "Edgar", Title: "Fundador · Master barber", Bio: "Quince años con la navaja. Obsesionado con los fades limpios y el detalle.", Hours: split},
		{Name: "Lucía Ortega", Title: "Barba & afeitado", Bio: "Especialista en barbas y afeitado tradicional con toalla caliente.", Hours: weekdays(9*60+30, 17*60+30, 1, 2, 3, 4, 5)},
		{Name: "Dani Ruiz", Title: "Cortes modernos", Bio: "Texturas, crops y diseños a máquina.", Hours: weekdays(12*60, 20*60, 2, 3, 4, 5, 6)},
	}
	for i := range barbers {
		barbers[i].Active = true
		if err := catalog.CreateBarber(ctx, &barbers[i]); err != nil {
			return err
		}
	}
	slog.Info("demo catalog seeded", slog.Int("services", len(services)), slog.Int("barbers", len(barbers)))
	return nil
}

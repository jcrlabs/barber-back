# CLAUDE.md — barber-back

API de reservas de la barbería Navaja (Go + Gin + pgx + PostgreSQL).

## CI local

Ejecutar **antes de cada commit**:

```bash
gofmt -l .                      # no debe mostrar ficheros
go vet ./...
golangci-lint run --timeout=5m
go test -race ./...
TEST_DATABASE_URL=postgres://... go test -race -count=1 ./internal/server/...   # integración
go build -o /dev/null ./cmd/server
```

## Git

- Ramas: `feature/`, `bugfix/`, `hotfix/`, `release/` — sin prefijos adicionales
- Commits: convencional (`feat:`, `fix:`, `chore:`, etc.) — sin mencionar herramientas externas ni agentes en el mensaje
- PRs: título y descripción propios del cambio — sin mencionar herramientas externas ni agentes
- Comentarios y documentación: redactar en primera persona del equipo — sin atribuir autoría a herramientas

## Reglas del proyecto

- SQL siempre parametrizado (`$1, $2…`). En WHERE dinámicos solo se interpola el índice del placeholder.
- Las migraciones viven en `internal/database/migrations/NNNN_nombre.sql` y se aplican al arrancar (con advisory lock).
- Nunca relajar la restricción `EXCLUDE` de `appointments`: es la garantía contra dobles reservas.
- Secretos solo por variables de entorno; en el cluster, SealedSecret `barber-back-secrets` en `jcrlabs-infra`.

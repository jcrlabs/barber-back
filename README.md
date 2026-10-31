# barber-back — API de Edgar Barber

API de reservas para la barbería **Edgar Barber**: registro con email o Google/GitHub, catálogo de servicios y barberos, motor de disponibilidad y gestión de citas para clientes, barberos y administración.

**Stack:** Go 1.26 · Gin · pgx/v5 · PostgreSQL 17 · JWT RS256 · Helm · ArgoCD

## Arquitectura

```mermaid
flowchart LR
  U[Navegador] -->|HTTPS edgarbarber.jcrlabs.net| IN[ingress-nginx]
  IN --> FE[barber-front · nginx]
  FE -->|/api/*| BE[barber-back · Gin]
  BE --> PG[(PostgreSQL)]
  BE -->|OAuth2 + PKCE| G[Google / GitHub]
```

Capas (`internal/`):

| Paquete | Responsabilidad |
|---|---|
| `domain` | Entidades, roles, estados y errores de negocio |
| `repository` | Acceso a datos con pgx (SQL siempre parametrizado) |
| `services` | Casos de uso: auth, OAuth, motor de reservas |
| `handlers` | HTTP: validación de entrada y mapeo de errores |
| `middleware` | Rate limit por IP, JWT, CSRF, cabeceras de seguridad |
| `server` | Composición de dependencias y rutas |
| `database` | Pool y migraciones embebidas con advisory lock |

### Decisiones clave

- **Sin dobles reservas, ni con carreras**: `EXCLUDE USING gist (barber_id WITH =, tstzrange(starts_at, ends_at) WITH &&)` en Postgres. El servicio valida además que la hora sea exactamente un hueco calculado (alineado, en horario, con antelación mínima).
- **Sesiones**: access token JWT RS256 de 15 min (en memoria en el front) + refresh token opaco de 30 días en cookie `HttpOnly; Secure; SameSite=Strict`, rotado en cada uso. Reutilizar un refresh token revoca toda la familia de sesiones.
- **OAuth**: Authorization Code + PKCE (S256) y `state` en cookie efímera. Solo se aceptan emails verificados por el proveedor. Si alguien había registrado ese email con contraseña sin verificar, la contraseña se invalida al vincular (protección contra *pre-account hijacking*).
- **Admin**: `ADMIN_EMAILS` promueve a admin solo cuentas con email verificado. Alternativa operativa: `kubectl exec deploy/barber-back -n barber -- /bin/server promote-admin email@dominio`.
- **Zona horaria**: los horarios de barberos se guardan en minutos locales y se resuelven con `SHOP_TIMEZONE` (correcto en cambios de hora).

## API (`/api/v1`)

| Método | Ruta | Acceso | Límite |
|---|---|---|---|
| POST | `/auth/register` | público | 3/h por IP |
| POST | `/auth/login` | público | 10/15 min |
| POST | `/auth/refresh` · `/auth/logout` | cookie + `X-Requested-With: navaja` | 60/15 min |
| GET | `/auth/providers` | público | |
| GET | `/auth/oauth/{google,github}/start` · `/callback` | público | 20/15 min |
| GET/PATCH | `/auth/me` · POST `/auth/me/password` · `/auth/logout-all` | usuario | |
| GET | `/meta` · `/services` · `/barbers` | público | |
| GET | `/availability?service_id&date[&barber_id]` | público | |
| GET | `/availability/days?service_id[&barber_id&from&days]` | público | |
| POST | `/appointments` · GET `/appointments/mine` · POST `/appointments/:id/cancel` | cliente | 20/h reservas |
| GET | `/staff/agenda?from&to` · POST `/staff/appointments/:id/status` | barbero/admin | |
| * | `/admin/{stats,services,barbers,appointments,users}` | admin | |

Límite global: 200 peticiones/min por IP en `/api`.

## Desarrollo local

```bash
cp .env.example .env
make keys                      # clave RSA en .keys/ (ignorada por git)
make run                       # requiere Postgres en DATABASE_URL
make test                      # unitarios
TEST_DATABASE_URL=postgres://user:pass@localhost:5432/barber_test?sslmode=disable make test-integration
```

Con `SEED_DEMO_DATA=true` se crea una carta y un equipo de ejemplo si el catálogo está vacío.

### OAuth

- **Google** → Google Cloud Console › Credenciales › ID de cliente OAuth (aplicación web). URI de redirección: `https://edgarbarber.jcrlabs.net/api/v1/auth/oauth/google/callback`.
- **GitHub** → Settings › Developer settings › OAuth Apps. Callback: `https://edgarbarber.jcrlabs.net/api/v1/auth/oauth/github/callback`.

Si un proveedor no tiene credenciales, simplemente no se ofrece en el login.

## Despliegue

- `develop` → imagen `develop-<sha>` → `values-test.yaml` → ArgoCD `barber-back-test` (namespace `barber-test`).
- tag `vX.Y.Z` → imagen `vX.Y.Z` → `values-prod.yaml` en `main` → ArgoCD `barber-back` (namespace `barber`).

Secretos en `jcrlabs-infra` como SealedSecret `barber-back-secrets` (`DB_USER`, `DB_PASSWORD`, `JWT_PRIVATE_KEY`, `ADMIN_EMAILS`, `GOOGLE_*`, `GITHUB_*`), generados con `scripts/seal-barber-secrets.sh`.

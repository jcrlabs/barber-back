-- Esquema inicial de Navaja (reservas de barbería)
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE users (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email          text NOT NULL,
    name           text NOT NULL,
    phone          text NOT NULL DEFAULT '',
    avatar_url     text NOT NULL DEFAULT '',
    password_hash  text,
    role           text NOT NULL DEFAULT 'client' CHECK (role IN ('client', 'barber', 'admin')),
    email_verified boolean NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_email_lower_key ON users (lower(email));

CREATE TABLE oauth_identities (
    provider   text NOT NULL,
    subject    text NOT NULL,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    email      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, subject)
);
CREATE INDEX oauth_identities_user_idx ON oauth_identities (user_id);

CREATE TABLE refresh_tokens (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    family_id  uuid NOT NULL,
    token_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX refresh_tokens_user_idx ON refresh_tokens (user_id);
CREATE INDEX refresh_tokens_family_idx ON refresh_tokens (family_id);

CREATE TABLE services (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text NOT NULL,
    description  text NOT NULL DEFAULT '',
    duration_min integer NOT NULL CHECK (duration_min BETWEEN 5 AND 480),
    price_cents  integer NOT NULL CHECK (price_cents >= 0),
    active       boolean NOT NULL DEFAULT true,
    sort_order   integer NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE barbers (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid UNIQUE REFERENCES users (id) ON DELETE SET NULL,
    name       text NOT NULL,
    title      text NOT NULL DEFAULT '',
    bio        text NOT NULL DEFAULT '',
    avatar_url text NOT NULL DEFAULT '',
    active     boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Franjas de trabajo semanales (minutos desde medianoche, hora local de la barbería)
CREATE TABLE barber_hours (
    barber_id uuid NOT NULL REFERENCES barbers (id) ON DELETE CASCADE,
    weekday   smallint NOT NULL CHECK (weekday BETWEEN 0 AND 6),
    start_min integer NOT NULL CHECK (start_min >= 0),
    end_min   integer NOT NULL CHECK (end_min <= 1440),
    CHECK (start_min < end_min),
    PRIMARY KEY (barber_id, weekday, start_min)
);

CREATE TABLE appointments (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    barber_id    uuid NOT NULL REFERENCES barbers (id) ON DELETE RESTRICT,
    service_id   uuid NOT NULL REFERENCES services (id) ON DELETE RESTRICT,
    starts_at    timestamptz NOT NULL,
    ends_at      timestamptz NOT NULL,
    status       text NOT NULL DEFAULT 'confirmed'
                 CHECK (status IN ('confirmed', 'completed', 'cancelled', 'no_show')),
    price_cents  integer NOT NULL,
    notes        text NOT NULL DEFAULT '',
    cancelled_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (ends_at > starts_at),
    -- La base de datos garantiza que un barbero nunca tenga dos citas solapadas,
    -- incluso con peticiones concurrentes desde varias réplicas.
    CONSTRAINT appointments_barber_no_overlap EXCLUDE USING gist (
        barber_id WITH =, tstzrange(starts_at, ends_at, '[)') WITH &&
    ) WHERE (status IN ('confirmed', 'completed')),
    CONSTRAINT appointments_client_no_overlap EXCLUDE USING gist (
        client_id WITH =, tstzrange(starts_at, ends_at, '[)') WITH &&
    ) WHERE (status = 'confirmed')
);
CREATE INDEX appointments_barber_start_idx ON appointments (barber_id, starts_at);
CREATE INDEX appointments_client_start_idx ON appointments (client_id, starts_at DESC);
CREATE INDEX appointments_start_idx ON appointments (starts_at);

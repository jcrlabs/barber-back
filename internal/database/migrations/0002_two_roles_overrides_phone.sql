-- Dos roles (cliente y admin), excepciones de horario por día y teléfono de contacto en cada cita.
UPDATE users SET role = 'client' WHERE role = 'barber';
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (role IN ('client', 'admin'));

ALTER TABLE appointments ADD COLUMN client_phone text NOT NULL DEFAULT '';

-- Excepciones al horario semanal para un día concreto.
--   closed: no se trabaja ese día.
--   open:   horario especial; sustituye al semanal de ese día.
--   block:  bloquea una franja (descanso, recado...).
-- barber_id NULL aplica a toda la barbería.
CREATE TABLE schedule_overrides (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    barber_id  uuid REFERENCES barbers (id) ON DELETE CASCADE,
    day        date NOT NULL,
    kind       text NOT NULL CHECK (kind IN ('closed', 'open', 'block')),
    start_min  integer,
    end_min    integer,
    note       text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (
        (kind = 'closed' AND start_min IS NULL AND end_min IS NULL)
        OR (kind <> 'closed' AND start_min >= 0 AND end_min <= 1440 AND start_min < end_min)
    )
);
CREATE INDEX schedule_overrides_day_idx ON schedule_overrides (day);

CREATE INDEX appointments_created_idx ON appointments (created_at DESC);

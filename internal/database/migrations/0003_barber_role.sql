-- Rol de barbero: cada barbero enlazado a una cuenta gestiona su horario y recibe sus propios avisos.
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (role IN ('client', 'barber', 'admin'));

-- Canales de aviso por barbero. El topic de ntfy es aleatorio y privado por defecto.
ALTER TABLE barbers
    ADD COLUMN notify_email   text NOT NULL DEFAULT '',
    ADD COLUMN whatsapp_phone text NOT NULL DEFAULT '',
    ADD COLUMN callmebot_key  text NOT NULL DEFAULT '',
    ADD COLUMN ntfy_topic     text NOT NULL DEFAULT ('edgarbarber-' || substr(md5(gen_random_uuid()::text), 1, 20));
CREATE UNIQUE INDEX barbers_ntfy_topic_idx ON barbers (ntfy_topic);

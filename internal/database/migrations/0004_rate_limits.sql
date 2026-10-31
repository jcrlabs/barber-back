-- Intentos para los límites que deben compartir todas las réplicas (login, registro, bloqueo por cuenta...).
-- UNLOGGED: es un dato efímero; perderlo en un reinicio de Postgres solo resetea los contadores.
CREATE UNLOGGED TABLE rate_limit_hits (
    bucket text        NOT NULL,
    key    text        NOT NULL,
    at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX rate_limit_hits_key_idx ON rate_limit_hits (bucket, key, at);

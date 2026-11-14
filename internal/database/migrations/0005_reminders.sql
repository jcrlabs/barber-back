-- Recordatorio por email al cliente poco antes de su cita.
-- reminder_sent_at se marca al reclamar la cita: con varias réplicas solo una lo envía.
ALTER TABLE appointments ADD COLUMN IF NOT EXISTS reminder_sent_at timestamptz;
CREATE INDEX IF NOT EXISTS appointments_reminder_due_idx ON appointments (starts_at)
    WHERE status = 'confirmed' AND reminder_sent_at IS NULL;

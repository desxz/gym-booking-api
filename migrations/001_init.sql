CREATE TABLE clubs (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    club_id       UUID NOT NULL REFERENCES clubs (id),
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL CHECK (role IN ('member', 'trainer')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE classes (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    club_id      UUID NOT NULL REFERENCES clubs (id),
    trainer_id   UUID NOT NULL REFERENCES users (id),
    title        TEXT NOT NULL,
    starts_at    TIMESTAMPTZ NOT NULL,
    capacity     INT NOT NULL CHECK (capacity > 0),
    booked_count INT NOT NULL DEFAULT 0 CHECK (booked_count >= 0 AND booked_count <= capacity),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE bookings (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    club_id    UUID NOT NULL REFERENCES clubs (id),
    class_id   UUID NOT NULL REFERENCES classes (id),
    user_id    UUID NOT NULL REFERENCES users (id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (class_id, user_id)
);

CREATE TABLE refresh_tokens (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    family_id   UUID NOT NULL,
    user_id     UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    club_id     UUID NOT NULL REFERENCES clubs (id),
    token_hash  TEXT NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ NOT NULL,
    revoked_at  TIMESTAMPTZ,
    replaced_by UUID REFERENCES refresh_tokens (id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE idempotency_keys (
    club_id       UUID NOT NULL REFERENCES clubs (id),
    user_id       UUID NOT NULL REFERENCES users (id),
    key           TEXT NOT NULL,
    request_hash  TEXT NOT NULL,
    status_code   INT NOT NULL,
    response_body JSONB NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (club_id, user_id, key)
);

CREATE INDEX idx_classes_club_starts ON classes (club_id, starts_at);
CREATE INDEX idx_bookings_user ON bookings (user_id);
CREATE INDEX idx_refresh_tokens_family ON refresh_tokens (family_id);

-- Demo data. Every account's password is Password123! (bcrypt cost 12).
INSERT INTO clubs (id, name) VALUES
    ('11111111-1111-1111-1111-111111111111', 'Apex Fitness Amsterdam'),
    ('22222222-2222-2222-2222-222222222222', 'Pulse Studio Rotterdam')
ON CONFLICT (id) DO NOTHING;

INSERT INTO users (id, club_id, email, password_hash, role) VALUES
    ('11111111-1111-1111-1111-111111111101', '11111111-1111-1111-1111-111111111111', 'trainer@club-a.test',
     '$2a$12$LPn6rV87rRQQAbu/ubXgg..01hovEJBEGbCsKILtzBOUtJmKFvGXa', 'trainer'),
    ('11111111-1111-1111-1111-111111111102', '11111111-1111-1111-1111-111111111111', 'member@club-a.test',
     '$2a$12$Xm8VYM/Gv76OhRQhZXGOEewTJTP.cPztfi2tS01nPYvS83xnqGI2G', 'member'),
    ('22222222-2222-2222-2222-222222222201', '22222222-2222-2222-2222-222222222222', 'member@club-b.test',
     '$2a$12$aNvriKVICBBuj7qBhfPIN.fmF2NasfgzFQKFNE8d4MEfUnmAKbo2i', 'member')
ON CONFLICT (email) DO NOTHING;

INSERT INTO classes (id, club_id, trainer_id, title, starts_at, capacity) VALUES
    ('33333333-3333-3333-3333-333333333301', '11111111-1111-1111-1111-111111111111',
     '11111111-1111-1111-1111-111111111101', 'Morning Strength', now() + interval '2 days', 2),
    ('33333333-3333-3333-3333-333333333302', '11111111-1111-1111-1111-111111111111',
     '11111111-1111-1111-1111-111111111101', 'Evening HIIT', now() + interval '3 days', 1)
ON CONFLICT (id) DO NOTHING;

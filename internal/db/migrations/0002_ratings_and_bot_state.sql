-- 0002_ratings_and_bot_state.sql — calificaciones y estado del long-poll.

-- Calificaciones. Estrictamente personales: la PK y todas las consultas van por
-- user_id, y ningún aprendizaje se comparte entre usuarios.
CREATE TABLE ratings (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    listing_id BIGINT NOT NULL REFERENCES listings (id) ON DELETE CASCADE,
    -- 1 = 👍, 0 = 👎
    label      SMALLINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, listing_id)
);

-- Estado del poller de Telegram. Una sola fila.
CREATE TABLE bot_state (
    id                    INT PRIMARY KEY CHECK (id = 1),
    -- Offset de getUpdates. Se persiste porque Telegram retiene los updates no
    -- confirmados 24 h: con el offset solo en memoria, cada reinicio reproduce
    -- onboardings y callbacks recientes.
    last_update_id        BIGINT NOT NULL DEFAULT 0,
    -- Lease del poller: Telegram entrega cada update a un solo caller, así que una
    -- instancia de dev con el mismo token le roba los updates a producción.
    poll_lease_holder     TEXT,
    poll_lease_expires_at TIMESTAMPTZ
);

INSERT INTO bot_state (id) VALUES (1) ON CONFLICT (id) DO NOTHING;

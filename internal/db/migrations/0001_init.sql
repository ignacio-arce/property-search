-- 0001_init.sql — esquema mínimo del camino vertical V1.

-- Usuarios. user_id es el from.id de Telegram (la persona); chat_id es el destino
-- de entrega (la conversación). En chats privados coinciden, y solo se admite
-- onboarding en privados.
CREATE TABLE users (
    user_id              BIGINT PRIMARY KEY,
    chat_id              BIGINT NOT NULL,
    state                TEXT NOT NULL DEFAULT 'idle',
    state_data           JSONB,
    onboarded_at         TIMESTAMPTZ,
    settings             JSONB NOT NULL DEFAULT '{}',
    -- Interruptor de notificación diaria. El digest solo recorre active = true;
    -- el operador lo prende a mano:
    --   UPDATE users SET active = true WHERE chat_id = <id>;
    active               BOOLEAN NOT NULL DEFAULT false,
    active_model_version INT NOT NULL DEFAULT 0,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE search_urls (
    id                BIGSERIAL PRIMARY KEY,
    user_id           BIGINT NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    label             TEXT NOT NULL,
    url               TEXT NOT NULL,
    -- Forma canónica: host en minúsculas, query ordenado y params de tracking
    -- fuera. La unicidad va sobre esto para que la misma búsqueda pegada desde
    -- otra página no se registre dos veces.
    url_norm          TEXT NOT NULL,
    sort_injected     BOOLEAN NOT NULL DEFAULT false,
    -- pending | valid | valid_empty | failed | invalid
    validation_status TEXT NOT NULL DEFAULT 'pending',
    attempts          INT NOT NULL DEFAULT 0,
    next_check_at     TIMESTAMPTZ,
    last_fetch_status INT,
    last_card_count   INT,
    last_checked_at   TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, url_norm),
    UNIQUE (user_id, label)
);

CREATE TABLE listings (
    id                BIGSERIAL PRIMARY KEY,
    -- data-id de la tarjeta. NO un hash de la URL: los hrefs traen n_pg/n_pos y
    -- cambian cuando la publicación se mueve de puesto.
    zonaprop_id       TEXT NOT NULL UNIQUE,
    canonical_url     TEXT NOT NULL,
    -- Claves de PARTICIÓN del modelo, no features de scoring (ver el spec).
    operation_type    TEXT,
    currency          TEXT,
    features          JSONB NOT NULL DEFAULT '{}',
    -- Posición ordinal en el fetch ordenado por recencia al verse por primera vez
    -- (1 = la más nueva). Ni zonaprop_id ni id sirven como proxy de recencia.
    recency_rank      INT,
    first_indexed_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_indexed_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Sin esto, "candidatas = NOT EXISTS(deliveries)" sobre listings global le filtra a
-- un usuario el inventario de otro.
CREATE TABLE listing_sources (
    listing_id    BIGINT NOT NULL REFERENCES listings (id) ON DELETE CASCADE,
    search_url_id BIGINT NOT NULL REFERENCES search_urls (id) ON DELETE CASCADE,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (listing_id, search_url_id)
);

-- Fuente de verdad de la entrega: se escribe al enviar, no al calificar, así una
-- publicación que el usuario ignora no se le vuelve a mandar.
CREATE TABLE deliveries (
    user_id       BIGINT NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    listing_id    BIGINT NOT NULL REFERENCES listings (id) ON DELETE CASCADE,
    message_id    BIGINT,
    position      INT,
    score         REAL,
    model_version INT NOT NULL DEFAULT 0,
    -- Snapshot al enviar: el modelo entrena con lo que el usuario vio, no con lo
    -- que mutó después.
    features      JSONB NOT NULL DEFAULT '{}',
    status        TEXT NOT NULL DEFAULT 'pending',
    attempts      INT NOT NULL DEFAULT 0,
    sent_at       TIMESTAMPTZ,
    PRIMARY KEY (user_id, listing_id)
);

-- Settings globales con defaults. ON CONFLICT DO NOTHING en el seed: los ajustes
-- que el operador cambie sobreviven a los reinicios.
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

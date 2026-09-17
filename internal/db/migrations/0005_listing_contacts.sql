-- 0005_listing_contacts.sql — datos de contacto extraidos del detalle.

-- Se cachean porque cada extraccion cuesta un request contra el presupuesto
-- escaso, y dos usuarios pueden dar 👍 a la misma publicacion.
-- fetched_at existe para poder vencer el dato: un telefono de hace meses puede
-- estar desafectado.
CREATE TABLE listing_contacts (
    listing_id      BIGINT PRIMARY KEY REFERENCES listings (id) ON DELETE CASCADE,
    phone           TEXT,
    email           TEXT,
    -- Campos que el JSON-LD del detalle trae estructurados y el card no.
    street_address  TEXT,
    neighbourhood   TEXT,
    fetched_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

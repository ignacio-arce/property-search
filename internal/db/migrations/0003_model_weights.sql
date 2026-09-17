-- 0003_model_weights.sql — pesos del modelo, por usuario y versionados.

-- Una fila por (usuario, feature, valor, versión). La clave incluye el VALOR: una
-- tasa de like es por bucket, no por feature. Con (user_id, feature) a secas todos
-- los valores de un feature colapsan en un solo número igual al promedio global y
-- el orden se vuelve una moneda al aire.
--
-- La versión permite reentrenar sin que el scorer lea pesos a medio escribir: se
-- escribe la versión nueva en una transacción y se voltea users.active_model_version
-- dentro de la misma.
CREATE TABLE model_weights (
    user_id       BIGINT NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    feature       TEXT NOT NULL,
    value         TEXT NOT NULL,
    ups           INT NOT NULL DEFAULT 0,
    downs         INT NOT NULL DEFAULT 0,
    model_version INT NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, feature, value, model_version)
);

CREATE INDEX model_weights_active
    ON model_weights (user_id, model_version);

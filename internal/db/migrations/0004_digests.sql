-- 0004_digests.sql — estado de los envíos diarios, por usuario y fecha.

-- Existe para que un dia interrumpido se reanude en vez de perderse: la fila se
-- crea al agendar, y el boot procesa lo que quedo sin terminar.
--
-- Nota: el insumo del digest son "las publicaciones no entregadas", no "las de
-- hoy", asi que reanudar un dia es simplemente seguir mandando lo que falta.
CREATE TABLE digests (
    user_id     BIGINT NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    run_date    DATE NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending', -- pending|running|done|error
    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    sent        INT NOT NULL DEFAULT 0,
    error       TEXT,
    PRIMARY KEY (user_id, run_date)
);

-- Para encontrar rapido lo que quedo sin terminar al arrancar.
CREATE INDEX digests_unfinished ON digests (status) WHERE status <> 'done';

-- 0006_users_active_default_true.sql — los usuarios nuevos nacen activos.
--
-- En pruebas se quiere que todo usuario nuevo reciba el digest sin que el
-- operador prenda el interruptor a mano:
--   UPDATE users SET active = true WHERE chat_id = <id>;
-- Solo cambia el DEFAULT; las filas existentes no se tocan. El switch manual
-- sigue existiendo para apagar a alguien puntualmente.
ALTER TABLE users ALTER COLUMN active SET DEFAULT true;

# Visibilidad operativa del bot (logs con niveles)

One-pager de idea. Refinado con `idea-refine`.

## Problem Statement

How might we dejar de estar ciegos sobre **qué está haciendo el bot en cada operación**, de
modo que un día sin envíos, un fetch bloqueado o una alta de usuario se expliquen en un minuto
desde `docker compose logs`, sin inundar la consola ni filtrar datos personales?

## Recommended Direction

Migrar el logging a `log/slog` con un `TextHandler` legible sobre stdout y un nivel configurable
por `LOG_LEVEL` (default `info`). La regla de oro es **una línea resumen por operación en INFO**, y
que el detalle por ítem y las URLs completas vivan en DEBUG, apagado por defecto. Así el operador ve
hitos y degradaciones sin ruido, y puede subir a DEBUG para un caso puntual.

Las tres operaciones a instrumentar con embudo/resumen son: el **digest por usuario**
(candidatas→dedup→nuevas→enviadas→diferidas, con motivo del tope), la **validación**
(checked/valid/empty/invalid + por qué falló), y el **onboarding** (`/start`, `/addurl`,
activación). El **fetch** suma WARN en `challenge`, entrada a `cooldown` y reintentos, que es lo
único que responde "¿me está bloqueando Cloudflare?"; el detalle de pacing y modo queda en DEBUG.
Se usan campos (`user=`, `mode=`, `ms=`) para que sea greppable sin ser JSON.

El `slog` default ya imprime time con milisegundos, lo que habilita el timing sin tocar el formato.
Los mensajes se mantienen en el estilo actual (prefijo por dominio, minúsculas) para no romper
búsquedas existentes; los comentarios siguen en español.

## Key Assumptions to Validate

- [ ] Una línea INFO resumen por operación alcanza para reconstruir un caso. *Test: un día real;
      responder las 4 preguntas sin tocar la DB.*
- [ ] El ruido de DEBUG es tolerable porque es opt-in. *Test: `LOG_LEVEL=debug` un ciclo y contar
      líneas.*
- [ ] No hace falta `run_id`: cada etapa emite su propio resumen. *Test: explicar un no-envío solo
      con INFO.*
- [ ] La migración a `slog` no rompe los tests que hoy inyectan `log.New(io.Discard, ...)` (digest,
      validate, chat, contact, db). *Test: `go test ./...` verde.*

## MVP Scope

**In:**

- Logger central `slog` con `LOG_LEVEL` (debug/info/warn/error) validado en `config.Load`;
  `slog.SetDefault` en `main`.
- Reemplazo de `Logger *log.Logger` por `*slog.Logger` en `digest.Runner`, `validate.Validator`,
  `chat.Poller`, `contact.Extractor` y `db.Options` (más tests).
- Resumen INFO al cerrar: digest por usuario, corrida de validación, alta/baja de URL y `/start`.
- Fetch: WARN en `challenge`/`cooldown`/retry; DEBUG con modo, wait de pacing y URL.
- IDs (`user`, `chat`) en INFO; URL completa sólo en DEBUG.

**Out:**

- Salida JSON estructurada.
- Métricas, health endpoint o alertas activas.
- Correlación por `run_id`.
- Rotación/agregación de archivos (sigue stdout + `docker logs`).

## Not Doing (and Why)

- **JSON logs** — descartaste el consumo por herramientas; el `TextHandler` con campos ya es
  greppable.
- **Métricas/health endpoint** — es otra feature; los logs responden las 4 preguntas por ahora.
- **`run_id` por corrida** — sin JSON el costo de ruido supera el beneficio; el resumen por
  operación ya ubica el contexto.
- **DEBUG por listing en INFO** — 30 tarjetas × 5 búsquedas × N usuarios ahogan la señal.
- **Loguear URLs en INFO** — son datos de búsqueda del usuario; quedan en DEBUG igual que el token
  se mantiene redactado.

## Open Questions

- **¿`/borrardatos` y los logs?** Los logs ya emitidos no se borran; hay que decidir si los IDs en
  logs entran en la política de datos personales (fuera del MVP, pero a documentar).
- **¿Resumen diario agregado (todos los usuarios) además del por-usuario?** Útil para "cuánto se
  evaluó hoy"; se puede derivar de las líneas por usuario con grep. Diferido.
- **¿`LOG_LEVEL=debug` en producción desde compose o sólo local?** Default `info`; DEBUG documentado
  como diagnóstico temporal.

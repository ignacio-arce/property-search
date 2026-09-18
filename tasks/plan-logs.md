# Implementation Plan: Visibilidad operativa del bot (logs con niveles)

## Overview

Migrar el logging del bot de `*log.Logger` de texto plano a `log/slog` con `TextHandler` legible,
niveles (`LOG_LEVEL`, default `info`) y **una línea resumen por operación en INFO**. El detalle por
ítem y las URLs completas quedan en DEBUG, apagado por defecto. Objetivo: poder responder desde
`docker compose logs` (1) por qué no le llegó a un usuario, (2) cuánto se evaluó/envió hoy, (3) si el
fetch está bloqueado, y (4) cuánto tarda cada etapa, sin inundar la consola ni filtrar datos
personales. La idea y sus trade-offs están en `docs/ideas/logs-bot.md`.

## Architecture Decisions

- **`log/slog` + `TextHandler` sobre stdout**, no JSON. Decisión del usuario: los logs se leen a ojo
  con `docker compose logs -f`. El handler imprime `time` con milisegundos, lo que habilita timing
  sin cambiar el formato.
- **Niveles semánticos.** `INFO` = hitos y resúmenes de operación; `WARN` = degradación manejada
  (challenge, reintento, notify fallido, dead-letter, URL inválida, canary de cero tarjetas);
  `ERROR` reservado para fatal; `DEBUG` = detalle por ítem, stat de parseo, modo/pacing/URL de fetch.
- **Migración incremental por paquete** para que cada tarea deje el build y los tests verdes. En la
  transición, `main` mantiene un logger legacy `*log.Logger` para los paquetes aún no migrados; se
  elimina al terminar la migración (Task 7).
- **`fetch.New` con opciones variádicas** (`fetch.WithLogger`) en vez de cambiar la firma: los
  llamadores existentes (`cmd/probe`, `e2e_test`, tests de fetch) siguen compilando sin cambios.
- **Privacidad.** `user`/`chat`/`label` en INFO; la URL completa de búsqueda solo en DEBUG. El token
  de Telegram sigue redactado por `httpx.Redact`; no se toca.
- **Convención de campos:** `user`, `chat`, `label`, `url` (solo DEBUG), `mode`, `status`, `ms`,
  `searches`, `fetched`, `candidates`, `sent`, `checked`, `valid`, `empty`, `invalid`, `retrying`,
  `attempt`, `backoff`. Mensajes en el estilo actual (prefijo por dominio, minúsculas).

## Task List

### Phase 0: Foundation

- [ ] Task 1: Paquete `internal/logging` (`New`, `Discard`) + test
- [ ] Task 2: `LOG_LEVEL` en config + `.env.example` + test

### Checkpoint: Foundation

- [ ] `nix develop -c go test ./...` verde; `LOG_LEVEL=bogus` rechazado con error claro

### Phase 1: Wire the core

- [ ] Task 3: `main` + `db.Options` + `scheduler` sobre slog (logger legacy temporal)
- [ ] Task 4: `digest` → slog + línea resumen por usuario
- [ ] Task 5: `validate` → slog + resumen por corrida

### Checkpoint: Core

- [ ] Con `LOG_LEVEL=info` se ve el embudo del digest por usuario en stdout
- [ ] `nix develop -c go build ./...` y `go vet ./...` limpios

### Phase 2: Interactive + fetch

- [ ] Task 6: `chat` → slog + eventos INFO de onboarding y rating
- [ ] Task 7: `contact` → slog; se elimina el logger legacy de `main`
- [ ] Task 8: instrumentar `fetch`/`Gate` con `WithLogger` (WARN bloqueo/cooldown/retry; DEBUG modo/pacing/URL)
- [ ] Task 9: wire del logger de fetch en `cmd/bot` y `cmd/probe`

### Checkpoint: Complete

- [ ] `LOG_LEVEL=debug` muestra modo/pacing/URL; `info` muestra WARN de bloqueo/cooldown
- [ ] Suite completa verde y drill manual documentado

## Dependency Graph

```
Task 1 (logging) ─┐
Task 2 (config) ──┴─> Task 3 (main/db/scheduler)
                            ├─> Task 4 (digest)
                            ├─> Task 5 (validate)
                            ├─> Task 6 (chat)
                            ├─> Task 7 (contact → borra legacy)
                            └─> Task 9 (wire fetch) ──> Task 8 (fetch/gate) ─> Task 1
Task 10 (docs) depende de todo.
```

Tasks 4–7 tocan `cmd/bot/main.go` por el wiring: **no paralelizar** entre sí sin coordinación.
Task 8 (paquete `fetch`) es independiente de main hasta Task 9.

## Migration and Coexistence

`main` arranca con `logger := logging.New(os.Stdout, cfg.LogLevel)`. Mientras `digest`, `validate`,
`chat` y `contact` sigan aceptando `*log.Logger`, `main` conserva
`legacy := log.New(os.Stdout, "", log.LstdFlags)` y se lo pasa a esos campos. Cada tarea de
migración cambia el campo del paquete a `*slog.Logger`, actualiza su test y actualiza el call site en
`main`. Al cerrar Task 7, `legacy` queda sin usos y se elimina.

## Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| La migración a slog rompe tests que inyectan `log.New(io.Discard,...)` | Medio | Migración por paquete; `logging.Discard()` reemplaza el patrón; correr la suite en cada task |
| Ruido de DEBUG en producción | Medio | Default `info`; DEBUG documentado como diagnóstico temporal y URL solo ahí |
| Datos personales (`user`/`chat`/URL) en logs | Medio | IDs en INFO, URL en DEBUG; `/borrardatos` vs logs queda como Open Question |
| Logger nil en tests de `fetch`/`Gate` | Bajo | Helpers `logf` nil-safe en `fetch` y `Gate` |
| Doble logger (slog + legacy) durante la migración confunde | Bajo | Ventana acotada a este plan; se elimina en Task 7 |

## Open Questions

- **`/borrardatos` y logs ya emitidos.** No se pueden borrar; decidir si los IDs en logs entran en la
  política de datos personales (fuera de este MVP, documentar).
- **`LOG_LEVEL=debug` en producción.** ¿Se habilita desde compose o solo local? Default `info`.
- **Resumen diario agregado (todos los usuarios).** Se deriva por grep de las líneas por usuario;
  diferido.

# TODO — Visibilidad operativa del bot (logs con niveles)

Lista ejecutable del plan `tasks/plan-logs.md`. Cada tarea tiene acceptance criteria, verificación,
archivos y alcance. El racional de diseño está en `tasks/plan-logs.md`; la idea en
`docs/ideas/logs-bot.md`.

> Este plan es independiente del plan v2 (`tasks/plan.md` / `tasks/todo.md`), que queda intacto.

**Comandos del repo:** `nix develop -c go build ./...` · `nix develop -c go test ./...` ·
`nix develop -c go vet ./...` · `make fmt vet test` · `make run` (dry-run).

**Regla de slicing:** cada tarea deja el build y la suite **verdes**. Si una tarea toca más de ~5
archivos, se parte antes de empezar.

---

## Phase 0: Foundation

### Task 1: Paquete `internal/logging` (`New`, `Discard`) + test
**Descripción:** Punto único de construcción del logger `slog`. `New(w, level)` devuelve un
`*slog.Logger` con `TextHandler` legible (time con ms); `Discard()` devuelve uno que descarta, para
reemplazar el patrón `log.New(io.Discard, "", 0)` de los tests.

**Acceptance criteria:**
- [ ] `logging.New(os.Stdout, slog.LevelInfo)` produce líneas `time=... level=INFO msg=...`
- [ ] `logging.Discard()` no escribe a ningún lado y no es nil
- [ ] El level filtra: con `slog.LevelWarn`, un `Info` no se emite y un `Warn` sí

**Verificación:**
- [ ] `nix develop -c go test ./internal/logging/`
- [ ] `nix develop -c go build ./...`

**Dependencias:** Ninguna

**Archivos probables:**
- `internal/logging/logging.go`
- `internal/logging/logging_test.go`

**Alcance:** S (2 archivos)

---

### Task 2: `LOG_LEVEL` en config + `.env.example` + test
**Descripción:** Leer `LOG_LEVEL` del entorno con `slog.Level.UnmarshalText` (acepta
`debug|info|warn|error`, case-insensitive), default `info`, y rechazar un valor inválido con error
claro al arrancar. Documentar la variable en `.env.example`.

**Acceptance criteria:**
- [ ] `LOG_LEVEL` ausente deja `cfg.LogLevel == slog.LevelInfo`
- [ ] `LOG_LEVEL=debug` setea `slog.LevelDebug`; `LOG_LEVEL=error` setea `slog.LevelError`
- [ ] Un valor inválido (`LOG_LEVEL=verbose`) hace fallar `config.Load` con mensaje que nombra la variable
- [ ] `.env.example` documenta `LOG_LEVEL` con su default

**Verificación:**
- [ ] `nix develop -c go test ./internal/config/`
- [ ] `nix develop -c go build ./...`

**Dependencias:** Ninguna (puede ir en paralelo con Task 1)

**Archivos probables:**
- `internal/config/config.go`
- `internal/config/config_test.go`
- `.env.example`

**Alcance:** S (3 archivos)

---

## Checkpoint: Foundation
- [ ] `nix develop -c go test ./...` verde
- [ ] `nix develop -c go build ./...` verde
- [ ] `LOG_LEVEL=verbose nix develop -c go run ./cmd/bot` falla nombrando `LOG_LEVEL`

---

## Phase 1: Wire the core

### Task 3: `main` + `db.Options` + `scheduler` sobre slog (logger legacy temporal)
**Descripción:** Construir el logger con `logging.New(os.Stdout, cfg.LogLevel)`. Cambiar
`db.Options.Logger` y el parámetro de `scheduler.Run` a `*slog.Logger`, y migrar los logs del propio
`main`. Mientras los demás paquetes no estén migrados, `main` conserva un `legacy := log.New(...)`
que solo se pasa a `digest`/`validate`/`chat`/`contact` (se borra en Task 7).

**Acceptance criteria:**
- [ ] `main` arranca con `logging.New` y `slog` es el logger de `main`, `db` y `scheduler`
- [ ] Los logs de arranque (`bot: flaresolverr=...`, `digest: daily at ...`, `retrain user ...`) siguen saliendo, ahora con nivel
- [ ] `db.Open` reporta reintentos con `Info`
- [ ] El scheduler reporta el próximo digest con `Info`
- [ ] `go vet` sin nuevos avisos

**Verificación:**
- [ ] `nix develop -c go test ./internal/db/ ./internal/scheduler/`
- [ ] `nix develop -c go build ./...`
- [ ] Manual: `make run` (dry-run) imprime arranque con formato `time=... level=INFO`

**Dependencias:** Task 1, Task 2

**Archivos probables:**
- `cmd/bot/main.go`
- `internal/db/db.go`
- `internal/scheduler/scheduler.go`
- `internal/scheduler/scheduler_test.go`

**Alcance:** M (4 archivos)

---

### Task 4: `digest` → slog + línea resumen por usuario
**Descripción:** Migrar `Runner.Logger` a `*slog.Logger` y agregar la línea INFO de cierre por
usuario con el embudo: `searches`, `fetched`, `candidates`, `sent`, `cap_hit`, `ms`. Bajar el detalle
(stat de parseo por búsqueda, URL) a DEBUG; subir a WARN los fallos manejados (búsqueda que falla,
notify fallido, no se pudo registrar stats) y a INFO los hitos (baseline, sin búsquedas válidas).

**Acceptance criteria:**
- [ ] Al terminar `RunForUser`, se emite **una** línea INFO con `user=`, `searches=`, `fetched=`, `candidates=`, `sent=`, `ms=`
- [ ] `cap_hit=true` cuando `candidates == MaxPerRun`
- [ ] Un fallo de fetch por búsqueda es WARN con `label`; la URL solo aparece en DEBUG
- [ ] `candidates` sale de `sendUndelivered` (se amplía su retorno), sin queries nuevas
- [ ] Un test con logger a `bytes.Buffer` verifica que la línea contiene los conteos correctos

**Verificación:**
- [ ] `nix develop -c go test ./internal/digest/`
- [ ] `nix develop -c go build ./...`

**Dependencias:** Task 3

**Archivos probables:**
- `internal/digest/digest.go`
- `internal/digest/digest_test.go`
- `internal/digest/e2e_test.go`
- `cmd/bot/main.go` (pasar `logger` y quitar `legacy` de este call site)

**Alcance:** M (4 archivos)

---

### Task 5: `validate` → slog + resumen por corrida
**Descripción:** Migrar `Validator.Logger` a `*slog.Logger` y emitir, al cerrar `RunOnce` con al
menos una URL, una línea INFO: `checked`, `valid`, `empty`, `invalid`, `retrying`, `ms`. Errores de
fetch/notify a WARN, resultado por URL a DEBUG (label en INFO, URL solo DEBUG), canary de cero
tarjetas a WARN.

**Acceptance criteria:**
- [ ] Al menos una URL chequeada ⇒ **una** línea INFO con `checked=`, `valid=`, `empty=`, `invalid=`, `retrying=`, `ms=`
- [ ] Sin URLs pendientes no se emite resumen (sigue silencioso)
- [ ] El canary "todos vacíos" es WARN
- [ ] Un test con logger a `bytes.Buffer` verifica los conteos del resumen

**Verificación:**
- [ ] `nix develop -c go test ./internal/validate/`
- [ ] `nix develop -c go build ./...`

**Dependencias:** Task 3

**Archivos probables:**
- `internal/validate/validator.go`
- `internal/validate/validator_test.go`
- `cmd/bot/main.go`

**Alcance:** S/M (3 archivos)

---

## Checkpoint: Core
- [ ] Con `LOG_LEVEL=info`, un ciclo de digest deja el embudo por usuario en stdout
- [ ] `nix develop -c go build ./...` y `nix develop -c go vet ./...` limpios

---

## Phase 2: Interactive + fetch

### Task 6: `chat` → slog + eventos INFO de onboarding y rating
**Descripción:** Migrar `Poller.Logger` a `*slog.Logger`. Emitir INFO en hitos de onboarding (`/start`
con `new=true|false`, alta de búsqueda con `label`, baja con `removed=`, `/stop`, `/borrardatos`) y
WARN en fallos/dead-letter/getUpdates. El rating y el contacto quedan INFO con `user`/`listing`.

**Acceptance criteria:**
- [ ] `/start` de usuario nuevo emite INFO con `user=` y `new=true`; si ya existía, `new=false`
- [ ] Alta exitosa de búsqueda emite INFO con `user=`, `label=` (URL no aparece en INFO)
- [ ] `/rmurl` emite INFO con `label=` y `removed=true|false`
- [ ] Dead-letter y update fallido son WARN; getUpdates es WARN
- [ ] Tests actualizados a `logging.Discard()` y uno verifica el INFO de `/start`

**Verificación:**
- [ ] `nix develop -c go test ./internal/chat/`
- [ ] `nix develop -c go build ./...`

**Dependencias:** Task 3

**Archivos probables:**
- `internal/chat/bot.go`
- `internal/chat/onboarding.go`
- `internal/chat/bot_test.go`
- `internal/chat/poller_test.go`
- `cmd/bot/main.go`

**Alcance:** M (5 archivos)

---

### Task 7: `contact` → slog; se elimina el logger legacy de `main`
**Descripción:** Migrar `Extractor.Logger` a `*slog.Logger`. Info cuando se manda el teléfono,
DEBUG cuando el detalle no trae teléfono o se sirve de caché, WARN en fallo de fetch. Con el último
paquete migrado, borrar `legacy := log.New(...)` de `main` y el import de `log` si queda sin uso.

**Acceptance criteria:**
- [ ] `contact` ya no importa `log` ni usa `*log.Logger`
- [ ] `main` no tiene logger legacy; `slog` es el único logger
- [ ] El token sigue redactado (no se toca `httpx.Redact`)
- [ ] `grep -rn "log.Logger" --include=*.go` no devuelve campos de struct pendientes

**Verificación:**
- [ ] `nix develop -c go test ./internal/contact/`
- [ ] `nix develop -c go build ./...`
- [ ] `nix develop -c go vet ./...`

**Dependencias:** Task 4, Task 5, Task 6

**Archivos probables:**
- `internal/contact/extractor.go`
- `internal/contact/extractor_test.go`
- `cmd/bot/main.go`

**Alcance:** S (3 archivos)

---

### Task 8: instrumentar `fetch`/`Gate` con `WithLogger`
**Descripción:** Agregar `fetch.WithLogger(*slog.Logger)` como opción de `fetch.New` (firma
variádica, sin romper llamadores). Instrumentar el `Gate` (espera de pacing y cooldown) y el
`Client` (modo elegido, status, duración, reintentos, challenge). WARN en challenge/cooldown; DEBUG
en modo/status/ms/wait/url/reintento. Helpers nil-safe.

**Acceptance criteria:**
- [ ] `fetch.New(cfg)` sigue compilando (opciones variádicas)
- [ ] `fetch.New(cfg, fetch.WithLogger(l))` emite DEBUG con `mode=`, `status=`, `ms=`, `url=` en un fetch exitoso
- [ ] Un challenge emite WARN con `mode=`, `status=` y entra a cooldown (WARN/debug con `cooldown=`)
- [ ] Un reintento emite DEBUG con `attempt=`, `backoff=`
- [ ] Con logger nil, no panic
- [ ] Un test con logger a `bytes.Buffer` verifica el WARN de challenge

**Verificación:**
- [ ] `nix develop -c go test ./internal/fetch/`
- [ ] `nix develop -c go build ./...`

**Dependencias:** Task 1

**Archivos probables:**
- `internal/fetch/fetch.go`
- `internal/fetch/gate.go`
- `internal/fetch/fetch_test.go`
- `internal/fetch/gate_test.go`

**Alcance:** M (4 archivos)

---

### Task 9: wire del logger de fetch en `cmd/bot` y `cmd/probe`
**Descripción:** Pasar `fetch.WithLogger(logger)` al construir el `fetch.Client` en `main` (con el
logger slog) y en `cmd/probe` (con un logger slog a stdout y su nivel).

**Acceptance criteria:**
- [ ] `cmd/bot` construye el fetcher con `fetch.WithLogger(logger)`
- [ ] `cmd/probe` construye el fetcher con un logger slog legible
- [ ] El fetch del digest en `LOG_LEVEL=debug` deja `mode=`/`ms=` en los logs

**Verificación:**
- [ ] `nix develop -c go build ./...`
- [ ] Manual: `LOG_LEVEL=debug make run` y `make probe URL='<una URL>'`

**Dependencias:** Task 8, Task 3

**Archivos probables:**
- `cmd/bot/main.go`
- `cmd/probe/main.go`

**Alcance:** S (2 archivos)

---

### Task 10: Documentar logging en README
**Descripción:** Sección "Logs" en el README: niveles, `LOG_LEVEL`, qué responde cada uno, cómo
subir a DEBUG para diagnóstico y la regla de privacidad (IDs en INFO, URL en DEBUG).

**Acceptance criteria:**
- [ ] README documenta `LOG_LEVEL` y el significado de cada nivel
- [ ] README muestra un ejemplo de la línea resumen del digest y de los WARN de fetch
- [ ] README aclara que DEBUG es diagnóstico temporal y que la URL solo aparece ahí

**Verificación:**
- [ ] Revisión humana de la sección
- [ ] `nix develop -c go test ./...` (no debe cambiar nada)

**Dependencias:** Task 3–9

**Archivos probables:**
- `README.md`

**Alcance:** S (1 archivo)

---

## Checkpoint: Complete
- [ ] Suite completa verde: `nix develop -c go test ./...`
- [ ] `make fmt vet test` limpio
- [ ] Drill manual: con `LOG_LEVEL=debug` un ciclo real/mock deja resumen por usuario y detalle de fetch; con `info`, solo resúmenes y WARN
- [ ] Revisión humana del plan y del drill antes de cerrar

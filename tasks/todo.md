# TODO — Zonaprop Bot en Go

Checklist operativa del plan (`tasks/plan.md`). Cada item queda verificado antes de pasarse.

## Phase 0: Scaffold
- [x] Task 1: Scaffold del repo + toolchain reproducible (Go vía `nix develop`)
  - AC: `nix develop -c go build ./...` OK · `make test` OK · go.mod/dirs/.gitignore/Makefile/flake.devShell presentes
  - Archivos: go.mod, Makefile, flake.nix, .gitignore, dirs cmd/bot cmd/probe internal/*
  - Tamano: Small

## Phase 1: Configuración
- [x] Task 2: Paquete `internal/config` (env vars + validación + tests)
  - AC: parsea TELEGRAM_BOT_TOKEN/CHAT_ID, SEARCH_URLS (lineas/comas/file), CHECK_INTERVAL (def 60m),
        FLARESOLVERR_URL y HTTP_PROXY OPCIONALES (si faltan → no se usan, conexion directa),
        FETCH_RETRIES/TIMEOUT, MAX_BROWSER_TIMEOUT, DATA_DIR; errores claros; tests tabla
  - Archivos: internal/config/*.go
  - Tamano: Small

## Checkpoint: Fundaciones (Tasks 1-2)
- [x] `nix develop -c go build ./...` y `make test` en verde
- [x] Config valida y rechaza env faltantes con mensajes utiles

## Phase 2: Fetch (riesgo, fail-fast)
- [x] Task 3: Paquete `internal/fetch` (Modo A FlareSolverr + Modo B tls-client/proxy + reintentos) y `cmd/probe`
  - AC: probe contra env real devuelve HTML con `data-to-posting`; reintentos con backoff jittered;
        403/challenge/timeout retryeables; URL que falla no aborta; tests httptest de retries y modo
  - Depende: Task 2 (necesita env reales del usuario)
  - Archivos: internal/fetch/*.go, cmd/probe/main.go, fixtures/sample.html
  - Tamano: Medium
  - NOTA: FLARESOLVERR_URL y HTTP_PROXY opcionales — probe valida Modo A si hay FS, si no Modo B, si no directo
  - VALIDADO REAL: modo directo (huella TLS Chrome_152) devolvio 30 tarjetas con data-to-posting + price el 15:50.
        Posteriormente Cloudflare rate-limiteo el IP de dev (403); confirma la necesidad de FS/proxy en produccion.
  - PENDIENTE: env vars de FlareSolverr del usuario para validar Modo A.

## Checkpoint: Riesgo resuelto (Task 3)
- [x] Probe OK contra Zonaprop real (modo directo TLS; fallback B por proxy implementado y testeado)
- [x] Revisión del fixture y selectors contra el DOM real (h2 precio, h3 features, h4 location, a description)

## Phase 3: Parseo, estado y notificacion
- [x] Task 4: `internal/model` + `internal/parser` (extraccion rica con fallbacks + tests sobre fixture)
  - AC: Listing{ID sha1(link), URL, Titulo, Precio, M2, Ambientes, FotoURL}; fallbacks de selectors;
        extrae de fixture real; detecta 0 tarjetas; tests
  - Depende: Task 3 (fixture)
  - Archivos: internal/model/*.go, internal/parser/*.go
  - Tamano: Small
  - VALIDADO: 30/30 tarjetas reales extraidas (precio, titulo, m2, amb, foto, ubicacion).
- [x] Task 5: `internal/store` (JSONL append-only + dedup + tests)
  - AC: Load/Contains/Append en DATA_DIR/seen.jsonl; escritura atomica (fsync); no pierde IDs en restart
  - Archivos: internal/store/*.go
  - Tamano: Small
- [x] Task 6: `internal/telegram` (sendPhoto multipart + caption + rate-limit + retry_after + tests)
  - AC: envia foto descargada vía fetch/proxy con caption (titulo/precio/m2/amb/link); fallback caption-only;
        respeta retry_after; sin creds → dry-run stdout; tests httptest
  - Archivos: internal/telegram/*.go
  - Tamano: Medium

## Checkpoint: Componentes (Tasks 4-6)
- [x] `go test ./...` en verde
- [x] Dry-run de notifier renderiza tarjeta (precio, m2, ambientes, link)

## Phase 4: Orquestacion
- [x] Task 7: `cmd/bot/main.go` (loop CHECK_INTERVAL, primera corrida = todo, aislamiento por URL, SIGTERM)
  - AC: ticker configurable; estado vacio → notifica todo inventario; corridas siguientes solo nuevas;
        fallo por URL se loguea y sigue; flush de estado en SIGTERM; tests con fakes + dry-run real
  - Depende: Tasks 2,3,4,5,6
  - Archivos: cmd/bot/main.go, internal/bot/*
  - Tamano: Medium
  - VALIDADO e2e local contra copia del HTML real: 1ra corrida 30 alertas, 2da corrida 0.

## Checkpoint: E2E local (Task 7)
- [x] Dry-run contra env real (local): 1ra corrida notifica inventario; siguientes silencio salvo nuevas
- [x] Proceso sobrevive Ctrl-C/TERM limpiamente (flush)

## Phase 5: Docker y despliegue
- [x] Task 8: `Dockerfile` (arm64 scratch) + `docker-compose.yml` + `.env.example` + `README.md`
  - AC: imagen multi-stage ~15MB linux/arm64; compose con env_file y volumen data:/data; restart unless-stopped;
        README con pasos (obtener URL busqueda, deploy en Pi); `docker compose config` valido
  - Depende: Task 7
  - Archivos: Dockerfile, docker-compose.yml, .env.example, README.md
  - Tamano: Medium
  - Cross-build validado: binario arm64 estatico 12MB (CGO_ENABLED=0 GOOS=linux GOARCH=arm64).
  - PENDIENTE: `docker compose config` + build de imagen en el Pi (no hay docker en la maquina de dev).
- [ ] Task 9: Despliegue e2e en el RPi5 (operacional)
  - AC: contenedor corriendo en Pi; 1ra corrida envia inventario a Telegram; sin duplicados tras restart; logs limpios
  - Depende: Task 8 + acceso al host Pi (Alpine) + env vars reales (Telegram, y opcionalmente FS/proxy)
  - Tamano: Small

## Checkpoint: Completo
- [ ] Alerta real recibida en Telegram desde el Pi
- [ ] Sin duplicados tras reinicio del contenedor
- [ ] Revision humana antes de commit

# Implementation Plan: Zonaprop Bot en Go (`property-search`)

## Overview

Bot daemon en Go que monitorea búsquedas de **compra** de Zonaprop (Zona Norte, GBA) y notifica por
Telegram cada publicación **nueva** con tarjeta completa: precio, m², ambientes, foto y link directo.
Metodología equivalente a `nazarenads/zonaprop-bot` (HTTP + parseo de selectors + dedup por
`sha1(link)` + loop con intervalo) pero en Go, con estado persistente en volumen, reintentos sobre
proxy rotativo y resolución de Cloudflare vía FlareSolverr. Se despliega como imagen Docker ARM64 en
un RPi5 (host Alpine Linux). Primera corrida notifica **todo** el inventario actual. Sin seguimiento
de cambios de precio (descartado por el usuario).

## Architecture Decisions

- **Resolución de Cloudflare → FlareSolverr (Modo A, principal).** POST `{FLARESOLVERR_URL}/v1` con
  `{"cmd":"request.get","url":...,"maxTimeout":...}` devuelve HTML resuelto. Reusamos la instancia ya
  existente; es la forma más robusta de pasar el challenge y mantiene la metodología "request + parse".
- **Servicios externos OPCIONALES (sin defaults de red).** `FLARESOLVERR_URL` e `HTTP_PROXY` se usan
  sólo si su env var está definida; si no, se omiten y se conecta directo. Selección automática por
  env vars: (1) con `FLARESOLVERR_URL` → resolver vía FlareSolverr; (2) sin ella y con `HTTP_PROXY` →
  cliente con huella TLS de Chrome saliendo por el proxy rotativo (equivalente a `cloudscraper`);
  (3) sin ninguna → conexión TLS directa.
- **Reintentos con backoff jittered** en ambos modos: cada intento abre conexión nueva (rota IP del
  proxy); 403/challenge/timeout/5xx son retryeables. Una URL que agota reintentos se loguea y el ciclo
  continúa (no crashea).
- **Parseo con `goquery`** sobre `div[data-to-posting]` (mismo selector que el repo base) + selectors de
  respaldo para precio/m²/ambientes/foto dentro de la tarjeta. Sin filtro USD (en compra es la norma).
- **Persistencia `seen.jsonl`** (append-only, crash-safe, mismo rol que `seen.txt`) en `DATA_DIR` (volumen).
- **Telegram:** `sendPhoto` multipart (foto descargada vía el fetch/proxy configurado) + caption con
  título/precio/m²/ambientes/link. Fallback caption-only si la imagen falla. Rate-limit ~2.5s entre
  mensajes y respeto de `retry_after`. Sin credenciales → dry-run por stdout (como el original).
  mensajes y respeto de `retry_after`. Sin credenciales → dry-run por stdout (como el original).
- **Docker:** multi-stage `golang:1.24-alpine` (estático, sin CGO) → `scratch` + CA certs, `linux/arm64`,
  ~15 MB. Build nativo con `docker compose up -d --build` en el RPi5.
- **Toolchain dev (NixOS sin Go):** devShell vía `nix develop` (go_1_24). Sin Docker local → el build de
  imagen se hace en el Pi/Alpine.

## Task List

### Phase 0: Scaffold
- [x] Task 1: Scaffold del repo + toolchain reproducible (Go vía `nix develop`)

### Phase 1: Configuración
- [x] Task 2: Paquete `internal/config` (env vars + validación + tests)

### Checkpoint: Fundaciones (Tasks 1–2)
- [x] `nix develop -c go build ./...` y `make test` en verde

### Phase 2: Fetch (ruta de riesgo, fail-fast)
- [x] Task 3: Paquete `internal/fetch` (Modo A FlareSolverr + Modo B tls-client/proxy + reintentos)
      y `cmd/probe` para validar contra Zonaprop real y generar fixture

### Checkpoint: Riesgo resuelto (Task 3)
- [x] `cmd/probe` contra env real devuelve HTML con `data-to-posting` (Modo directo TLS validado;
      30 tarjetas. Cloudflare rate-limiteo el IP de dev luego; FS/proxy quedan como capas opcionales)
- [x] Revisión humana del fixture y de selectors antes de seguir

### Phase 3: Parseo, estado y notificación
- [x] Task 4: `internal/model` + `internal/parser` (extracción rica con fallbacks + tests sobre fixture)
- [x] Task 5: `internal/store` (JSONL append-only + dedup + tests)
- [x] Task 6: `internal/telegram` (sendPhoto multipart + caption + rate-limit + retry_after + tests)

### Checkpoint: Componentes (Tasks 4–6)
- [x] `go test ./...` en verde
- [x] Dry-run de notifier renderiza tarjeta (precio, m², ambientes, link)

### Phase 4: Orquestación
- [x] Task 7: `cmd/bot/main.go` (loop con `CHECK_INTERVAL`, primera corrida = todo, aislamiento por URL,
      graceful shutdown SIGTERM) + e2e con fakes y dry-run real

### Checkpoint: E2E local (Task 7)
- [x] Dry-run contra env real: primera corrida notifica inventario (30 alertas), luego silencio

### Phase 5: Docker y despliegue
- [x] Task 8: `Dockerfile` (arm64 scratch) + `docker-compose.yml` + `.env.example` + `README.md`
      (cross-build arm64 validado; falta `docker compose config` en el Pi, sin docker local)
- [ ] Task 9: Despliegue e2e en el RPi5 (operacional; requiere acceso al host + env reales)

### Checkpoint: Completo
- [ ] Alerta real recibida en Telegram desde el Pi; sin duplicados tras reinicio del contenedor
- [ ] Listo para review / commit

## Dependency Graph

```
Task 1 (scaffold)
  └── Task 2 (config)                    [Task 5 (store) puede correr en paralelo]
        └── Task 3 (fetch/probe → fixture real + validación riesgo)
              └── Task 4 (parser sobre fixture)
                    ├── Task 5 (store)
                    └── Task 6 (telegram)
                          └── Task 7 (cmd/bot wiring)
                                └── Task 8 (docker)
                                      └── Task 9 (deploy Pi)
```

Nota de slicing: al ser un daemon, cada tarea entrega una unidad funcional con sus tests (config →
fetch → parser → store → telegram → wiring). La "slice vertical" la aporta la Task 3 (red real de punta
a punta antes de invertir en el resto) y la Task 7 (flujo completo con fakes). Alto riesgo adelante.

## Risks and Mitigations

| Riesgo | Impacto | Mitigación |
|--------|---------|------------|
| FlareSolverr no resuelve el challenge (config/puerto/versión) | Alto | Probe en Task 3 antes de construir el resto; fallback Modo B (tls-client+proxy rotativo) |
| URL/puerto real de FlareSolverr desconocido | Alto | Env var `FLARESOLVERR_URL` (opcional); se usa sólo si está definida |
| Cambio de DOM de Zonaprop rompe selectors | Medio | Fixture de HTML real + selectors de respaldo + test que detecta 0 tarjetas |
| Proxy rotativo no rota en 403 (sólo en fallo de conexión) | Medio | Reintentos con conexión nueva por intento + diferenciar challenge (retry) de error real |
| Hotlink/protección de imágenes en descarga | Bajo | Fallback caption-only con link |
| Rate-limit de Telegram | Bajo | Sleep ~2.5s + honor `retry_after` |
| Sin Go/Docker en la máquina de dev (NixOS) | Bajo | `nix develop` para toolchain; build de imagen en el Pi |

## Open Questions

- URL real de FlareSolverr (y credenciales si las tiene). **Opcional** — el bot funciona igual sin ella
  (Modo B o directo).
- Una URL de búsqueda "compra" real de Zona Norte para configurar/validar (sample).
- En el Pi: confirmar si FlareSolverr/proxy son alcanzables y con qué IP/puerto (LAN `192.168.1.150:xxxx`
  vs bridge `172.10.0.1:xxxx`). Si no están definidas, el bot va directo.
- ¿Se commitea por tarea o al final? (por defecto: no commitear salvo pedido explícito)

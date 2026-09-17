# Área: Deploy, config y docs
> **Este documento es racional de diseño, no la lista de tareas.** La lista ejecutable,
> con acceptance criteria, verificación y alcance por tarea, está en `tasks/todo.md`.


**Objetivo:** que el stack quede **listo** para arrancar en el Pi (compose válido, imagen que
buildea, runbook escrito), que un `.env` incompleto falle en vez de correr en silencio, y que el
repo quede coherente con lo que el código hace.

**Alimenta:** V7. La limpieza final de `internal/config` es **V7.3**; **V1.1** hace solo lo mínimo
que el camino vertical necesita (evita reescribir config dos veces).

> **Alcance: este dominio llega hasta "listo para desplegar".** El despliegue real en la RPi 5
> (levantar los contenedores en el Pi, validar arm64 en hardware, confirmar la alerta real) **no
> forma parte de este plan**. Todo lo de acá se valida localmente: `docker compose config`, build de
> imagen y, como máximo, cross-build `GOOS=linux GOARCH=arm64`. Lo que no se puede validar sin el
> host se marca explícitamente como pendiente para el plan de despliegue.

## G1 — Compose final

- AC: `restart: unless-stopped` en **los tres** servicios. El compose actual ya lo tiene en el bot;
  la reescritura no puede perderlo. En un Pi headless con logs a stdout, un bot muerto es invisible.
- AC: `extra_hosts: ["host.docker.internal:host-gateway"]` en **flaresolverr**, no en el bot. El que
  consume un proxy residente en el host es Chromium vía `PROXY_URL`. Ponerlo en el bot es peso
  muerto y deja a FlareSolverr sin alcanzar el proxy.
- AC: **solo `PROXY_URL` en el entorno de flaresolverr. Nunca `HTTP_PROXY`, `HTTPS_PROXY` ni
  `http_proxy`/`https_proxy`.** A2 lo midió: con `HTTP_PROXY` seteado, Chromium los lee del entorno,
  su test de instalación falla con `Error getting browser User-Agent` y el contenedor queda en
  `Exited (1)` — o sea que el servicio del que depende **todo** el fetch ni arranca. Es el mismo
  envenenamiento de entorno que sufre Go con `HTTP_PROXY` (de ahí `ZONAPROP_PROXY`), del lado del
  browser. Documentarlo explícitamente en el `.env.example` y en el README del runbook.
- AC: healthcheck real de FlareSolverr (`GET /` en 8191) y `depends_on: service_healthy` desde el
  bot. FlareSolverr corre un check de `TEST_URL` con browser al arrancar y entra en restart-loop si
  su proxy es inalcanzable; con `depends_on: started` el bot arranca contra un cadáver.
- AC: `mem_limit: 1.5g` en flaresolverr, dimensionado para **semáforo 1**. FlareSolverr v3 lanza un
  Chromium nuevo por request; con semáforo 2 y un límite pensado para uno, el segundo browser muere
  por OOM justo en el pico de carga.
- AC: versión de FlareSolverr **pinneada en `v3.3.20`**, no `:latest`.
- AC: `logging: {driver: json-file, options: {max-size: "10m", max-file: "3"}}` en los tres.
  Tres containers verbose sin rotación llenan la SD en semanas, y un disco lleno tumba Postgres de
  forma no limpia (fallo de escritura de WAL -> loop de crash recovery).
- AC: `${POSTGRES_USER:?required}` / `${POSTGRES_PASSWORD:?required}` / `${POSTGRES_DB:?required}`.
  Sin `:?`, un `.env` fresco hace que compose sustituya vacío sin error y Postgres se niegue a
  inicializar.
- AC: el bot recibe `POSTGRES_*` y construye el DSN **en Go** con `url.UserPassword`. Interpolar
  `postgres://user:pass@host/db` en compose se rompe con cualquier password que contenga `@ : / # %`
  — justo lo que produce "generá un password fuerte" — y el fallo aparece como error de auth,
  apuntando a la causa equivocada.
- AC: runbook de deploy arranca con `docker compose down --remove-orphans`. Renombrar el servicio
  `zonaprop-bot` a `bot` crea un servicio nuevo y solo **avisa** del huérfano: el container viejo
  sigue corriendo (con `restart: unless-stopped`), crash-loopeando por config o, peor, mandando
  alertas desde el store JSONL viejo que el dedup de Postgres desconoce.
- AC: documentación de rotación de password (`ALTER ROLE`) — `POSTGRES_PASSWORD` solo se consume en
  el primer init; cambiarlo en `.env` después desincroniza el DSN del cluster real.

## G2 — Imagen y contexto de build

- AC: **`.dockerignore`** con `.git`, `.env`, `data/`, `tasks/`, `fixtures/`, `bin/`. El repo no lo
  tiene y `Dockerfile:7` hace `COPY . .`: en el Pi el contexto incluye `.env` con el token de
  Telegram y el password de Postgres, que quedan en la capa del build stage y en la caché de
  BuildKit — recuperables del historial de build aunque el stage final sea `scratch`. De yapa,
  `.git` invalida la caché en cada commit.
- AC: quitar `VOLUME /data` y `ENV DATA_DIR=/data` del Dockerfile. Con el estado en Postgres,
  `VOLUME` crea un volumen anónimo huérfano por container que sobrevive a `docker compose down`
  (`--remove-orphans` no borra volúmenes).
- AC: la imagen sigue `FROM scratch`, `CGO_ENABLED=0`, estática. pgx es Go puro, así que se conserva.

## G3 — `internal/config` reescrito

Ninguna fase anterior es dueña de este cambio y todo depende de él. **Hoy el bot no puede
arrancar** con este diseño: `config.Load` falla duro sin `SEARCH_URLS` y `main.go` hace
`log.Fatalf`.

- AC: se eliminan `SEARCH_URLS`, `SEARCH_URLS_FILE`, `CHECK_INTERVAL` y `DATA_DIR`.
- AC: `TELEGRAM_BOT_TOKEN` obligatorio en modo servidor (detectado por presencia de `DATABASE_URL`)
  con `log.Fatal`. Relajar la regla de pairing sin este guard hace que un deploy al que le falta el
  token arranque **bien**, pase los healthchecks, loguee a un stdout que nadie lee y no le mande
  nada a nadie, para siempre. La regla vieja al menos fallaba fuerte.
- AC: `dryRun()` pasa a depender **solo** del token.
- AC: `TELEGRAM_CHAT_ID` deja de ser destino único; se agrega `OPERATOR_CHAT_ID` para alertas.
  Toda llamada de alerta al operador debe ser no-op con log rate-limited si está vacío: hoy
  convertirse en un error de la API de Telegram justo cuando algo salió mal.
- AC: nuevas variables `DAILY_HOUR`, `SCHEDULE_TZ`, `RUN_ON_START`, `FETCH_RATE_LIMIT`,
  `SEED_CHAT_ID`, `SEED_URLS` (coma-separado `label|url`), `POSTGRES_USER`, `POSTGRES_PASSWORD`,
  `POSTGRES_DB`.
- AC: **`ZONAPROP_PROXY`** reemplaza `HTTP_PROXY` (ver B3: la stdlib envenena el transporte default
  y rompe el modo FlareSolverr justo cuando el proxy está configurado).
- AC: en el **mismo commit**: `config_test.go`, los helpers `cfgFrom` de `fetch_test`/`bot_test`/
  `telegram_test` (~15 tests que asumen `SEARCH_URLS` como baseline obligatorio y esperan *error*
  cuando falta), `cmd/probe/main.go` (itera `cfg.SearchURLs` y lee `cfg.DataDir`, ambos eliminados;
  pasa a tomar URLs por argv), `internal/bot` (itera `cfg.SearchURLs` y su interfaz `Notifier`
  cambia de firma), y **eliminación de `internal/store`** — si queda en el árbol hay dos stores
  compitiendo.

## G4 — Docs y toolchain

- AC: `.env.example` sin `SEARCH_URLS`/`CHECK_INTERVAL`/`DATA_DIR`, con todos los `POSTGRES_*`,
  `ZONAPROP_PROXY` y el proxy de dev `http://192.168.1.150:8089` como ejemplo.
- AC: `README.md` reescrito: onboarding por `/start` en vez de armar URLs en env, compose de tres
  servicios, runbook con `down --remove-orphans`, y el cambio de comportamiento de la primera
  corrida (baseline silencioso en vez de "notifica todo el inventario").
- AC: el runbook documenta **cómo encontrar usuarios esperando activación**, que es el proceso
  manual mientras el aviso automático está diferido:
  `SELECT chat_id, onboarded_at FROM users WHERE active = false AND state = 'ready' ORDER BY onboarded_at;`
  y cómo activarlos: `UPDATE users SET active = true WHERE chat_id = <id>;`.
- AC: `Makefile`: el target `image` buildea la tag `zonaprop-bot:arm64` que el compose renombrado ya
  no referencia; agregar targets para compose up/down/logs y para los tests de integración.
- AC: `flake.nix`: devShell sigue con `go gopls gofumpt`; opcional `golangci-lint`. Los tests con
  los tests de integracion necesitan Docker (o un Postgres alcanzable), que esta disponible en dev pero no lo garantiza
  el flake — por eso el `t.Skip`.
- AC: los archivos del plan v1 quedan archivados en `tasks/archive/v1-zonaprop-bot/`, y el README
  deja claro que v2 los reemplaza y que el deploy del Pi de v1 no se hizo a propósito.

## Archivos

`docker-compose.yml`, `.dockerignore` (nuevo), `Dockerfile`, `internal/config/*.go`,
`cmd/probe/main.go`, `internal/bot/*`, `internal/store/` (eliminar), `.env.example`, `README.md`,
`Makefile`, `flake.nix`.

# TODO — Bot de calificación de propiedades (v2)

Lista ejecutable del plan `tasks/plan.md`. Cada tarea tiene acceptance criteria, verificación,
archivos y alcance. El racional de diseño está en `tasks/specs/`; la evidencia en
`tasks/research/probe-results.md`.

**Comandos del repo:** `nix develop -c go test ./...` · `nix develop -c go build ./...` ·
`make fmt vet test` · `make run` (dry-run) · `make probe`.

**Regla de slicing:** cada rebanada V* deja el sistema **funcionando y demostrable**. Si una tarea
toca más de ~5 archivos, se parte antes de empezar.

---

## V1: Camino de valor central — seed → fetch → manda con botones

> Primer entregable usable, y donde vive el riesgo de Cloudflare. No se construye nada más hasta
> que esto manda una publicación real.

### V1.1 Config mínima + contenedores ✅
**Descripción:** Plomería de entorno para el camino vertical. No se limpia config: solo se habilita
lo que V1 necesita, para no reescribirla dos veces (la limpieza final es V7.3).

**Acceptance criteria:**
- [x] `SEARCH_URLS` deja de ser **obligatoria** (el seed provee las URLs); no se elimina todavía
- [x] `HTTP_PROXY` renombrado a `ZONAPROP_PROXY`; el bot no lee `HTTP_PROXY` en ningún camino
- [x] Nuevas: `FLARESOLVERR_URL`, `POSTGRES_*`, `SEED_CHAT_ID`, `SEED_URLS`
- [x] **Formato de `SEED_URLS`:** entradas separadas por coma, cada una `label|url`, en **una sola
      línea** (compatible con `env_file`, que rompe con multi-línea sin comillas). Label sin `|` ni
      `,`; el parser lo rechaza con error claro
- [x] DSN construido en Go con `url.UserPassword`, **no** interpolado en compose
- [x] compose levanta `postgres:17-alpine` (healthcheck `pg_isready -h 127.0.0.1`) y `flaresolverr`
      pinneado con **`PROXY_URL`** y sin `HTTP_PROXY`/`HTTPS_PROXY` en su entorno

**Verificación:**
- [x] `docker compose config` válido
- [x] `docker compose up -d postgres flaresolverr` → postgres `healthy`, FS `running` con
      `Test successful!` y `PROXY_URL` como **única** variable de proxy en su entorno
- [x] `nix develop -c go test ./...` verde

**Dependencias:** ninguna
**Archivos:** `internal/config/config.go`, `internal/config/config_test.go`, `docker-compose.yml`,
`.env.example`. **Extra sobre lo planeado:** el rename obligó a tocar `internal/fetch/fetch.go`,
`internal/fetch/tls.go`, `internal/fetch/flaresolverr.go`, `internal/fetch/fetch_test.go`,
`internal/telegram/telegram.go`, `cmd/bot/main.go` y `README.md`; se agregó `noProxyTransport()`
en `fetch` y `telegram` para que el bot **no** herede `HTTP_PROXY` del entorno vía
`http.DefaultTransport` (transporte cloneado con `Proxy = nil`).
**Alcance:** M

### V1.2 Postgres: migrador, seeds y esquema mínimo
**Descripción:** Persistencia con migraciones versionadas y seeds idempotentes. Solo las tablas que
V1 usa; el resto se agrega cuando su rebanada lo pida.

**Acceptance criteria:**
- [ ] Pool `pgx` con **retry acotado** al arrancar (cubre el race del healthcheck)
- [ ] Migrador versionado embebido (`go:embed` + `schema_migrations`), idempotente
- [ ] Seeds: settings globales + usuario con `SEED_CHAT_ID` y sus `SEED_URLS` (formato de V1.1)
- [ ] Esquema: `users` (con `active` y `active_model_version`), `search_urls`, `listings` (con
      `recency_rank`), `listing_sources`, `deliveries`
- [ ] Tests con `testcontainers-go`, con `t.Skip` si el daemon no está disponible

**Verificación:**
- [ ] `nix develop -c go test ./internal/db/...`
- [ ] Correr migraciones + seeds **dos veces** seguidas sin error
- [ ] Test de aislamiento: el usuario A no ve datos de B

**Dependencias:** V1.1
**Archivos:** `internal/db/*.go`, `migrations/*.sql`, `internal/user/*.go`, `go.mod`
**Alcance:** M

### V1.3 Fetch: `Result{Status,Header}`, FlareSolverr obligatorio, budget
**Descripción:** El camino de red completo con el freno de mano puesto. Es la tarea de mayor riesgo.

**Acceptance criteria:**
- [ ] `Result` gana `Status` y `Header`; `fetchViaTLS` **propaga** status/headers en vez de colapsar
      todo non-200 a un error genérico (sin esto la clasificación de V5.3 es imposible)
- [ ] Timeout del cliente de FlareSolverr **mayor** que el `maxTimeout` (p. ej. 90s) y
      **`maxTimeout` = 60s**; reintentos de FS **≤1** (medido: 150s × 3 = 7.5 min de browser churn)
- [ ] Cadena por intento FS → proxy → directo, conservando `TestFlareSolverrFallsBackToTLS`
- [ ] Budget global: token bucket **1 req/60s** (ráfaga 1) solo para `zonaprop.com.ar`, semáforo 1 con FS,
      carril prioritario (operador + contactos), cooldown tras challenge
- [ ] `errNoMode` muerto eliminado

**Verificación:**
- [ ] `nix develop -c go test ./internal/fetch/...`
- [ ] `make probe` contra la URL real devuelve ~30 tarjetas y respeta el intervalo
- [ ] Test de clasificación: 403 con `cf-mitigated` → bloqueado; 200 con 0 tarjetas → vacío;
      timeout → transporte

**Dependencias:** V1.1
**Archivos:** `internal/fetch/fetch.go`, `internal/fetch/tls.go`, `internal/fetch/flaresolverr.go`, `internal/fetch/fetch_test.go`
**Alcance:** M

### V1.4 Parser de card tipado
**Descripción:** Features reales y tipadas sobre el fixture real, con las correcciones que salieron
de la sonda (outliers, m² cub. inexistente, dorm constante).

**Acceptance criteria:**
- [ ] `model.Listing` tipado: `ZonapropID`, `CanonicalURL`, `PriceAmount *int64`, `Currency`,
      `M2Tot`/`M2Cub *float64`, `M2Basis`, `Dorm`/`Banos *int`, `Expensas *int64`, `Operation`
- [ ] **`Operation` (operation_type) se detecta del path de la URL** (`-alquiler-` / `-venta-`) y
      **`Currency` del prefijo del precio** (`USD` vs `$`→ARS); ambos van como columnas en `listings`
- [ ] `Parse` deriva el **origin** de `siteBase` (bug pre-existente: se le pasa la URL de búsqueda
      completa y los hrefs son root-relative) + test de regresión
- [ ] `parsePrice` cubre `USD 83.900`, `$ 150.000.000`, `Desde USD …`, `Consultar precio` **y un
      alquiler mensual en ARS** (`$ 450.000`)
- [ ] Guarda de outliers `m² ∈ [10,1000]`; ausentes → NULL, **nunca 0**
- [ ] `partido`: último segmento, o el primero si es macrozona

**Verificación:**
- [ ] `nix develop -c go test ./internal/parser/...` contra `fixtures/search_gba_norte.html`
- [ ] Conteo de tarjetas PROPERTY y de `zonaprop_id` vacío expuestos

**Dependencias:** V1.1
**Archivos:** `internal/model/model.go`, `internal/parser/parser.go`, `internal/parser/parser_test.go`
**Alcance:** M

### V1.5 Dedup + baseline silencioso + envío con botones
**Descripción:** Cierra el camino: candidatas acotadas al usuario, baseline que evita reproducir el
historial, y envío con teclado inline.

**Acceptance criteria:**
- [ ] Candidatas = `NOT EXISTS(deliveries)` **y** acotadas por `listing_sources` a las URLs del
      usuario (sin esto se filtran publicaciones entre usuarios)
- [ ] Baseline silencioso: al sembrar/validar una URL se insertan `deliveries` de la página 1 **sin
      enviar**
- [ ] `Notify(ctx, chatID, listing, opts)` con keyboard 👍/👎 y `callback_data` `u:<id>` / `d:<id>`
- [ ] Caption presupuestado en **UTF-16** reservando la URL; fallback a `sendMessage` si excede
- [ ] Dedup por `zonaprop_id`, nunca por URL

**Verificación:**
- [ ] E2E con fakes: primera corrida manda, segunda silencio
- [ ] Dry-run contra env real manda el inventario una sola vez
- [ ] Reiniciar el proceso no re-manda

**Dependencias:** V1.2, V1.3, V1.4
**Archivos:** `internal/bot/*`, `internal/telegram/telegram.go`, `internal/digest/*` (mínimo)
**Alcance:** M

### V1.6 Verificación E2E (dry-run y Telegram real)
**Descripción:** Prueba de punta a punta con el usuario sembrado, sin código nuevo salvo ajustes.

**Acceptance criteria:**
- [ ] Dry-run: 1ª corrida notifica, siguientes en silencio
- [ ] Telegram real: llega la tarjeta con foto, prestaciones y botones visibles
- [ ] Los logs muestran el rate de fetch respetado

**Verificación:** `make run` con env reales + inspección del chat
**Dependencias:** V1.5
**Archivos:** (ajustes menores)
**Alcance:** S

### Checkpoint V1
- [ ] Un usuario sembrado recibe sus publicaciones nuevas una sola vez, con botones
- [ ] Reiniciar el proceso no re-manda nada
- [ ] El budget de fetch se respeta
- [ ] **Revisión humana antes de seguir**

---

## V2: Califico y el bot lo persiste

### V2.1 Telegram inbound: long-poll, offset, dead-letter, lease
**Descripción:** El bot pasa de notificador a chat bot. Es la base de todo lo interactivo.

**Acceptance criteria:**
- [ ] `getUpdates` long-poll con **cliente dedicado** (`timeout = poll + 10s`)
- [ ] `bot_state.last_update_id` avanzado **dentro** de la transacción del handler
- [ ] Dead-letter tras N fallos sobre el mismo `update_id`: persiste el offset y alerta
- [ ] Lease por advisory lock (una instancia de dev no puede robar updates de producción)
- [ ] Handlers idempotentes ante redeliveria

**Verificación:**
- [ ] Tests httptest simulando updates y fallos del handler
- [ ] Prueba de restart a mitad: ni se pierde ni se duplica

**Dependencias:** V1
**Archivos:** `internal/chat/*.go`, `internal/telegram/*.go`, `migrations/` (bot_state)
**Alcance:** M

### V2.2 Callbacks: respuesta, revocación, first-tap-wins
**Descripción:** Que un tap se sienta bien y no ensucie los datos.

**Acceptance criteria:**
- [ ] **Siempre** `answerCallbackQuery` (hoy no existe en el repo: sin esto queda spinner + error)
- [ ] `editMessageReplyMarkup` revoca el teclado tras el primer tap
- [ ] **First-tap-wins**: un segundo tap no re-ejecuta side effects
- [ ] Tap tardío (≤14 días) → `answerCallbackQuery("expiró")` + revocación; **nunca** responder
      "guardado" a un tap descartado

**Verificación:** tests httptest de `callback_query`; prueba manual de doble tap y de tap viejo
**Dependencias:** V2.1
**Archivos:** `internal/chat/callbacks.go`, `internal/telegram/*.go`, tests
**Alcance:** M

### V2.3 `ratings` + `deliveries.status`
**Descripción:** Persistir la calificación de forma atómica y con ciclo de vida.

**Acceptance criteria:**
- [ ] `ratings` con `ON CONFLICT`, `UNIQUE(user_id, listing_id)`
- [ ] `deliveries.status ∈ pending|sent|failed|dead` + `attempts`
- [ ] Escritura de rating y delivery en **una transacción**
- [ ] Reconciliación de `deliveries` con `message_id IS NULL`

**Verificación:**
- [ ] Test de transacción: un fallo simulado no deja estado a medias
- [ ] Test de reconciliación

**Dependencias:** V2.2
**Archivos:** `internal/db/*.go`, `internal/chat/callbacks.go`, `migrations/`
**Alcance:** S

### V2.4 `/model` con conteos por bucket
**Descripción:** Primera superficie de transparencia. Solo lo que ya se puede calcular.

**Acceptance criteria:**
- [ ] `/model` muestra conteos `ups`/`downs` por bucket y cuántos ratings hay
- [ ] **No** muestra métricas que todavía no se pueden computar (agreement llega en V3.3)

**Verificación:** test del comando + prueba manual
**Dependencias:** V2.3
**Archivos:** `internal/chat/commands.go`
**Alcance:** S

### Checkpoint V2
- [ ] Un tap produce exactamente un rating, con callback respondido y keyboard revocado
- [ ] Un restart no pierde ni duplica updates

---

## V3: Reordena por lo aprendido y explica

### V3.1 Buckets + score literal
**Descripción:** La función de score, escrita de una única manera implementable.

**Acceptance criteria:**
- [ ] Buckets categóricos **sin particionar**: `partido`, `banios`
- [ ] Buckets numéricos **namespaciados por `operation_type:currency`**
      (`ppm2:venta:USD:1500-1750`, `expensas:alquiler:ARS:250k-300k`), con paso por namespace:
      `venta:USD` = 250 validado; `alquiler:ARS`/`venta:ARS` provisionales a validar con datos
- [ ] **`operation_type` y `currency` NO son buckets de scoring**, son claves de partición
- [ ] `ppm2` sobre `m2_tot` con outliers `[10,1000]` fuera; `m2_bin` de 10 m² con `m2_basis` en la clave;
      `expensas` namespaciada
- [ ] **`currency` vacío/desconocido ⇒ features numéricas a `unknown`** (guarda contra el outlier de 1000×)
- [ ] `rooms` y `dorm` **fuera del scoring** (medidos constantes en la búsqueda real)
- [ ] `rate = (ups+1)/(ups+downs+2)`; `score = mean(ln(rate/0.5))`; unknown aporta 0
- [ ] Orden `score DESC NULLS LAST, first_indexed_at DESC, recency_rank ASC`

**Verificación:**
- [ ] Tests de tabla de buckets sobre `fixtures/search_gba_norte.html`
- [ ] Test de orden determinista con scores empatados

**Dependencias:** V1.4, V2.3
**Archivos:** `internal/score/*.go`
**Alcance:** M

### V3.2 Retrain por usuario con versión atómica
**Descripción:** Aprender sin pisar el score que se está leyendo.

**Acceptance criteria:**
- [ ] Retrain **por usuario**, cada uno en su propia transacción, log-and-continue
- [ ] `active_model_version` volteado **dentro** de la misma transacción que escribe los pesos
- [ ] El scorer lee solo la versión activa
- [ ] Prune de versiones no referenciadas por `deliveries`

**Verificación:**
- [ ] Test: los ratings corruptos de un usuario no afectan a otro
- [ ] Test: crash entre escritura y flip no deja lectores a medias

**Dependencias:** V3.1
**Archivos:** `internal/score/retrain.go`, `internal/db/*.go`
**Alcance:** M

### V3.3 Razones n≥3 + concordancia
**Descripción:** La parte que hace confiable al modelo… o que se calla si no tiene datos.

**Acceptance criteria:**
- [ ] Razones: top-2 buckets con `(ups+downs) ≥ 3`, desempate `|rate-0.5|`, **en español**
- [ ] Si ningún bucket llega a n≥3 → **no se muestra ninguna razón**
- [ ] `/model`: concordancia pairwise **intra-día** anclada en `ratings.created_at`, recalculada
- [ ] Suprimida con n<30, con mensaje "todavía aprendiendo"

**Verificación:** tests de tabla de razones; test de supresión
**Dependencias:** V3.2
**Archivos:** `internal/score/reasons.go`, `internal/chat/commands.go`
**Alcance:** S

### Checkpoint V3
- [ ] Con <30 ratings la UI no inventa razones ni porcentajes
- [ ] El orden con empates es estable y favorece lo más nuevo

---

## V4: Corre solo todos los días

### V4.1 Scheduler tz-aware
**Descripción:** Disparar a las 09:00 **de Buenos Aires**, no a las 06:00 por UTC.

**Acceptance criteria:**
- [ ] `import _ "time/tzdata"` (la imagen `scratch` no trae zoneinfo)
- [ ] `SCHEDULE_TZ` con default explícito `America/Argentina/Buenos_Aires`; **vacío no es error**
      (`LoadLocation("")` devuelve UTC sin error, así que un fail-fast no lo catchea)
- [ ] Timer al próximo 09:00, **sin corrida al arrancar**
- [ ] `TryLock` por job, con log de skip

**Verificación:** test con TZ fija que verifica el próximo disparo calculado
**Dependencias:** V3.3
**Archivos:** `cmd/bot/main.go`, `internal/scheduler/*.go`
**Alcance:** S

### V4.2 `digests` + resume auto-sanante
**Descripción:** Que un día interrumpido no se pierda ni se duplique.

**Acceptance criteria:**
- [ ] Fila `digests(user_id, run_date, status)` creada **al agendar**, aunque el lock saltee
- [ ] Input del digest = **todas las no entregadas** de las URLs del usuario (no "lo de hoy")
- [ ] Al arrancar y en cada corrida, terminar cualquier digest no-`done`
- [ ] Reintento de `deliveries` en `pending`/`failed` con tope, luego `dead` + alerta

**Verificación:** test de resume simulando interrupción; test de día salteado por lock
**Dependencias:** V4.1
**Archivos:** `internal/digest/*.go`, `internal/db/*.go`
**Alcance:** M

### V4.3 Gate `active` + tope diario + cobertura
**Descripción:** El interruptor del operador y el límite del portal.

**Acceptance criteria:**
- [ ] El digest solo recorre `active = true` **y** `state != 'stopped'`
- [ ] Tope diario de **15 envíos** con **excedente re-puntuado al día siguiente** (nada se descarta)
- [ ] `last_card_count` registrado; alerta si satura el techo de 30 de forma sostenida

**Verificación:** tests con usuarios active/stopped; test de tope y carry-over
**Dependencias:** V4.2
**Archivos:** `internal/digest/*.go`, `internal/db/*.go`
**Alcance:** S

### Checkpoint V4
- [ ] El digest corre solo para `active=true` y reanuda tras un restart a mitad
- [ ] Un día interrumpido no se pierde

---

## V5: `/start` para desconocidos

### V5.1 Máquina de estados + comandos
**Descripción:** El alta conversacional, sin condiciones de carrera.

**Acceptance criteria:**
- [ ] Transiciones explícitas `idle → await_url → await_label → validating → idle`
- [ ] **Un escritor serializado por usuario** (el poll loop y el scheduler corren concurrentes)
- [ ] Entrada no reconocida **responde**, no se descarta
- [ ] Solo chat privado; en grupo se rechaza con mensaje claro
- [ ] Comandos: `/start`, `/addurl`, `/rmurl`, `/list`, `/stop`, `/borrardatos`, `/help`

**Verificación:** tests de transición, de concurrencia y de doble paste en un solo mensaje
**Dependencias:** V2.2
**Archivos:** `internal/onboarding/*.go`, `internal/chat/commands.go`
**Alcance:** M

### V5.2 Validación sintáctica, normalización y cap
**Descripción:** Cerrar el SSRF y evitar fetches duplicados.

**Acceptance criteria:**
- [ ] `https` obligatorio **y** host exactamente `zonaprop.com.ar` o sufijo `.zonaprop.com.ar`
      (un `Contains` lo pasan `zonaprop.com.ar.attacker.test` y `notzonaprop.com.ar`)
- [ ] `url_norm`: host lowercase, query ordenado, params de tracking fuera
- [ ] Auto-inyección de `-orden-publicado-descendente` si falta, con aviso al usuario
- [ ] Cap de 5 URLs enforceado **dentro** de la transacción
- [ ] `label` único; colisión → re-prompt (mapear el 23505, no reventar)

**Verificación:** tests de tabla de hosts maliciosos y de normalización; test de cap concurrente
**Dependencias:** V5.1
**Archivos:** `internal/onboarding/validate.go`, `internal/db/*.go`
**Alcance:** S

### V5.3 Validación profunda con taxonomía y canario
**Descripción:** Distinguir "bloqueado" de "búsqueda vacía" de "se cayó la red" — y no degradar una
URL buena por un fallo de transporte.

**Acceptance criteria:**
- [ ] Challenge (`cf-mitigated`, `Just a moment`, `cf_chl`, 403/503, FS `status != ok`) →
      `pending`/`retrying`
- [ ] Página real con 0 tarjetas → `valid_empty` (no "failed")
- [ ] Error de transporte **nunca degrada** una URL ya `valid`
- [ ] Backoff exponencial ≤24h; `attempts ≤5` → `invalid` + notificación; re-add resetea a `pending`
- [ ] Al pasar a `valid`, dispara el **baseline silencioso** de V1.5; los seeds pasan por el validador
- [ ] Canario: la alerta de cambio de DOM solo si la URL conocida-buena del operador también falla

**Verificación:** tests httptest por cada clase de respuesta; test de backoff; test de canario
**Dependencias:** V5.2, V1.3
**Archivos:** `internal/onboarding/validate.go`, `internal/db/*.go`
**Alcance:** M

### V5.4 Activación pendiente + `/list`
**Descripción:** Honestidad sobre cuándo empiezan las notificaciones.

**Acceptance criteria:**
- [ ] Fin de onboarding avisa que **las notificaciones no arrancan hasta que el operador active al
      usuario** (sin prometer fecha)
- [ ] `/list` muestra `label`, `status` y última revisión ("hace N, sin resultados / error")
- [ ] `/stop` pausa conservando datos; `/start` reanuda
- [ ] `/borrardatos` cascada

**Verificación:** prueba manual del flujo completo en Telegram
**Dependencias:** V5.3
**Archivos:** `internal/onboarding/*.go`, `internal/chat/commands.go`
**Alcance:** S

### Checkpoint V5
- [ ] Una URL de alquiler o de otro dominio se rechaza con mensaje claro
- [ ] Agregar una URL no reproduce su historial
- [ ] El usuario sabe que las notificaciones no arrancan hasta que lo activen

---

## V6: Al 👍 me manda el teléfono

### V6.1 Parser de JSON-LD + `listing_contacts`
**Descripción:** Extraer el teléfono del JSON-LD del detalle, que es HTML estático.

**Acceptance criteria:**
- [ ] Parseo del bloque `application/ld+json` con `@type: Apartment`: `telephone`,
      `numberOfRooms`, `numberOfBedrooms`, `numberOfBathroomsTotal`, `floorSize{value,unitCode}`,
      `address`, `image`
- [ ] `listing_contacts` con `fetched_at` y **TTL de 30 días**
- [ ] "Sin teléfono" es el camino normal, no un error
- [ ] Fixture: `fixtures/detail_ldjson.json`

**Verificación:** test contra el fixture; test de ausencia de teléfono
**Dependencias:** V1.4, V2.2
**Archivos:** `internal/contact/*.go`, `fixtures/detail_ldjson.json`, `migrations/`
**Alcance:** S

### V6.2 Flujo de contacto
**Descripción:** El link va primero; el teléfono es un bonus que puede no llegar.

**Acceptance criteria:**
- [ ] Al 👍: manda el **link inmediato**, luego intenta la extracción
- [ ] **Segundo mensaje solo si se encontró teléfono**
- [ ] Un GET **sin reintentos** con cliente propio; carril prioritario; sin reintento tras challenge
- [ ] Un 500 por challenge deja al usuario con el link igual
- [ ] Si ya existe `label=1`, no se re-ejecuta nada

**Verificación:** prueba manual del flujo; test con challenge simulado
**Dependencias:** V6.1
**Archivos:** `internal/chat/callbacks.go`, `internal/contact/*.go`
**Alcance:** S

### Checkpoint V6
- [ ] Un 👍 manda el link al instante y el teléfono si aparece
- [ ] Un fallo de challenge no deja al usuario sin nada

---

## V7: Preparación de deploy y cierre

> **El despliegue real en la RPi 5 queda fuera de este plan** (decisión del usuario). V7 deja todo
> listo: compose validado, imagen que buildea, runbook escrito, config final. Ejecutar en el Pi se
> planifica aparte cuando haya acceso al host.

### V7.1 Compose final
**Acceptance criteria:**
- [ ] `restart: unless-stopped` en **los tres** servicios
- [ ] Postgres: healthcheck con **`-h 127.0.0.1`** (sin `-h` pasa durante el initdb por socket unix)
- [ ] `extra_hosts: host.docker.internal:host-gateway` en **flaresolverr**, no en el bot
- [ ] FlareSolverr: **solo `PROXY_URL`**, **pinneado en `v3.3.20`** (validar arm64 al desplegar),
      **`mem_limit: 1.5g`**, healthcheck real
- [ ] Bot: `depends_on service_healthy`; logging `json-file` 10m×3 en los tres; `${POSTGRES_*:?}`

**Verificación:** `docker compose config`; `docker compose up -d` → los tres healthy
**Dependencias:** V6.2
**Archivos:** `docker-compose.yml`, `.env.example`
**Alcance:** S

### V7.2 Imagen y runbook
**Acceptance criteria:**
- [ ] `.dockerignore` con `.git`, `.env`, `data/`, `fixtures/`, `bin/`
- [ ] Dockerfile sin `VOLUME /data` ni `ENV DATA_DIR`; sigue `FROM scratch` y `CGO_ENABLED=0`
- [ ] Runbook arranca con `docker compose down --remove-orphans`
- [ ] Rotación de `POSTGRES_PASSWORD` documentada (`ALTER ROLE`)
- [ ] Runbook documenta el **proceso manual de activación** mientras el aviso automático está
      diferido: query de usuarios `active=false AND state='ready'` + el `UPDATE ... SET active=true`

**Verificación:** build de imagen; confirmar que `.env` no entra al contexto; inspeccionar tamaño
**Dependencias:** V7.1
**Archivos:** `.dockerignore`, `Dockerfile`, `README.md`
**Alcance:** S

### V7.3 Config final y código muerto
**Acceptance criteria:**
- [ ] `SEARCH_URLS`, `SEARCH_URLS_FILE`, `CHECK_INTERVAL`, `DATA_DIR` eliminados; `SearchURLs` fuera de `Config`
- [ ] `TELEGRAM_BOT_TOKEN` obligatorio en modo servidor (falla fuerte, no arranca mudo)
- [ ] `internal/store` eliminado; `cmd/probe` toma URLs por argv
- [ ] `internal/bot` y su interfaz `Notifier` reescritos sin los campos viejos
- [ ] Tests actualizados (`config_test`, helpers `cfgFrom`)

**Verificación:** `nix develop -c go build ./...`; `go test ./...`; grep de referencias muertas
**Dependencias:** V7.2
**Archivos:** `internal/config/*`, `internal/store/` (borrar), `cmd/probe/main.go`, `internal/bot/*`, tests
**Alcance:** M

### V7.4 Docs y toolchain
**Acceptance criteria:**
- [ ] README sin la afirmación obsoleta ("Zonaprop acepta la huella TLS de Chrome")
- [ ] README documenta el flujo `/start`, los tres servicios y el baseline silencioso
- [ ] `.env.example` con todos los `POSTGRES_*`, `ZONAPROP_PROXY` y la advertencia de **no** poner
      `HTTP_PROXY` en FlareSolverr
- [ ] Makefile: target de imagen coherente con el nombre de servicio; targets de compose y tests
- [ ] `tasks/archive/v1-zonaprop-bot/` referenciado como superado

**Verificación:** `make fmt vet test`; revisión de que `.env.example` es copiable y arranca
**Dependencias:** V7.3
**Archivos:** `README.md`, `.env.example`, `Makefile`, `flake.nix`
**Alcance:** S

### Checkpoint: Completo
- [ ] `go test ./...` verde y `docker compose config` válido
- [ ] La imagen buildea y el runbook está escrito y revisado
- [ ] **Listo para desplegar.** El deploy en la RPi 5 **no es parte de este plan**: se planifica
      aparte cuando haya acceso al host (levantar contenedores, validar arm64, confirmar la alerta)
- [ ] Revisión humana antes del commit

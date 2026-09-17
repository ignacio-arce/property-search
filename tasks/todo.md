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

### V1.2 Postgres: migrador, seeds y esquema mínimo ✅
**Descripción:** Persistencia con migraciones versionadas y seeds idempotentes. Solo las tablas que
V1 usa; el resto se agrega cuando su rebanada lo pida.

**Acceptance criteria:**
- [x] Pool `pgx` con **retry acotado** al arrancar (cubre el race del healthcheck)
- [x] Migrador versionado embebido (`go:embed` + `schema_migrations`), idempotente
- [x] Seeds: settings globales + usuario con `SEED_CHAT_ID` y sus `SEED_URLS` (formato de V1.1)
- [x] Esquema: `users` (con `active` y `active_model_version`), `search_urls`, `listings` (con
      `recency_rank`), `listing_sources`, `deliveries`, `settings`
- [x] Tests de integración contra Postgres real, con `t.Skip` si no está disponible (**desvío:** se
      usa el Postgres de compose en `127.0.0.1:5432` en vez de testcontainers, para no arrastrar
      docker/moby al `go.mod`; `dbtest.NewPool` crea una base descartable por test)

**Verificación:**
- [x] `go test -count=1 ./internal/db/... ./internal/repo/...` verde y **sin skips**
- [x] Migraciones + seeds corridos **dos veces** seguidas sin error
- [x] Aislamiento: `TestSearchURLsAreScopedPerUser` — A no ve las URLs de B
- [x] `go test -count=1 ./...` verde, `gofmt` y `vet` limpios

**Dependencias:** V1.1
**Archivos:** `internal/db/*.go` (+ `internal/db/migrations/`), `internal/repo/*.go`,
`internal/searchurl/*.go`, `internal/dbtest/*.go`, `go.mod`, `docker-compose.yml` (puerto loopback)
**Alcance:** M

### V1.3 Fetch: `Result{Status,Header}`, FlareSolverr obligatorio, budget ✅
**Descripción:** El camino de red completo con el freno de mano puesto. Es la tarea de mayor riesgo.

**Acceptance criteria:**
- [x] `Result` gana `Status` y `Header`; `fetchViaTLS` **propaga** status/headers en vez de colapsar
      todo non-200 a un error genérico
- [x] Timeout del cliente de FS = `MaxBrowserTimeout + 30s` (90s) y **`maxTimeout` = 60s**;
      **FS se intenta UNA sola vez** por fetch (0 reintentos — más estricto que el ≤1 pedido: cada
      intento lanza un browser)
- [x] Cadena FS → proxy → directo, conservando `TestFlareSolverrFallsBackToTLS`
- [x] Budget: `Gate` con 1 req/60s (`FETCH_RATE_LIMIT`), semáforo 1 con FS, carril prioritario y
      cooldown de 5 min tras un challenge; el pacing aplica **solo a `zonaprop.com.ar`**, así las
      fotos del CDN y los servidores de test no consumen el presupuesto
- [x] Clasificación tipada: `fetch.Error{Kind,Status,Header}` con `KindTransport` / `KindBlocked` /
      `KindHTTPStatus` y `KindOf(err)`
- [x] `errNoMode` eliminado

**Verificación:**
- [x] `go test -count=1 ./internal/fetch/...` verde
- [x] Diagnóstico real: `403 + cf-mitigated: challenge` → `KindBlocked` y **no se reintenta**
      (1 solo hit, contra `FETCH_RETRIES=3`); 403 pelado → reintentable; conexión rechazada →
      `KindTransport`; `Header` propagado
- [~] `make probe` contra la URL real: **corre y clasifica bien** (`kind=blocked`, status 500,
      `Error solving the challenge`) y **no reintenta**. Las ~30 tarjetas **no se pudieron
      verificar**: la IP sigue degradada por Cloudflare desde las sondas (A1). Es condición de red,
      no del código — el mismo probe dio 30 tarjetas en A1.
- [x] Verificación de "200 con 0 tarjetas → vacío" movida a V1.4/parser (es nivel parser, no fetch)

**Dependencias:** V1.1
**Archivos:** `internal/fetch/{fetch,tls,flaresolverr,gate}.go` + tests, `internal/config/config.go`
(`FETCH_RATE_LIMIT`), `docker-compose.yml` (FS en loopback), `.env.example`
**Alcance:** M

### V1.4 Parser de card tipado ✅
**Descripción:** Features reales y tipadas sobre el fixture real, con las correcciones que salieron
de la sonda (outliers, m² cub. inexistente, dorm constante).

**Acceptance criteria:**
- [x] `model.Listing` tipado: `ZonapropID`, `CanonicalURL`, `PriceAmount *int64`, `Currency`,
      `M2Tot`/`M2Cub *float64`, `M2Basis`, `Rooms`/`Dorm`/`Banos *int`, `Expensas *int64`, `Operation`
      (`Rooms` se conserva para mostrar; no puntúa)
- [x] **`Operation` se detecta del path** (`alquiler`/`venta`) y **`Currency` del prefijo del precio**
      (`USD` vs `$`→ARS)
- [x] `Parse` deriva el **origin** de `siteBase` + test de regresión
      (`TestCanonicalURLUsesOriginNotTheSearchPath`)
- [x] `parsePrice` cubre `USD 144.000`, `$ 150.000.000`, `Desde USD …`, `Consultar precio` **y un
      alquiler mensual en ARS** (`$ 450.000`)
- [x] Guarda de outliers `m² ∈ [10,1000]`; ausentes → NULL, **nunca 0** (`TestMissingValuesStayNil`)
- [x] `Partido()`: último segmento, o el primero si es macrozona (`San Isidro, GBA Norte` → `San Isidro`)
- [x] `ParseWithStats` expone `Cards`/`SkippedType`/`SkippedNoID`: un cambio de DOM se cuenta en vez
      de perderse en silencio

**Verificación:**
- [x] `go test -count=1 ./internal/parser/...` verde contra `fixtures/search_gba_norte.html`
      (30/30 tarjetas, 0 saltos, valores exactos de la tarjeta 1: id 60170922, USD 144.000, 69 m² tot.,
      3 amb., 2 dorm., 1 baño, expensas 180.000, Florida/Vicente López)
- [x] `go test -count=1 ./...` verde, `gofmt` y `vet` limpios

**Dependencias:** V1.1
**Archivos:** `internal/model/*` (incluye `model_test.go` nuevo), `internal/parser/*`.
**Adaptadores mínimos** que el cambio de modelo obligó (el plan lo anticipaba): `internal/store`
(usa `ZonapropID`/`CanonicalURL`), `internal/telegram` (caption con `PriceLabel`/`SizeLabel`),
`internal/bot`, `cmd/probe` y sus tests. Se **eliminó `fixtures/sample.html`** (sintético y
engañoso: se lo llamaba "DOM real" sin serlo); el probe ahora escribe `probe_capture.html`.
**Alcance:** M

### V1.5 Dedup + baseline silencioso + envío con botones ✅
**Descripción:** Cierra el camino: candidatas acotadas al usuario, baseline que evita reproducir el
historial, y envío con teclado inline.

**Acceptance criteria:**
- [x] Candidatas = `NOT EXISTS(deliveries)` **y** acotadas por `listing_sources` a las URLs del
      usuario (sin esto se filtran publicaciones entre usuarios) — `TestCandidatesAreScopedToTheUsersOwnSearches`
- [x] Baseline silencioso: la **primera indexación exitosa** de una búsqueda inserta `deliveries` de
      toda la página sin enviar (`status='baseline'`), sin necesidad de estado extra
- [x] `Notify(ctx, chatID, listingID, listing)` con keyboard 👍/👎 y `callback_data` `u:<id>` / `d:<id>`
- [x] Caption presupuestado en **UTF-16** reservando la URL + el `\n`; fallback a `sendMessage` ante
      un 400 (pero **no** ante 429/red, que duplicaría el mensaje)
- [x] Dedup por `zonaprop_id`, nunca por URL
- [x] `fetch.Result.Status/Header` aprovechado por el digest: `Stats` de saltos se loguea

**Verificación:**
- [x] `go test -count=1 ./internal/digest/...` con **Postgres real** (dbtest) + fakes de red
- [x] **Corrección de la AC original:** la primera corrida **no manda nada** — baselina. Manda a
      partir de la segunda, y solo lo nuevo. La AC decía "primera corrida manda", escrita antes de
      que el baseline se decidiera; se corrige acá y en V1.6.
- [x] Re-ejecutar no re-manda (`TestRepeatedRunIsSilent`), que es lo que hace seguro un reinicio
- [x] Una búsqueda que falla no aborta el ciclo (`TestFailingSearchDoesNotAbortTheCycle`)
- [x] Usuario inactivo no se procesa ni se fetchea (`TestInactiveUsersAreSkipped`)
- [x] `go test -count=1 ./...` verde, `gofmt` y `vet` limpios

**Dependencias:** V1.2, V1.3, V1.4
**Archivos:** `internal/digest/*` (nuevo), `internal/repo/{listings,deliveries,users}.go`,
`internal/model/snapshot.go`, `internal/telegram/telegram.go` (reescrito), `cmd/bot/main.go`
(recableado a la pila nueva), tests.
**Decisión adelantada:** el gate `users.active` se implementó **acá** (`ListActiveUsers`) en vez de
en V4.3, para que no exista una ventana en la que un desconocido reciba correo. V4.3 queda con el
tope diario y el monitoreo de cobertura. El seed marca al operador como `active = true`.
**Alcance:** M

### V1.6 Verificación E2E ✅ (con una salvedad de entorno)
**Descripción:** Prueba de punta a punta con el usuario sembrado.

**Acceptance criteria:**
- [x] Dry-run: 1ª corrida **baselina en silencio**, la 2ª manda solo lo nuevo
- [~] Telegram real: **NO verificable acá** — no hay token disponible. Queda para el operador
      (igual que el deploy del Pi). El envío en sí está cubierto por los tests httptest de telegram:
      multipart con foto, caption, teclado y revocación.
- [x] Los logs muestran el rate respetado: `rate=1m0s` en el arranque y el ciclo tardó
      **1m0.62s** (un solo request, paced)

**Verificación:**
- [x] `TestEndToEndAgainstTheRealPage`: el pipeline completo (config → Postgres → migraciones →
      fetch real → parser → indexado → baseline → deliveries) contra
      `fixtures/search_gba_norte.html` servido por HTTP local. **30 publicaciones indexadas,
      0 enviadas (baselinadas), segunda corrida silenciosa e idempotente.** No toca Zonaprop ni
      Telegram: hermético y repetible.
- [x] `make run` con el env real: arranca, migra, siembra (`user_id=999 active=true`),
      clasifica el bloqueo de Cloudflare como `kind=blocked`, **no aborta el ciclo** y cierra
      limpio con SIGTERM.
- [x] Estado en la DB verificado a mano: usuario activo, 0 listings (fetch bloqueado), y
      `settings` con `daily_hour=09:00` y `max_daily=15`.

**Limitación honesta:** Zonaprop sigue bloqueando esta IP desde las sondas (A1), así que el camino
real de 30 tarjetas no se pudo ejercitar contra el sitio. Está cubierto por el fixture real +
el E2E. La única verificación que falta de verdad es una tarjeta llegando a Telegram.

**Dependencias:** V1.5
**Archivos:** `internal/digest/e2e_test.go` (nuevo)
**Alcance:** S

### Checkpoint V1
- [x] Un usuario sembrado no recibe el inventario inicial: se baselina, y después recibe solo lo nuevo
- [x] Reiniciar el proceso no re-manda nada (E2E idempotente)
- [x] El budget de fetch se respeta (`rate=1m0s`, un request por ciclo)
- [~] **Revisión humana pendiente** — y queda sin verificar la llegada real a Telegram (sin token)

---

## V2: Califico y el bot lo persiste ✅

### V2.1 Telegram inbound: long-poll, offset, dead-letter, lease ✅
**Descripción:** El bot pasa de notificador a chat bot. Es la base de todo lo interactivo.

**Acceptance criteria:**
- [x] `getUpdates` long-poll con **cliente dedicado** (`PollTimeout` 50s + 20s de margen)
- [x] `bot_state.last_update_id` persistido **después** del handler (no en su transacción: los
      handlers son idempotentes y un crash replayea en vez de perder)
- [x] Dead-letter tras `maxAttemptsPerUpdate=3`: persiste el offset y lo loguea como DEAD-LETTER
- [x] Lease de poller (no advisory lock sino una fila con expiración en `bot_state`): una segunda
      instancia **se niega a arrancar** en vez de robar updates
- [x] Un update que falla **no reordena** los siguientes: se corta el batch, se reintenta con
      backoff y recién después de 3 intentos se avanza

**Verificación:**
- [x] `TestPollerPersistsOffset` (el offset queda persistido tras manejar el update)
- [x] `TestPoisonUpdateIsDeadLettered` (un handler que siempre falla no traba el loop)
- [x] `TestPollLeaseIsExclusive` + `TestPollerRefusesToStealUpdates`
- [x] El fake de `getUpdates` modela el comportamiento real: **re-entrega** los pendientes mientras
      el offset no avance (el primer fake entregaba batches secuenciales y el test pasaba por la
      razón equivocada)

**Dependencias:** V1
**Archivos:** `internal/chat/*` (nuevo), `internal/telegram/updates.go`, `migrations/0002_*`,
`internal/repo/ratings.go`, `cmd/bot/main.go`
**Alcance:** M

### V2.2 Callbacks: respuesta, revocación, first-tap-wins ✅
**Descripción:** Que un tap se sienta bien y no ensucie los datos.

**Acceptance criteria:**
- [x] **Siempre** `answerCallbackQuery`, en todos los caminos (incluido el desconocido)
- [x] `ClearRatingKeyboard` revoca el teclado tras el primer tap
- [x] **First-tap-wins**: un segundo tap responde "Ya la calificaste" sin side effects
- [x] Tap tardío (>14 días) → "Esa tarjeta expiró" + revocación; **nunca** dice "guardado"
- [x] Un callback de una publicación que nunca se entregó se rechaza ("Esa publicación no es tuya")

**Verificación:** `TestCallbackRecordsRating`, `TestSecondTapIsAcknowledgedWithoutSideEffects`,
`TestLateTapExpires`, `TestCallbackForUndeliveredListingIsRejected`,
`TestUnknownCallbackDataIsAnswered`, `TestGroupChatIsRefused`
**Dependencias:** V2.1
**Archivos:** `internal/chat/bot.go`, `internal/telegram/updates.go`, tests
**Alcance:** M

### V2.3 `ratings` + `deliveries.status` ✅
**Descripción:** Persistir la calificación de forma atómica y con ciclo de vida.

**Acceptance criteria:**
- [x] `ratings` con `ON CONFLICT`, `UNIQUE(user_id, listing_id)`
- [x] `deliveries.status` con ciclo de vida (`pending|sent|failed|dead|baseline|rated`) + `attempts`
- [x] Escritura de rating y estado de la entrega en **una transacción** (`RecordRating`)
- [x] La entrega se valida antes de aceptar el rating (rechaza callbacks forjados)

**Verificación:** `TestCallbackRecordsRating` (ratings + status en la misma operación);
`TestCallbackForUndeliveredListingIsRejected` (no queda fila de rating)
**Pendiente:** la reconciliación de `message_id IS NULL` necesita que el notifier devuelva el
`message_id`, que llega junto con la revocación real en V2.2/V6. Anotado, no perdido.
**Dependencias:** V2.2
**Archivos:** `migrations/0002_*`, `internal/repo/ratings.go`, `internal/chat/bot.go`
**Alcance:** S

### V2.4 `/model` ✅ (conteos; los buckets llegan en V3)
**Descripción:** Primera superficie de transparencia. Solo lo que ya se puede calcular.

**Acceptance criteria:**
- [x] `/model` muestra cuántas calificaciones hay (👍/👎) y **no** promete más
- [x] **No** muestra métricas que todavía no se pueden computar: dice "todavía aprendiendo: N de 30"
- [x] Los conteos **por bucket** requieren `model_weights`, que es V3.1; se agregan en V3.3

**Verificación:** `TestModelCommandRepliesWithCounts`, `TestModelTextDoesNotPretendBelowThreshold`
**Dependencias:** V2.3
**Archivos:** `internal/chat/bot.go`
**Alcance:** S

### Checkpoint V2
- [x] Un tap produce exactamente un rating, con callback respondido y keyboard revocado
- [x] Un restart no pierde ni duplica updates (offset persistido + lease)

---

## V3: Reordena por lo aprendido y explica ✅

### V3.1 Buckets + score literal ✅
**Descripción:** La función de score, escrita de una única manera implementable.

**Acceptance criteria:**
- [x] Buckets categóricos **sin particionar**: `partido`, `banios`
- [x] Buckets numéricos **namespaciados por `operation_type:currency`**
      (`ppm2:venta:USD:1500-1750`), con paso por namespace: `venta:USD` = 250 lineal
      (validado por A1), cualquier otro namespace con **bins logarítmicos** (razón 1.12)
- [x] **`operation_type` y `currency` NO son buckets de scoring**, son claves de partición
- [x] `ppm2` sobre `m2_tot` (A1 midió 0 apariciones de `m² cub.`); `m2_bin` de 10 m²
- [x] **`currency` vacío/desconocido ⇒ ninguna feature numérica** (guarda contra el outlier de 1000×)
- [x] `rooms` y `dorm` **fuera del scoring**; ausentes → `unknown`, nunca 0
- [x] `rate = (ups+1)/(ups+downs+2)`; `score = mean(ln(rate/0.5))` sobre los buckets **con
      evidencia** (uno desconocido no diluye el promedio)
- [x] Orden por `score DESC` y, en empates, el orden de recencia de la consulta
      (`sort.SliceStable` sobre `first_indexed_at DESC, recency_rank ASC`)

**Verificación:** `TestExtractNamespacesNumericFeatures`, `TestExtractOmitsNumericWhenCurrencyUnknown`,
`TestExtractUsesUnknownNeverZero`, `TestScoreIsZeroWithoutEvidence`, `TestScoreAveragesKnownBuckets`
**Dependencias:** V1.4, V2.3
**Archivos:** `internal/score/{features,weights}.go`, `migrations/0003_*`
**Alcance:** M

### V3.2 Retrain por usuario con versión atómica ✅
**Descripción:** Aprender sin pisar el score que se está leyendo.

**Acceptance criteria:**
- [x] Retrain **por usuario**, cada uno en su propia transacción, log-and-continue
- [x] `active_model_version` volteado **dentro** de la misma transacción que escribe los pesos
- [x] Entrena con el **snapshot de la entrega**, no con el `features` actual: si el vendedor cambia
      el precio, el modelo aprende de lo que el usuario realmente vio
- [x] Prune de versiones no referenciadas por `deliveries`

**Verificación:** `TestRetrainBuildsAndActivatesANewGeneration` (v1 → v2, buckets con los ups/downs
correctos), `TestRetrainWithNoRatingsStillActivatesAnEmptyModel`
**Dependencias:** V3.1
**Archivos:** `internal/score/retrain.go`, `internal/repo/weights.go`
**Alcance:** M

### V3.3 Razones n≥3 + concordancia ✅
**Descripción:** La parte que hace confiable al modelo… o que se calla si no tiene datos.

**Acceptance criteria:**
- [x] Razones: top-2 buckets con `(ups+downs) ≥ 3`, ordenadas por `|rate-0.5|`, **en español**
- [x] Si ningún bucket llega a n≥3 → **no se muestra ninguna razón**
- [x] `/model`: concordancia pairwise **intra-día** sobre 30 días, anclada en `ratings.created_at`
- [x] Suprimida con n<30, con mensaje "todavía no medible (N de 30)"
- [x] La tarjeta muestra el **ranking** ("#2 de 14 hoy") y hasta **2 razones**; nunca un porcentaje
- [x] `/model` lista los buckets con más evidencia (`BucketSummary`)

**Verificación:** `TestReasonsNeedMinimumSupport`, `TestReasonsAreOrderedByStrengthAndCapped`,
`TestBucketSummaryOrdersByEvidence`, `TestAgreementNeedsEnoughSamples`,
`TestSummaryTextIsHonestAboutWhatIsKnown`
**Dependencias:** V3.2
**Archivos:** `internal/score/{weights,retrain}.go`, `internal/digest/digest.go`,
`internal/telegram/telegram.go`, `internal/chat/bot.go`
**Alcance:** M

### Checkpoint V3
- [x] Con <30 ratings la UI no inventa razones ni porcentajes
- [x] El orden con empates es estable y favorece lo más nuevo

---

## V4: Corre solo todos los días ✅

### V4.1 Scheduler tz-aware ✅
**Descripción:** Disparar a las 09:00 **de Buenos Aires**, no a las 06:00 por UTC.

**Acceptance criteria:**
- [x] `import _ "time/tzdata"` en `cmd/bot/main.go` (la imagen `scratch` no trae zoneinfo)
- [x] `SCHEDULE_TZ` con default explícito `America/Argentina/Buenos_Aires`; **vacío no es error**,
      se usa el default; un valor no resoluble **falla al arrancar**
- [x] `DAILY_HOUR` ("HH:MM", default 09:00) validado; `NextRun` es una **función pura** testeable
- [x] **Sin corrida al arrancar** (`RUN_ON_START=false` por defecto): con `restart: unless-stopped`
      un ciclo en cada reinicio sería un digest fuera de horario

**Verificación:** `TestNextRunBeforeTheHourIsToday`, `TestNextRunAfterTheHourIsTomorrow`,
`TestNextRunExactlyAtTheHourIsTomorrow`, `TestNextRunRespectsTheZone` (el test que atrapa el bug de
tzdata: 09:00 ART = 12:00 UTC), `TestNextRunHonoursMinutes`
**Dependencias:** V3.3
**Archivos:** `internal/scheduler/*` (nuevo), `internal/config/config.go`, `cmd/bot/main.go`
**Alcance:** S

### V4.2 `digests` + resume auto-sanante ✅
**Descripción:** Que un día interrumpido no se pierda ni se duplique.

**Acceptance criteria:**
- [x] Fila `digests(user_id, run_date, status)` creada **al agendar** para cada usuario activo
- [x] Input del digest = **todas las no entregadas** de las URLs del usuario (no "lo de hoy"), que
      es lo que hace que reanudar sea correcto y no un duplicado
- [x] Al arrancar se procesan los runs sin terminar (`PendingDigests` con `run_date <= hoy`)
- [x] `FinishDigest` registra `sent` y el error; un run con error queda reanudable

**Verificación:** `TestRunDailyRecordsAndDoesNotRepeat`, `TestUnfinishedDayIsResumed`
**Dependencias:** V4.1
**Archivos:** `internal/digest/digest.go`, `internal/repo/digests.go`, `migrations/0004_*`
**Alcance:** M

### V4.3 Tope diario y cobertura ✅
**Descripción:** El límite del portal y el monitoreo del techo de 30.

**Acceptance criteria:**
- [x] El digest solo recorre `active = true` **y** `state != 'stopped'` (implementado en V1.5)
- [x] Tope diario de **15 envíos** (`MAX_DAILY`), con el excedente **diferido, no descartado**:
      las publicaciones que sobran siguen sin entregar y salen al día siguiente
- [x] `last_card_count` y `last_fetch_status` persistidos por búsqueda
- [x] Alerta en el log cuando una búsqueda devuelve la página llena (30), porque es saturación del
      portal y no un bug

**Verificación:** `TestDailyCapDefersInsteadOfDropping`, `TestActiveGateSkipsInactiveUsersInRunDaily`
**Dependencias:** V4.2
**Archivos:** `internal/digest/digest.go`, `internal/repo/digests.go`, `internal/config/config.go`
**Alcance:** S

### Checkpoint V4
- [x] El digest corre solo para `active=true` y reanuda tras un restart a mitad
- [x] Un día interrumpido no se pierde

---

## V5: `/start` para desconocidos ✅

### V5.1 Máquina de estados + comandos ✅
**Descripción:** El alta conversacional, sin condiciones de carrera.

**Acceptance criteria:**
- [x] Transiciones explícitas `idle → await_url → await_label → ready` (+ `stopped`)
- [x] **Un escritor serializado por usuario**: `lockUser` toma un mutex por `user_id`, porque el
      poller y el scheduler corren concurrentes
- [x] Entrada no reconocida **responde**, no se descarta; un mensaje suelto que no es URL recibe guía
- [x] Solo chat privado; en grupo se rechaza con mensaje claro
- [x] Comandos: `/start`, `/addurl`, `/rmurl <label>`, `/list`, `/model`, `/stop`, `/borrardatos`, `/help`

**Verificación:** `TestOnboardingRegistersASearchAndInjectsRecencyOrder`,
`TestListShowsStatusAndLabels`, `TestStopAndBorrardatos`, `TestRemoveURLNeedsAKnownLabel`
**Dependencias:** V2.2
**Archivos:** `internal/chat/onboarding.go` (nuevo), `internal/chat/bot.go`, `internal/repo/onboarding.go`
**Alcance:** M

### V5.2 Validación sintáctica, normalización y cap ✅
**Descripción:** Cerrar el SSRF y evitar fetches duplicados.

**Acceptance criteria:**
- [x] `https` obligatorio **y** host exactamente `zonaprop.com.ar` o sufijo `.zonaprop.com.ar`
      (`searchurl.IsZonapropURL`); rechaza `notzonaprop.com.ar`, `zonaprop.com.ar.attacker.test` y `http`
- [x] `url_norm`: host en minúsculas, query ordenado, tracking fuera
- [x] **Auto-inyección de `-orden-publicado-descendente`** con aviso al usuario
- [x] Cap de **5 URLs** enforceado **dentro de la transacción**, con `FOR UPDATE` sobre el usuario
      para que dos mensajes concurrentes no pasen los dos
- [x] `label` único por usuario; colisión → re-prompt y el usuario **queda en `await_label`** para
      poder responder otro nombre (no se pierde el estado)

**Verificación:** `TestOnboardingRejectsNonZonapropURL`, `TestOnboardingEnforcesTheSearchCap`,
`TestOnboardingDuplicateLabelReprompts`, `TestOnboardingDeduplicatesOnNormalizedURL`
**Dependencias:** V5.1
**Archivos:** `internal/searchurl/normalize.go`, `internal/repo/onboarding.go`, `internal/chat/onboarding.go`
**Alcance:** S

### V5.3 Validación profunda con taxonomía y canario ✅
**Descripción:** Distinguir bloqueado / vacío / transporte, y no degradar una URL buena.

**Acceptance criteria:**
- [x] Challenge (`fetch.KindBlocked`) → `retrying` con backoff; **nunca** degrada a inválida
- [x] Error de transporte → `retrying`: una caída de red no es un veredicto sobre la URL
- [x] Página real con 0 tarjetas → `valid_empty` (sin más chequeos programados)
- [x] Backoff exponencial con techo de 24 h; `attempts ≥ 5` → `invalid` + **notificación al usuario**
- [x] **Sin reintentos internos** durante la validación (`fetch.Options.NoRetries`): 5 intentos × 4
      reintentos serían 20 golpes a una página de challenge desde una IP ya sospechada
- [x] **Canario**: si *todas* las búsquedas del pase vuelven vacías, se loguea como probable cambio de
      DOM en vez de "búsquedas vacías"
- [x] El baseline silencioso lo hace el digest solo: una URL recién validada tiene 0
      `listing_sources`, así que su primera indexación se baselina sin código extra

**Verificación:** `TestValidPageBecomesWatched`, `TestChallengeIsRetriedNotRejected`,
`TestTransportFailureIsRetried`, `TestEmptyPageIsValidButEmpty`, `TestGivesUpAfterMaxAttempts`,
`TestNotDueIsNotFetched`
**Dependencias:** V5.2, V1.3
**Archivos:** `internal/validate/*` (nuevo), `internal/fetch/fetch.go` (`NoRetries`),
`internal/repo/{onboarding,repo}.go`, `cmd/bot/main.go`
**Alcance:** M

### V5.4 Activación pendiente + `/list` ✅
**Descripción:** Honestidad sobre cuándo empiezan las notificaciones.

**Acceptance criteria:**
- [x] Fin de onboarding avisa que **no hay notificaciones hasta que el operador active** al usuario
- [x] Avisa también el **límite de cobertura**: solo se ve la primera página (~30 más nuevas)
- [x] `/list` muestra `label`, estado traducido (`vigilando` / `validando…` / `hoy sin resultados`) y la URL
- [x] `/stop` pausa conservando datos; `/start` reanuda
- [x] `/borrardatos` cascada

**Verificación:** incluidas en `TestOnboardingRegistersASearchAndInjectsRecencyOrder` (los dos avisos)
y `TestStopAndBorrardatos`
**Dependencias:** V5.3
**Archivos:** `internal/chat/onboarding.go`
**Alcance:** S

### Checkpoint V5
- [x] Una URL de otro dominio o `http` se rechaza con mensaje claro
- [x] Agregar una URL no reproduce su historial (baseline en la primera indexación)
- [x] El usuario sabe que las notificaciones no arrancan hasta que lo activen

**Bug encontrado y corregido acá:** `SetUserState` era un `UPDATE` que no hacía nada si el usuario
no existía, así que `/addurl` sin `/start` quedaba en silencio y el bot respondía como si hubiera
funcionado. Ahora devuelve error si no afectó filas, y `/addurl` asegura el usuario primero.

---

## V6: Al 👍 me manda el teléfono ✅

### V6.1 Parser de JSON-LD + `listing_contacts` ✅
**Descripción:** Extraer el teléfono del JSON-LD del detalle, que es HTML estático.

**Acceptance criteria:**
- [x] Parseo de los bloques `application/ld+json` buscando el `Apartment`/`House`; un bloque
      corrupto no corta el escaneo
- [x] `telephone` normalizado a dígitos con `+` inicial; menos de 8 dígitos se descarta
- [x] **No hay email**: la página real solo tiene placeholders y direcciones de Zonaprop, así que la
      feature manda **teléfono o nada**
- [x] Bonus del mismo bloque: `streetAddress` y `addressRegion` (barrio), que el card no expone
- [x] `listing_contacts` con **TTL de 30 días**; un resultado vacío también se cachea, para que un
      futuro 👍 no gaste otro request
- [x] Fixture: `fixtures/detail_ldjson.json`

**Verificación:** `TestFromJSONLDReadsTheListingBlock`, `TestFromJSONLDWithoutAListingBlock`,
`TestNormalizePhone`
**Dependencias:** V1.4, V2.2
**Archivos:** `internal/contact/contact.go`, `internal/repo/contacts.go`, `migrations/0005_*`,
`internal/model/snapshot.go` (`model.Contact`, movido ahí para no crear un ciclo repo↔contact)
**Alcance:** S

### V6.2 Flujo de contacto ✅
**Descripción:** El link va primero; el teléfono es un bonus que puede no llegar.

**Acceptance criteria:**
- [x] El 👍 **responde primero** y después trabaja: el fetch del detalle tarda segundos y el cliente
      del usuario se rinde mucho antes
- [x] La extracción corre **destacada** (`context.WithoutCancel` + goroutine), así una página lenta
      no bloquea el loop de updates de los demás
- [x] **Segundo mensaje solo si hay teléfono**; "no encontrado" es el camino normal
- [x] Un GET con `Priority: true` y `NoRetries: true`
- [x] Un challenge en el detalle **no rompe nada**: el rating ya está registrado y el link ya estaba
      en la tarjeta
- [x] Si ya existe `label=1`, no se re-ejecuta nada (first-tap-wins de V2.2)

**Verificación:** `TestOnLikeSendsThePhoneFromTheDetailPage`, `TestOnLikeWithoutAPhoneSaysNothing`,
`TestOnLikeUsesTheCacheInsteadOfRefetching`, `TestOnLikeSurvivesADetailFetchFailure`
**Dependencias:** V6.1
**Archivos:** `internal/contact/extractor.go`, `internal/chat/bot.go`, `cmd/bot/main.go`
**Alcance:** S

### Checkpoint V6
- [x] Un 👍 manda el link al instante (ya estaba en la tarjeta) y el teléfono si aparece
- [x] Un fallo de challenge no deja al usuario sin nada

**Pendiente de verificación real:** que el `telephone` del JSON-LD **varíe por publicación**. La
sonda A2 no pudo medir un segundo detalle (challenge). Si resultara un número genérico, V6 se degrada
a "manda solo el link" sin romper nada.

---

## V7: Preparación de deploy y cierre ✅

> **El despliegue real en la RPi 5 queda FUERA de este plan.** V7 deja todo listo para desplegar.

### V7.1 Compose final ✅
**Acceptance criteria:**
- [x] `restart: unless-stopped` en **los tres** servicios
- [x] Postgres: healthcheck con **`-h 127.0.0.1`**
- [x] `extra_hosts: host.docker.internal:host-gateway` en **flaresolverr**, no en el bot
- [x] FlareSolverr: **solo `PROXY_URL`**, pinneado en `v3.3.20`, `mem_limit: 1536m` (sized para el
      semáforo 1), healthcheck real vía python (la imagen no garantiza `curl`)
- [x] El bot espera `service_healthy` de los dos, y ya no monta `./data:/data`
- [x] Logging `json-file` 10m×3 en los tres: sin rotación llenan la SD del Pi
- [x] `${POSTGRES_*:?required}`

**Verificación:** `docker compose config` válido
**Dependencias:** V6.2
**Alcance:** S

### V7.2 `.dockerignore` + Dockerfile + runbook ✅
**Acceptance criteria:**
- [x] `.dockerignore` con `.git`, `.env`, `data/`, `fixtures/`, `tasks/`, `docs/`, `bin/`
- [x] Dockerfile sin `VOLUME /data` ni `ENV DATA_DIR`; sigue `FROM scratch` y `CGO_ENABLED=0`
- [x] El runbook arranca con `docker compose down --remove-orphans`
- [x] Rotación de `POSTGRES_PASSWORD` documentada (`ALTER ROLE`)

**Verificación:** revisión del contexto de build (`.env` excluido)
**Dependencias:** V7.1
**Alcance:** S

### V7.3 Config final y código muerto ✅
**Acceptance criteria:**
- [x] `SEARCH_URLS`, `SEARCH_URLS_FILE`, `CHECK_INTERVAL`, `DATA_DIR` eliminados
- [x] `internal/store` e **`internal/bot`** eliminados (el bot v1, reemplazado por `internal/digest`)
- [x] `cmd/probe` toma las URLs por **argv** (`-out` opcional) en vez de leer un env global
- [x] `repo.SearchURLsForBaseline` eliminado (código muerto de V1.2)
- [x] Tests actualizados (`config_test`)

**Verificación:** `go build ./...`, `go test -count=1 ./...` y `go vet ./...` limpios
**Dependencias:** V7.2
**Alcance:** M

### V7.4 Docs y toolchain ✅
**Acceptance criteria:**
- [x] README **sin** la afirmación obsoleta ("Zonaprop acepta la huella TLS de Chrome"): ahora
      explica que FlareSolverr es obligatorio y **por qué**
- [x] README documenta el flujo `/start`, los tres servicios, el baseline silencioso, el activado
      manual de usuarios y el runbook
- [x] README declara explícitamente que **el deploy en el Pi no está hecho** y cuáles son los límites
      conocidos
- [x] `.env.example` con todas las variables y la advertencia de **no** poner `HTTP_PROXY` en
      FlareSolverr
- [x] Makefile con targets de compose, probe por URL y cross-build arm64
- [x] `flake.nix` sin cambios (el devShell Go ya alcanza)

**Verificación:** `make fmt vet test`; `docker compose config`
**Dependencias:** V7.3
**Alcance:** S

### Checkpoint: Completo
- [x] `go test -count=1 ./...` verde y `docker compose config` válido
- [x] La imagen buildea (validado el cross-build arm64) y el runbook está escrito
- [x] **Listo para desplegar.** El deploy en la RPi 5 **no es parte de este plan**
- [x] Revisión humana antes del commit

---



---

## Hallazgos de la verificación en vivo (con el bot real y Telegram real)

La verificación con token real destapó cosas que ningún test unitario iba a encontrar. Todas
corregidas y con test de regresión:

| Hallazgo | Qué pasaba | Fix |
|---|---|---|
| **Pairing de credenciales** | Con token y sin `TELEGRAM_CHAT_ID` el bot **se negaba a arrancar**, que es exactamente la configuración de producción (el destinatario sale de la base). Mi propia AC de V7.3 pedía relajarlo y solo lo había hecho en `dryRun()`. | Se eliminó la regla; el chat id es solo un destino por defecto para dev. |
| **Errores de red clasificados como challenge** | FlareSolverr envuelve todo como *"Error solving the challenge. Message: net::ERR_CONNECTION_REFUSED"*, y el código lo marcaba `blocked` por la palabra "challenge". Se aplicaba cooldown por un problema que no existía. | `classifyFlareSolverrFailure`: si el mensaje envuelto es de red → `transport`. |
| **Día sin lectura marcado como hecho** | Si ninguna búsqueda se podía leer, el día quedaba `done` y no se reintentaba. | `RunForUser` devuelve error cuando no pudo leer **ninguna** búsqueda; el día queda retryable. Un fallo parcial sigue siendo "hecho" (lo que falta sale mañana). |
| **El link apuntaba a localhost** | El link de la tarjeta sale del origin de la página fetcheada; al servir el fixture desde HTTP local, el link heredó `127.0.0.1`. | Es del andamiaje de prueba, no del producto: el fixture servido ahora usa hrefs **absolutos** de Zonaprop. |
| **"Título" = descripción completa** | `POSTING_CARD_DESCRIPTION` trae la descripción entera (miles de caracteres), y ocupaba el caption empujando fuera precio, m² y ubicación. | El título sale del `alt` de la galería (resumen corto y estructurado), con fallback a la descripción truncada. Además el título se acota a 100 unidades en el caption. |
| **Sin constancia del tap** | El teclado se revocaba pero el único feedback era un toast efímero, fácil de perder. El plan pedía editar el caption y yo solo había hecho la revocación. | `editMessageCaption` agrega "✓ te gustó" / "✗ no te gustó", y el callback se **responde antes** de escribir en la base para que el aviso no llegue tarde. |
| **`operation_type` vacío** | Se derivaba solo del path de la búsqueda; una URL sin "venta"/"alquiler" dejaba al usuario sin features numéricas. | Fallback al slug de cada publicación. |

**Verificado en vivo con Telegram real:** llegada de la tarjeta (foto, precio, m², ubicación,
botones), baseline silencioso (29 publicaciones sin enviar), envío de la publicación nueva (real, id
60124075, con link al portal), calificación con `✓ te gustó` en la tarjeta, y `/model` respondiendo
con los conteos.

---

## Hallazgo tardío: la versión de FlareSolverr era la causa raíz

Después de la verificación en vivo quedaba un problema abierto: la búsqueda real **no se podía leer
nunca**. Yo lo había atribuido a que Cloudflare tenía la IP bloqueada, y llegué a sugerir cambiar el
egreso por un proxy residencial.

Al revisarlo con evidencia, la causa era otra:

| Prueba | Resultado |
|---|---|
| IP directa vs. vía proxy | Distintas (`149.88.104.21` vs `146.70.188.34`) → el proxy funciona |
| FlareSolverr v3.3.20 **con** proxy | `Error solving the challenge. Timeout after 90s` |
| FlareSolverr v3.3.20 **sin** proxy | Idéntico → no era la IP del proxy |
| Chromium de v3.3.20 | **120** (diciembre de 2023) |
| Chromium de v3.5.2 | **152** |
| FlareSolverr **v3.5.2** (con el mismo proxy) | **200, 30 tarjetas, 11s, sin challenge** |

O sea: **era el browser antediluviano**, no la reputación de la IP. Pinneé v3.3.20 sin verificar
cuál era la última versión, y encima el plan justificaba el pin diciendo "evitar regresiones arm64 de
`latest`" — cierto como principio, pero me llevó a elegir una versión vieja sin comprobarla.

**Corregido:** `docker-compose.yml` pinneado en `v3.5.2`, con el porqué documentado en el propio
archivo y en el README. Verificado después con el probe real (`mode=flaresolverr`, 30 tarjetas) y
con el digest: las 30 publicaciones reales de la búsqueda del operador quedaron indexadas y
baselinadas.

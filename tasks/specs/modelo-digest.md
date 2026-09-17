# Área: Modelo, scoring y digest
> **Este documento es racional de diseño, no la lista de tareas.** La lista ejecutable,
> con acceptance criteria, verificación y alcance por tarea, está en `tasks/todo.md`.


**Objetivo:** un score que sea implementable de una única manera, honesto cuando no tiene datos, y
un digest que no se pierda ni se duplicada ante un restart.

**Alimenta:** V3 (modelo y scoring), V4 (scheduler y digest), V6 (contacto).
El skill de planificación marcó este conjunto como **XL**: se partió en las tareas V3.1–V3.3, V4.1–V4.3 y V6.1–V6.2.

## F1 — Buckets y score (especificación literal)

Dos implementadores deben producir el mismo bot leyendo esto.

```
rate(b) = (ups + 1) / (ups + downs + 2)                      -- Laplace
score   = mean( ln( rate(b) / 0.5 ) )  sobre features estructuradas PRESENTES
          bucket 'unknown' o feature ausente aporta 0
prior   = 0.5 el día 1 (model_weights vacío -> score 0)
orden   = ORDER BY score DESC NULLS LAST,
                      first_indexed_at DESC NULLS LAST, recency_rank ASC, id ASC
```

- AC: **`title_token` excluido del scoring en v1.** Se guarda en `features` pero no puntúa: una
  tarjeta trae 5-12 tokens, así que una suma queda dominada por el largo del título y una media
  diluye la única señal estructurada fuerte (`partido`) entre diez buckets cercanos al prior.
- AC: reasons = top-2 buckets por `(ups+downs) >= 3`, desempate por `|rate - 0.5|`. **Si ningún
  bucket llega a n>=3, no se muestra ninguna razón.** Decisión de producto.
- AC: **el desempate de recencia NO puede ser `listing_id`.** Razonamiento corregido por la
  evidencia de A1:
  - `data-id` **no es monotónico** con la fecha de publicación (`60170922, 60167605, 60170097, …`),
    así que no sirve como proxy de recencia.
  - Nuestro `listings.id BIGSERIAL` se asigna en **orden de ingesta**, y el orden de ingesta es el
    de la página, que es **más nuevo primero**. O sea: dentro de un mismo lote, la tarjeta más nueva
    recibe el id **más bajo**. `DESC` favorece a la más vieja y `ASC` a la más nueva, pero **al día
    siguiente** las publicaciones nuevas reciben ids más altos y `ASC` las entierra. Ninguna
    dirección funciona sola.
  - Solución: `recency_rank INT` = posición ordinal dentro del fetch ordenado por recencia **al
    verse por primera vez** (1 = la más nueva de esa corrida). Desempate
    `first_indexed_at DESC` (vista primero = más reciente entre días) y **dentro** del mismo día
    `recency_rank ASC`.
- AC: plantillas de razón **en español** ("San Isidro: te gustaron 4 de 5"). El producto es es-AR
  de punta a punta; esta es justo la frase que debe generar confianza.

### Buckets (corregidos por la medición de A1 sobre el DOM real)

- AC: `partido` — **último** segmento separado por comas, **salvo** que sea una macrozona conocida
  (`GBA Norte`, `GBA Sur`, `GBA Oeste`, `Capital Federal`), en cuyo caso se usa el **primero**.
  El DOM real es `barrio, partido`: `"Florida, Vicente López"` → `Vicente López`;
  `"San Isidro, GBA Norte"` → `San Isidro`. Solo 2 de 19 valores únicos terminan en macrozona, así
  que la regla por defecto es el último segmento. Una heurística al revés fragmenta los buckets.
  **Sin particionar** por operación ni moneda: la preferencia de barrio es la misma en venta que en
  alquiler.
- AC: **Claves de partición: `operation_type` × `currency`.** El producto **admite alquileres y pesos**
  (decisión de producto), así que un alquiler en ARS y una venta en USD **no se comparan jamás**.
  `operation_type` (`venta` | `alquiler`) y `currency` (`USD` | `ARS`) **no son buckets de scoring**:
  son **particionadores** de las features numéricas. Como buckets harían que el modelo aprenda "te
  gustan los alquileres", que no significa nada; como partición aprende *qué* alquileres te gustan,
  que es lo que se quiere.
- AC: **toda feature numérica se namespacia**: la clave del bucket es
  `feature:operation_type:currency`, p. ej. `ppm2:venta:USD:1500-1750` o
  `expensas:alquiler:ARS:250k-300k`. El paso del bin se define **por namespace**: `venta:USD` usa
  **250 lineal** (validado por A1), y **cualquier otro namespace usa bins logarítmicos** (razón ~1.12)
  hasta tener datos que justifiquen un paso lineal. Los log-bins se adaptan solos a cualquier moneda y
  magnitud, así que no hay que adivinar escalas en ARS — y evitan el bin degenerado que ya vimos con
  10k USD/m². Ausencia de paso no existe: siempre hay uno (lineal o log).
- AC: **`currency` vacío o desconocido ⇒ todas las features numéricas van a `unknown`.** Es la guarda
  que evita el outlier de 1000× ya visto en el ciclo 2 (un precio ARS puntuado contra una banda USD).
- AC: `ppm2` = `price_amount / m2_tot`, con `m2_tot` dentro de `[10, 1000]`. A1 midió `venta:USD` en
  **896 a 2759 USD/m²** (mediana 1872) con **9 buckets no vacíos de 28 muestras** a paso 250, así que
  ese namespace queda validado. Los 10k USD/m² del plan original eran degenerados (655 y 1353 caían
  ambos en el bin 0). **No se usa `m2_cub`**: A1 midió **0 apariciones de `m² cub.`**, condicionar a
  él habría dado `unknown` en el 100% de los casos.
- AC: **guarda de outliers antes de cualquier feature numérica.** El DOM real trae `m² = 1` y
  `m² = 12500` junto al rango sano 58-141. Fuera de `[10, 1000]` → `unknown`.
- AC: `banios` — **entra**, y **sin particionar**: es preferencia de vivienda, independiente de
  operación y moneda. Única feature estructurada además del precio con varianza limpia y baja
  cardinalidad: `1 baño`×13, `2 baños`×16, `3 baños`×1.
- AC: `expensas` — **entra**, namespaciada por `operation_type:currency`. Existe en el DOM real
  (`data-qa="expensas"`) en **25/30** tarjetas (mediana $300.000/mes). Para un alquiler, expensas y
  precio están en la **misma** moneda y son comparables entre sí; para una venta en USD, la expensa
  ARS y el precio USD **no**. La partición resuelve ambos casos sin necesitar tipo de cambio.
- AC: `dorm` — se parsea pero **no puntúa en v1**: A1 midió `2 dorm.` en **30/30** (constante).
- AC: `rooms`/`ambientes` — fuera como bucket. Medido `3 amb.` en **30/30**. (Se sigue parseando
  para mostrar.)
- AC: `m2_bin` en pasos de 10 m², con `m2_basis` en la clave, para no mezclar totales con cubiertos
  si otra búsqueda sí expone `m² cub.`.
- AC: valores ausentes -> bucket `unknown` explícito, **nunca 0**. Coercionar `""` a
  `price_amount=0` mete la publicación en el bucket más barato, donde junta 👍 accidentales y
  domina el orden.

### Honestidad sobre la cardinalidad

- AC: `partido` es la feature más predictiva **y** la que menos datos por bucket tiene: **19 valores
  únicos sobre 30 tarjetas**, así que la mayoría de los buckets queda en n=1-2 y **no alcanza el
  n≥3** para mostrar una razón. Esto no es un defecto corregible con más features: es el límite
  estructural de aprender sobre un solo usuario con poco volumen. La regla n≥3 existe justo para no
  mentir sobre esto, y `/model` debe exponer los conteos para que el usuario lo vea.

## F2 — Retraining nocturno

- AC: **por usuario**, cada uno en su propia transacción, con log-and-continue. Una transacción
  global acopla el destino de todos: los ratings corruptos de un usuario harían rollback de los
  pesos nuevos de todos, y la transacción escala linealmente con usuarios sobre el hardware más
  débil del stack.
- AC: `users.active_model_version` se voltea **dentro** de la misma transacción que escribe los
  pesos. Sin el volteo atómico, un crash entre escritura y flip deja lectores pinneados a una
  versión a medio escribir.
- AC: el scorer lee **solo** la versión activa. Un delete+insert sobre la tabla viva deja una
  ventana con pesos a mitad actualizar; si las 09:00 caen ahí, el orden del día es basura.
- AC: prune de versiones no referenciadas por ninguna `deliveries` más vieja que la ventana de
  `/model`. Con `model_version` en el PK, cada noche acumula un snapshot completo por usuario para
  siempre.
- AC: el retrain no puede solaparse con el digest (ver F4, `TryLock`).

## F3 — `/model`

- AC: conteos por bucket **siempre** visibles.
- AC: métrica = **concordancia pairwise dentro del día**: sobre días con >=2 entregas calificadas,
  fracción de pares donde `score_i > score_j` y `label_i > label_j`. Anclada en `ratings.created_at`,
  recalculada (no cacheada) sobre los últimos 30 días.
- AC: "agreement" a secas no es implementable — `deliveries.score` es REAL en [0,1] y
  `ratings.label` es binario, así que hace falta una regla de decisión. Con umbral 0.5 y la mayoría
  de los scores empatados en el prior, todos los 👍 "aciertan" y todos los 👎 "fallan": la métrica
  mediría el sesgo de tap del usuario, no el modelo.
- AC: suprimida con n<30, mostrando "todavía aprendiendo: N calificaciones". Como `ratings` hace
  upsert, un cambio de opinión posterior muta el agreement histórico contra un score congelado; la
  ventana se ancla en `created_at` para que sea estable.
- AC: **declara explícitamente** que v1 entrena solo con labels explícitos y que ignorar una
  tarjeta **no** cuenta como negativo.

## F4 — Scheduler y digest

- AC: **sin paginación.** A1 descartó la paginación server-side: `?n_pg=2` devuelve los mismos 30
  `data-id`, y `n_pg=2` no aparece ninguna vez en el HTML. El digest lee **la página 1**, que con el
  orden reciente inyectado son las ~30 más nuevas.
- AC: **monitorizar el techo de 30.** Si `last_card_count == 30` de forma sostenida y aparecen más de
  30 publicaciones nuevas entre corridas, se pierden publicaciones sin aviso. Se registra el
  indicador y, si se detecta saturación, se le informa al operador: es un límite del portal, no un
  bug.
- AC: **FlareSolverr obligatorio** (A1: ni curl ni `tls-client` Chrome_152 vía proxy pasan el
  `cf-mitigated: challenge`; solo FlareSolverr). Con `budget` de 1 req/60s (ráfaga 1) y ventana deslizante,
  dado que A1 vio escalar a Cloudflare tras **~4 requests en 10 minutos**.
- AC: `import _ "time/tzdata"` en `cmd/bot/main.go`. La imagen es `scratch` y copia **solo** CA
  certs: no hay `/usr/share/zoneinfo`, `TZ` en compose no sirve para nada, `time.Local` es UTC y
  las 09:00 disparan a las **06:00** de Buenos Aires. El código actual nunca dependió de la hora
  absoluta (es un ticker relativo), así que este fallo es nuevo y silencioso.
- AC: `SCHEDULE_TZ` con **default explícito** `America/Argentina/Buenos_Aires` cuando viene vacío.
  `time.LoadLocation("")` devuelve `(UTC, nil)`: es resoluble, así que un fail-fast sobre
  "no resoluble" jamás lo catchea.
- AC: timer al próximo 09:00, **sin** corrida al arrancar (`RUN_ON_START=false`). Hoy `main.go`
  corre un ciclo en el startup y luego cada 60m; con `restart: unless-stopped` y un Pi que rebootea,
  eso es un ciclo completo en cada reinicio.
- AC: fila `digests(user_id, run_date, status='pending')` creada **al agendar, pase lo que pase con
  el lock**. Con `TryLock` que saltea y resume por `run_date`, si el digest de ayer sigue corriendo
  a las 09:00 no se crea la fila de hoy y no hay nada que reanudar: el día se pierde sin dejar rastro.
- AC: **el digest solo recorre usuarios con `users.active = true`.** Decisión del operador: `/start`
  y el onboarding funcionan para cualquiera (carga y valida URLs), pero **nadie recibe notificación
  diaria hasta que se lo active a mano** con
  `UPDATE users SET active = true WHERE chat_id = <id>`. El flag se chequea en el job, no en el
  momento del alta, así que activar/desactivar surte efecto en la corrida siguiente sin tocar datos.
  `users.state='stopped'` (que maneja el propio usuario con `/stop`) y `active=false` (que maneja el
  operador) son **independientes**: el digest requiere `active AND state != 'stopped'`.
  **Implementado en V1.5** (`repo.ListActiveUsers`), adelantado desde V4.3 para que no exista una
  ventana sin control; V4.3 queda con el tope diario y el monitoreo de cobertura.
- AC: input del digest = **todas las publicaciones no entregadas** de las URLs del usuario, no
  "lo fetchado hoy". Auto-sanante: si el bot muere a mitad y reanuda a las 15:00, no se saltea el día.
- AC: en boot y en cada corrida, terminar cualquier digest no-`done`.
- AC: `TryLock` por job con log de skip. Un digest largo no puede solaparse con el retrain y leer
  pesos a mitad escribir.
- AC: tope diario de **15 envíos** con **excedente re-puntuado al día siguiente**. Mantiene la promesa de
  "sin filtrado": no se descarta nada, se difiere.
- AC: `deliveries.status` con ciclo de vida `pending|sent|failed|dead` y `attempts`. El digest y el
  resume reintentan `pending`/`failed` con tope de intentos, luego `dead` + alerta. Sin esto, un
  fallo de envío o pierde la publicación para siempre o la reintenta sin límite.
- AC: reconciliación nocturna de `deliveries` con `message_id IS NULL` y `sent_at` reciente.
  Telegram no es un recurso transaccional, así que la secuencia real es **enviar y luego**
  `INSERT … ON CONFLICT DO NOTHING`; si el insert falla, se encola para reparación y se alerta.
  Duplicado raro antes que pérdida silenciosa.

## F5 — Contacto al 👍 (**habilitada por A2: GO, solo teléfono**)

- AC: **implementación concreta medida por A2.** El detalle trae un `<script
  type="application/ld+json">` con un objeto `Apartment` que incluye `"telephone"` en el **HTML
  estático**. Se extrae parseando ese JSON-LD (no needles de HTML, no click, no API interna). Una
  sola ocurrencia por página. Ejemplo real: `"telephone":"54 9 1168690900"`.
- AC: **no hay email.** A2 verificó que no existe email del anunciante — solo placeholders
  (`nombre@mail.com`) y direcciones corporativas de Zonaprop. La feature manda **teléfono o nada**,
  y el link va siempre aparte. No se promete mail en la UI.
- AC: **el costo del camino es 1 request por 👍, no por publicación.** El card ya trae las 30
  publicaciones con 1 request; el detalle cuesta 1 request cada uno. Si se fetcheara el detalle para
  todas, un digest de 30 listings tardaría ~30 minutos al budget de 1 req/60s. Por eso: card =
  features de volumen, detalle = solo al 👍. A2 confirma que este reparto no es un compromiso sino
  el diseño correcto.
- AC: **reutilizar el JSON-LD como feature de refuerzo.** El mismo bloque trae `numberOfRooms`,
  `numberOfBedrooms`, `numberOfBathroomsTotal`, `floorSize{value,unitCode:"MTK"}` (m² con unidad
  explícita), `streetAddress` (dirección real) y `addressRegion` (barrio) estructurados. Se guardan
  en `listings.features` del detalle: son más limpios que los needles del card y no cuestan nada
  extra, porque el request ya se pagó por el teléfono. Fixture de test:
  `fixtures/detail_ldjson.json`.
- AC: el **link va siempre en la tarjeta**, y el 👍 dispara únicamente la búsqueda de teléfono.
  Gatear el link detrás del 👍 corrompe las etiquetas: la gente toca 👍 para obtener el link, los
  positivos se inflan a ~100%, el modelo pierde poder discriminativo y el texto "te gustaron 4 de 5"
  describe datos que no son lo que dice.
- AC: orden de mensajes — **link primero**, inmediato; luego intento de extracción con timeout duro
  (`maxTimeout` ≤60s, A2) y **segundo mensaje solo si se encontró el teléfono**. Si el fetch tira
  500 por challenge — **el caso más probable según A2** — el usuario igual ya tiene el link.
  "No encontrado" es el camino normal, no un error.
- AC: la extracción usa **un GET sin reintentos con cliente propio**, no `fetch.Client`: el stack
  actual usa `MaxBrowserTimeout=60s` y backoff de 1s/2s/4s+jitter, así que un deadline de 5s aborta
  casi siempre a mitad del challenge. "No encontrado es lo normal" se volvería "no encontrado es lo
  único". Y un fallo de challenge **no** debe reintentarse contra una IP que ya está escalando.
- AC: **caveat a verificar antes de confiar en la feature:** A2 no pudo confirmar que `telephone`
  varíe entre publicaciones (el segundo detalle falló por challenge). **Se espera que sí** (decisión
  del usuario): está en el JSON-LD del `Apartment` específico junto a la dirección de esa publicación.
  Verificar sobre ≥2 detalles con IP fresca **antes de V6**. Si resultara un número genérico, F5 se
  degrada a "manda el link" sin romper nada, porque el link ya va en la tarjeta.
- AC: los fetches interactivos van por el **carril prioritario**. Un 👍 encolado detrás de los
  decenas de fetches del digest llega minutos u horas tarde, cuando el spinner del callback ya
  falló en el cliente.
- AC: `listing_contacts` con **TTL de 30 días** y re-fetch por el carril prioritario cuando está vencido.
  Con PK en `listing_id` y sin política, un 👍 seis meses después sirve un teléfono de seis meses
  como si fuera fresco.
- AC: si ya existe `label=1` para esa publicación, **no** se re-ejecuta nada.

## Archivos

`internal/score/*.go`, `internal/digest/*.go`, `internal/contact/*.go` (si go),
`internal/scheduler/*.go`, `cmd/bot/main.go` (reescribir), tests.

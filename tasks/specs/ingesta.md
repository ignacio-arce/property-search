# Área: Ingesta (card tipado + fetch)
> **Este documento es racional de diseño, no la lista de tareas.** La lista ejecutable,
> con acceptance criteria, verificación y alcance por tarea, está en `tasks/todo.md`.


**Objetivo:** que el parser produzca features tipadas **reales** sobre fixture **real**, y que el
fetch exponga lo necesario para clasificar fallos. Sin esto, el modelo entrena sobre datos
inventados.

**Alimenta:** V1 (parser de card y fetch), V5 (validación profunda), V6 (parser de detalle).
**Evidencia:** `tasks/research/probe-results.md`.

## B1 — `internal/model` tipado

Hoy `Listing` tiene todo en `string` (`Price`, `M2`, `Ambientes`). Cambia a:

- AC: `ZonapropID string` desde el atributo `data-id` — **es la clave de dedup**, no la URL.
- AC: `CanonicalURL string` = origin + path, **sin query** (los `n_src`/`n_pg`/`n_pos` de tracking
      son los que rompen el dedup).
- AC: `PriceAmount *int64` y `Currency string` (`USD` | `ARS` | `""`). Puntero: ausente es NULL,
      **nunca 0** — un 0 mete la publicación en el bucket más barato y el modelo aprende que un
      departamento gratis es genial.
- AC: `M2Tot *float64`, `M2Cub *float64`, `M2Basis string`. **Corregido por A1**: en la página real
      hay **30/30 `m² tot.` y 0 `m² cub.`** — la mezcla `128 tot.`/`220 cub.` que mostraba
      `fixtures/sample.html` era **artefacto del fixture sintético**, no del DOM. Se parsean igual
      por separado (otras búsquedas sí los mezclan), pero `M2Cub` va a ser NULL casi siempre y F1
      no puede depender de él.
- AC: **guarda de outliers obligatoria.** La página real contiene `m² = 1` y `m² = 12500` junto a un
      rango sano de 58-141. Sin filtro, un `m²=1` produce un `price_per_m2` de ~150.000 USD/m² que
      se queda con el tope del ranking. Se descartan del feature los m² fuera de `[10, 1000]`
      (→ bucket `unknown`), con test sobre el fixture real.
- AC: `Dorm *int`, `Banos *int`. **Corregido por A1**: del hallazgo "son los ejes que varían" solo
      se cumple la mitad. En la página real **`2 dorm.` aparece en 30/30 → constante** (cero señal),
      mientras **`baños` sí varía**: 13× "1 baño", 16× "2 baños", 1× "3 baños". Se parsean ambos
      (multi-usuario), pero F1 no debe esperar señal de `dorm` en esta búsqueda.
- AC: **`Expensas *int64` — existe en el DOM real.** El ciclo 2 lo descartó porque no estaba en el
      repo, y era cierto del repo pero **falso de Zonaprop**: cada tarjeta lo trae en
      `data-qa="expensas"` con formato `$ 180.000 Expensas`. Presente en **25/30** tarjetas, mediana
      $300.000/mes. Es la segunda señal con más varianza después del precio y es costo real
      mensual, así que **vuelve al modelo** (ver F1). Ojo: está en **ARS** aunque el precio sea USD.
- AC: `Operation string` (`venta` | `alquiler` | `""`) — el **`operation_type`** que pide el producto.
  Se detecta del **path** de la URL de búsqueda (`-alquiler-` / `-venta-`), que es la señal más
  confiable, y se guarda como columna `operation_type` en `listings` (snake_case en DB/features,
  `Operation` en Go). El bot es multi-usuario y **admite alquileres**: las URLs de otros no vienen
  filtradas y `data/seen.jsonl` ya contiene tarjetas de alquiler. Es **clave de partición** del
  modelo, no un feature de scoring (ver `modelo-digest.md`).
- AC: **`Currency` se detecta del prefijo del precio** (`USD` explícito vs `$` → ARS) y se guarda
  como columna `currency`. Es la otra **clave de partición**. Si queda vacía o desconocida, todas
  las features numéricas de esa publicación van a `unknown`: sin esto un alquiler en ARS se compararía
  contra una banda de venta en USD.
- AC: se eliminan `Price`, `M2`, `Ambientes` como strings sueltos; `Title`, `Location`, `PhotoURL`
      quedan.

## B2 — `internal/parser`

- AC: `Parse` deriva el **origin** de `siteBase` (`url.Parse` → `Scheme://Host`) antes de
      concatenar hrefs. Bug pre-existente: `bot.go` y `cmd/probe` le pasan la URL de búsqueda
      completa, así que los hrefs root-relative producen
      `…-200000-dolar.html/propiedades/clasificado/…`. Los tests no lo ven porque siempre pasan el
      origin. Se agrega test de regresión que pasa una **URL de búsqueda completa**.
- AC: solo tarjetas `data-posting-type=PROPERTY` en v1, con **contador de salteadas** expuesto
      (no silently dropped: un cambio de DOM debe distinguirse de una búsqueda vacía).
      **A1 midió 30/30 PROPERTY y 0% de emprendimientos** en la búsqueda real: el ~17% que estimaba
      el ciclo 2 venía de `data/seen.jsonl`, una captura contaminada. El filtro se conserva como
      **red de seguridad barata**, no como necesidad de volumen, y deja de ser un riesgo a evaluar.
- AC: `zonaprop_id` vacío → saltear con contador, no abortar el parseo.
- AC: `parsePrice(s) (amount *int64, currency string, ok bool)` cubriendo los formatos reales.
      **A1 midió en la página real**: 30/30 con prefijo `USD` y miles con punto (`USD 144.000`),
      y **`expensas` en 25/30 como `$ 180.000 Expensas`** → el parser debe manejar USD y ARS en la
      misma tarjeta y **no comparar magnitudes entre monedas**. Conservar además `Consultar precio`
      y `Desde USD …` → `ok=false`.
      **Alquileres (decisión de producto):** un alquiler en ARS es un monto **mensual** y de
      magnitud nominal muy distinta a una venta (`$ 450.000`/mes vs `USD 144.000` total). El parser
      **no** interpreta periodicidad — solo extrae monto y moneda; la distinción venta/alquiler vive
      en `operation_type` y la separación de magnitudes en la partición del modelo. Tests de tabla
      con al menos un caso de alquiler ARS explícito.
      El formato real del selector es `data-qa="POSTING_CARD_PRICE"` con el texto **dentro de un
      `<h2>`** y la expensa en un `<h2>` hermano `data-qa="expensas"`: el selector actual de precio
      funciona, pero **expensas hoy no se toca**.
- AC: needles `m² tot.` / `m² cub.` por separado; `dorm.`; `baño` (el needle `banio` **no matchea**
      el DOM real).
- AC: `partido` normalizado: si el último segmento separado por comas es una macrozona conocida
      (`GBA Norte`, `GBA Sur`, `GBA Oeste`, `Capital Federal`), se usa el **primero**.
      `"San Isidro, GBA Norte"` → `San Isidro`, no `GBA Norte`. Se guarda el `Location` crudo en
      features para auditoría.
- AC: **la regla anterior es necesaria pero no suficiente.** A1 muestra que el `Location` real es
      `barrio, partido` — `"Florida, Vicente López"`, `"Centro, Tigre"`, `"Pilar, Pilar"` — y **solo
      2 de 19 valores únicos terminan en macrozona**. El resto necesita el **último** segmento, que
      sí es el partido. O sea: la heurística correcta es "último segmento, salvo que sea macrozona →
      entonces primero". Un `partido` mal extraído fragmenta los buckets y mata la única señal
      estructurada fuerte que tiene el modelo.
- AC: tests contra `fixtures/search_gba_norte.html` (real, 30 tarjetas, PII scrubbeada, commiteado
      en A1), conservando `fixtures/sample.html` solo para los casos de tarjeta malformada.

## B3 — `internal/fetch`

- AC: `Result` gana `Status int` y `Header http.Header`. Hoy es `{Body, Mode}` y `fetchViaTLS`
      descarta headers y convierte **cualquier** non-200 — incluido el 403 de Cloudflare — en un
      error genérico. La taxonomía de validación de E3 (challenge vs transporte vs vacío) es
      imposible sin esto, y por FlareSolverr los headers hoy no existen.
- AC: `ZONAPROP_PROXY` reemplaza `HTTP_PROXY`. La stdlib de Go lee `HTTP_PROXY` vía
      `ProxyFromEnvironment`, y `flaresolverr.go` usa un `&http.Client{}` pelado: con el proxy
      configurado, el POST a `http://flaresolverr:8191` saldría por el proxy externo, que no
      resuelve el DNS de compose. **El modo principal falla 100% de las veces justamente cuando
      se configura el proxy.** Alternativa o complemento: `Transport{Proxy: nil}` explícito en los
      clientes de Telegram, FlareSolverr y fotos.
- AC: timeout del cliente de FlareSolverr ≥ `MaxBrowserTimeout + 30s`. Hoy es `FetchTimeout` (30s)
      contra un `maxTimeout` de 60s: el bot **aborta resoluciones exitosas**, las cuenta como
      fallo y relanza otro Chromium. En un Pi5 es DoS autoinfligido.
- AC: **PERO el techo va para abajo, no solo para arriba.** A1 midió que un `maxTimeout` de 150s ×
      3 reintentos = **7.5 minutos de browser churn sobre una IP ya sospechada**, y aun así falló.
      Bajar el timeout del cliente sin bajar `maxTimeout` deja el problema. Regla: `maxTimeout`
      **≤ 60s**, reintentos de FS **≤ 1** (no 3), y **cooldown** antes de reintentar una IP que
      devolvió challenge. Reintenter rápido contra Cloudflare empeora la reputación de forma
      semi-permanente; el loop de reintento ingenuo es un autodenial de servicio.
- AC: **FlareSolverr deja de ser opcional y pasa a ser el camino principal.** A1: el 403 de Zonaprop
      trae `cf-mitigated: challenge`, y ni curl ni `tls-client` con huella `Chrome_152` vía proxy lo
      pasan — solo FlareSolverr (`status: ok`, 30 tarjetas, 2.8s). El README que afirma que "Zonaprop
      acepta la huella TLS de Chrome" queda **obsoleto** y se corrige en G4.
      `FLARESOLVERR_URL` se vuelve **requerida en modo servidor** (misma lógica de guard que el token
      de Telegram en G3): sin ella, el bot arranca, no puede fetchear nada, y no le avisa a nadie.
- AC: endpoint **`/v1`** confirmado: `POST /v2` devuelve **404** en `v3.3.20`. El repo ya usa `/v1`
      (`flaresolverr.go:40`) ✓. Se pinnea la versión en compose (G1) y se ancla el test a `/v1`.
- AC: se conserva la cadena por intento FS → proxy → directo **y** `TestFlareSolverrFallsBackToTLS`.
      Lectura exclusiva ("FS si está, else proxy") con FlareSolverr caído = cero fetches exitosos
      y outage silencioso total. El fallback a TLS es hoy **estéril para Zonaprop** (lo probó A1)
      pero se mantiene: es lo único que sirve para la descarga de fotos del CDN.
- AC: `errNoMode` (`fetch.go:88`) está muerto — se elimina o se cablea.

## Archivos

`internal/model/model.go`, `internal/parser/parser.go`, `internal/parser/parser_test.go`,
`internal/fetch/fetch.go`, `internal/fetch/tls.go`, `internal/fetch/flaresolverr.go`,
`internal/fetch/fetch_test.go`, `fixtures/`.

## Nota de compatibilidad

B1 rompe `internal/bot`, `internal/telegram` y `cmd/probe` (usan los campos string y la firma
`Notify(ctx, l)`). Se compila con adaptadores mínimos provisorios; la reescritura real es de D y G3.
`go build ./...` debe cerrar al terminar la fase.

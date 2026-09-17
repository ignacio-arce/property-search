# Resultados de sondas — evidencia empírica

> Registro de mediciones hechas contra Zonaprop el 2026-09-16. **Es evidencia, no un plan.**
> Varias decisiones de `tasks/plan.md` existen porque estas mediciones refutaron la suposición
> original; no borrar ni "limpiar".

**Objetivo:** resolver empíricamente los tres supuestos que pueden invalidar el diseño **antes**
de escribir el modelo, el parser tipado o la persistencia. Esta fase no construye producto:
construye evidencia.

**Alimenta:** todas las rebanadas. Lo medido acá es lo que obligó a re-slicear el plan.

## Por qué primero

El ciclo 2 de doubt encontró que el plan v1 se apoyaba en datos que no existen: `expensas` tiene
**cero coincidencias** en todo el repo, las 30 filas reales de `data/seen.jsonl` son todas
`n_pg=1` (la paginación nunca se verificó), y el único fixture commiteado es **sintético**
(`127.0.0.1:8099`, prefijo `veclapin`, una *casa* dentro de una búsqueda de departamentos).
Construir sobre eso es construir sobre datos inventados.

## A1 — Probe de la URL real

- AC: `cmd/probe` acepta URLs por **argv** (hoy lee `cfg.SearchURLs`, que se elimina en G3) y un
  path de salida para el fixture.
- AC: fetch de
  `https://www.zonaprop.com.ar/departamentos-venta-gba-norte-3-ambientes-mas-de-1-garage-mas-55-m2-cubiertos-hasta-10-anos-100000-200000-dolar-orden-publicado-descendente.html`
  con `ZONAPROP_PROXY=http://192.168.1.150:8089` (dev) y registro del modo que funcionó.
- AC: **verifica el orden reciente** — imprime el `data-id` y el slug de las primeras 10 tarjetas
  y permite comprobar a mano que son las más nuevas. Si el orden no se observa, se documenta y el
  early-stop global de paginación **no** se implementa.
- AC: **verifica paginación** — fetch de `?n_pg=2` y comparación de `data-id` contra `n_pg=1`.
  Si los conjuntos se solapan o son idénticos, la paginación se descarta y el digest se limita a
  página 1, avisando al usuario en la confirmación de `/addurl`.
- AC: **verifica `data-posting-type`** — cuenta tarjetas PROPERTY vs `emprendimiento` vs otras.
  Registra el porcentaje real (la evidencia actual sugiere ~17% de emprendimientos).
- AC: **verifica `data-id`** — presente en el 100% de las tarjetas PROPERTY; si falta en alguna,
  se cuenta y se saltea.
- AC: captura `fixtures/search_gba_norte.html` **scrubbeada**: reemplazo por regex de teléfonos,
  emails y nombres de inmobiliaria/agentes. Un test afirma que el fixture commiteado no contiene
  patrones de contacto.

## A2 — Probe de la página de detalle (go/no-go de contacto)

- AC: toma un `data-id` real de A1, construye la URL canónica del detalle y la fetcha con el modo
  que funcionó.
- AC: guarda el **JSON-LD del detalle** scrubbeado como `fixtures/detail_ldjson.json` (se descartó
  commitear el HTML de 536 KB: ver resultados).
- AC: **RESUELTO — salida 1, parcial: GO para teléfono, NO-GO para email.** El teléfono está en el
  JSON-LD estático (`Apartment.telephone`), así que F5 se implementa con **un GET sin reintentos y
  cliente propio** + parseo de JSON-LD. El email no existe; F5 manda teléfono o nada.
- AC: **RESUELTO — tiempo real: 1.9s** (`Challenge not detected!`), muy por debajo del umbral. Pero
  la extracción va igual por el carril prioritario porque compite con el digest, y `maxTimeout` se
  fija ≤60s: el segundo intento de detalle agotó 60s y falló por challenge.
- AC: **PENDIENTE:** confirmar que `telephone` varíe entre publicaciones, sobre ≥2 detalles con IP
  fresca. Si fuera genérico, F5 se degrada a "manda el link".

## A3 — Alcance de red desde compose

- AC: `docker compose up` con los tres servicios; desde **adentro** del contenedor del bot se
  verifica resolución de `flaresolverr` y respuesta de `GET http://flaresolverr:8191/`.
- AC: se verifica que FlareSolverr alcance el proxy del host. `extra_hosts: host-gateway` va en
  **flaresolverr**, no en el bot — el que consume `PROXY_URL` es Chromium.
- AC: se confirma que el proxy escucha en `0.0.0.0` o en la interfaz alcanzable; si solo escucha
  en `127.0.0.1` del host, se documenta el cambio necesario.
- AC: se mide un fetch a Zonaprop **con** y **sin** FlareSolverr, y se registra cuál pasa el
  challenge desde la IP del Pi.

## Artefactos producidos

- `fixtures/search_gba_norte.html` — página real, 30 tarjetas, PII scrubbeada (~970 KB)
- `fixtures/detail_ldjson.json` — JSON-LD del detalle, 780 B, teléfono y dirección falsos
- Este documento: los resultados, que son los que obligaron a re-slicear el plan

## Salida esperada

Un bloque "RESULTADOS A1/A2/A3" al pie de este archivo con: modo de fetch que funciona, orden
reciente confirmado sí/no, paginación confirmada o descartada, % de PROPERTY, decisión de contacto
(go / go-con-browser / no-go), latencia de detalle, y alcanzabilidad de proxy y FlareSolverr.
Esos resultados **re-escriben** partes de las fases B, E y F; ninguna de esas tres arranca antes
de tenerlos.

---

# RESULTADOS A1/A2/A3

Medidos el 2026-09-16 contra la URL real del operador, desde la máquina de dev (x86_64), con
`PROXY_URL=http://192.168.1.150:8089` y FlareSolverr `v3.3.20`.

## A3 — Alcance de red

| Camino | Resultado |
|---|---|
| curl directo (UA Chrome) | **403** en 0.26s |
| Go `tls-client` Chrome_152 **vía proxy** | **403** (`failed after 3 attempt(s)`) |
| curl **vía proxy** a `example.com` | **200** → el proxy funciona |
| Headers del 403 de Zonaprop | `HTTP/2 403`, **`cf-mitigated: challenge`**, `server: cloudflare`, `cf-ray: …-GRU` |
| FlareSolverr `POST /v2` | **404** → esta versión solo expone **`/v1`** (el repo ya usa `/v1` ✓) |
| FlareSolverr `POST /v1` a la búsqueda | **`status: ok`, 30 tarjetas, 1.02 MB, 2.8s** |

**Consecuencia que invalida una premisa del repo.** El README afirma "En la práctica Zonaprop
acepta la huella TLS de Chrome; el proxy/FlareSolverr ayudan cuando Cloudflare marca el IP". **Ya
no es cierto**: el 403 trae `cf-mitigated: challenge`, o sea un *managed challenge* que exige
resolver JavaScript. Ningún cliente HTTP lo pasa, con la firma TLS que traiga. **FlareSolverr pasa
de capa opcional a camino obligatorio**, y el README se corrige en G4. El proxy sigue siendo útil,
pero como rotación de IP para el browser, no como sustituto.

**Confirmación empírica de B3.** `tls.go:52` descarta los headers y convierte cualquier non-200 en
error genérico. La única señal que distingue "bloqueado" de "búsqueda vacía" es justamente un
**header** (`cf-mitigated`). Sin `Result.Status`/`Result.Header` la taxonomía de E3 es
literalmente inimplementable.

## A1 — Orden reciente: CONFIRMADO

Las edades extraídas del HTML ("Publicado hace N") en orden de aparición son **no-decrecientes**:

```
2880,2880,2880,2880, 4320, 5760,5760,5760, 7200×7, 8640,8640   (minutos)
= 2d, 3d, 4d, 5d, 6d                                              lo más nuevo primero
```

- Auto-inyectar `-orden-publicado-descendente` está justificado (E2).
- El early-stop global ("paro al encontrar una publicación ya indexada") es **sonoro en principio**
  porque el orden por recencia lo hace válido independientemente del usuario.
- **Pero los `data-id` no son monotónicos** (`60170922, 60167605, 60170097, …`), así que **no**
  sirven como proxy de recencia. Ver F1: el tie-break por `listing_id` estaba mal razonado.

## A1 — Paginación: **DESCARTADA**

| Prueba | Resultado |
|---|---|
| `?n_pg=2` como request nuevo | `status: ok`, 30 tarjetas, **intersección con página 1 = 30/30** (mismos `data-id`, mismas edades) |
| Apariciones de `n_pg=2` en el HTML | **0** — no existe ningún link servidor a la página 2 |
| `?n_pg=2&n_search_id=<el de la sesión>` | **500** — `Error solving the challenge. Timeout after 150.0 seconds` |
| Total declarado en la página | **565 resultados**, ~44 páginas |

**La paginación no está disponible server-side.** El listado lo monta el SPA en el cliente;
`n_pg` en la query se ignora y el HTML servido es siempre la página 1. Consecuencias:

1. **Se elimina la paginación `n_pg=1..3`** de F4 y el early-stop deja de tener objeto.
2. La cobertura real del bot es **las ~30 publicaciones más nuevas de cada búsqueda**, no las 565.
   Con orden reciente eso es mayormente inocuo para "avisame qué hay de nuevo" (lo nuevo está
   arriba), pero **hay que decírselo al usuario** en la confirmación de `/addurl`, no dejarlo
   implícito.
3. El riesgo pasa a ser otro: si en un día aparecen **más de 30** publicaciones nuevas en una
   búsqueda, las que queden por debajo del puesto 30 se pierden para siempre. En una búsqueda
   filtrada a 565 totales eso es improbable, pero se monitoriza (`last_card_count`).

## A1 — Tipos de tarjeta: **0% emprendimientos**

`data-posting-type: {"PROPERTY": 30}` en ambas páginas. **Cero tarjetas de emprendimiento.**

El "~17% de una página real" que afirmaba el ciclo 2 salió de `data/seen.jsonl`, que es una captura
**contaminada** (`127.0.0.1:8099`, prefijo `veclapin`, una *casa* dentro de una búsqueda de
departamentos). Corrección: el filtro PROPERTY no es una necesidad de volumen sino una **red de
seguridad barata**, y se conserva. Sigue vigente el contador de salteadas (distingue cambio de DOM
de búsqueda vacía).

## A2 — Contacto: **GO para teléfono · NO-GO para email**

Segundo intento, tras cooldown y con **`PROXY_URL`** (no `HTTP_PROXY`, ver abajo):

| Intento | Resultado |
|---|---|
| Detalle 1 (`…-60170922.html`) | **`status: ok`, 200, 560 KB, 1.9s**, `Challenge not detected!` |
| Detalle 2 (`…-60169261.html`) | **500** — `Error solving the challenge. Timeout after 60.0s` (Cloudflare escaló de nuevo) |

**El teléfono está en un bloque JSON-LD del HTML estático.** No hace falta click, ni API interna,
ni JS adicional: viene servido en `<script type="application/ld+json">` dentro de un `Apartment`.
Fixture commiteado, scrubbeado y reducido: **`fixtures/detail_ldjson.json`** (780 bytes):

```json
{ "@context": "https://schema.org", "@type": "Apartment",
  "name": "Departamento - Florida Belgrano - Oeste, Gba Norte - Zonaprop",
  "description": "DEPARTAMENTO SIMIPISO AL FRENTE DE 3 AMB CON AMPLIO BALCÓN CORRIDO Y COCHERA CUBIERTA…",
  "image": "https://imgar.zonapropcdn.com/avisos/…/2078927423.jpg?isFirstImage=true",
  "numberOfRooms": 3,
  "floorSize": { "@type": "QuantitativeValue", "value": 69, "unitCode": "MTK" },
  "numberOfBathroomsTotal": 1,
  "numberOfBedrooms": 2,
  "address": { "@type":"PostalAddress", "addressLocality":"…, GBA Norte, Argentina",
               "addressRegion":"Florida", "streetAddress":"SAN Martin al 3700" },
  "telephone": "54 9 1168690900" }
```

- **Teléfono: GO.** `Apartment.telephone`, una sola ocurrencia, en el HTML estático.
- **Email: NO-GO.** No hay email del anunciante; solo placeholders (`nombre@mail.com`) y direcciones
  corporativas de Zonaprop. **F5 manda teléfono o nada.**
- **Bonus inesperado — el JSON-LD es un espejo estructurado del card**, con tres campos que en el
  card hay que scrapear de needles: `numberOfRooms: 3`, `numberOfBedrooms: 2`,
  `numberOfBathroomsTotal: 1` (coinciden con `3 amb.`, `2 dorm.`, `1 baño`), más `floorSize` en
  **`MTK`** (m², con `unitCode` explícito), `streetAddress` (dirección real) y `addressRegion`
  (barrio). O sea que **el detalle tiene features estructuradas mejores que las del card**, y son
  gratis una vez que ya se pagó el request.
- **Se decidió NO commitear el HTML de detalle** (536 KB): lo que el parser consume es el JSON-LD.
  El fixture es el bloque extraído, scrubbeado (teléfono/dirección falsos) — chico, versionable y
  testeable. El HTML completo no aporta regresión de DOM que el JSON-LD no cubra, y evita 500 KB de
  contenido scrapeado en el repo.

**Consecuencia de diseño — el costo manda.** El card da los 30 listings con **1 request**, mientras
que el detalle cuesta **1 request por publicación** (30 requests ≈ 30 minutos al budget de ~1/min).
Por eso el reparto correcto es el que ya planteaba F5: **card = features de volumen (1 request por
búsqueda); detalle = solo al 👍**, que además es donde el usuario ya pidió el contacto. A2 confirma
que el modelo de costo de F5 es el correcto, no un compromiso.

**Hallazgo lateral que vale como bug de infra:** configurar **`HTTP_PROXY`** (o `HTTPS_PROXY`) en el
contenedor de FlareSolverr **rompe su arranque** — Chromium los lee del entorno y su test de
instalación muere con `Error getting browser User-Agent`, dejando el contenedor en `Exited (1)`. La
variable correcta es **`PROXY_URL`**. Es el mismo envenenamiento de entorno que en Go con
`ZONAPROP_PROXY`, ahora del lado del browser. Se documenta en G1.

**Caveat honesto, no verificado:** no se pudo confirmar que `telephone` **varíe entre
publicaciones** ni que sea el teléfono del anunciante y no uno genérico. La evidencia indirecta es
fuerte (está en el JSON-LD del `Apartment` específico, junto a la dirección de esa publicación), pero
**hay que medirlo sobre ≥2 detalles antes de confiar en la feature**. El segundo intento falló por
challenge, así que queda pendiente para una próxima ventana con IP fresca.

## Efectos de A2

| Fase | Cambio |
|---|---|
| F5 | **Desbloqueada.** Implementación concreta: parsear el JSON-LD `Apartment` del detalle y leer `telephone`. **No hay email**: la feature manda teléfono o nada, siempre con el link |
| F5 | 1 request por 👍 (no por listing) → el carril prioritario y el budget siguen siendo válidos |
| F5 | El timeout de 150s del primer intento es inaceptable para un camino interactivo; `maxTimeout` ≤60s y el 👍 no debe colgar el callback |
| B2 | El parser de detalle es un **parseo de JSON-LD**, no de needles HTML — mucho más robusto y testeable |
| G1 | `PROXY_URL` en FlareSolverr, **nunca** `HTTP_PROXY`/`HTTPS_PROXY` en su entorno |
| Plan | El techo de ~1 req/min sigue mandando: A2 volvió a escalar Cloudflare en 2-3 requests |

## **HALLAZGO DOMINANTE: Cloudflare escala rapidísimo**

Después de ~4 requests en 10 minutos, **la URL conocida-buena dejó de funcionar**:

```
23:12:51  Challenge detected. Title found: Just a moment...
23:13:50  Error solving the challenge. Timeout after 60.0 seconds.
```

El mismo fetch que había dado 30 tarjetas en 2.8s ahora no pasa. Esto es degradación de reputación
de IP, y reproduce exactamente lo que `tasks/todo.md:32` ya documentó en su momento. Es el
restricción que manda sobre todo el diseño:

1. **El presupuesto de fetch no es un detalle, es el requisito #1.** ~1 req/20s puede ser
   demasiado agresivo. Hay que ir a **1 req/minuto** o menos, y con ventana deslizante por IP.
2. **Los reintentos multiplican el daño de forma letal.** Un fallo de challenge con `maxTimeout`
   150s × 3 intentos = **7.5 minutos de browser churn** sobre una IP ya sospechada. Y la validación
   profunda agrega sus propios 5 intentos encima. `retries internos = 0` durante validación (E3)
   pasa de "optimización" a "obligación", y `maxTimeout` debe bajar.
3. **Compartir IP entre usuarios socializa el daño.** Un usuario nuevo haciendo onboard con 5 URLs
   puede tumbarle el bot al operador. El carril prioritario y el cap por usuario no son
   defensables, son obligatorios. Y esto reabre la decisión de **acceso abierto vs allowlist**: ver
   Open Questions en `tasks/plan.md`.
4. **El cooldown importa.** Tras bloquearnos, hay que esperar antes de reintentar; un loop de
   reintento ajustado empeora permanentemente la reputación.

## A1 — DOM real: qué features tienen varianza de verdad

Medido sobre las 30 tarjetas reales del fixture commiteado. Esto es lo que determina si el modelo
tiene señal con la cual trabajar, y **cuatro supuestos del ciclo 2 caen**:

| Campo | Realidad medida | Consecuencia |
|---|---|---|
| `3 amb.` | **30/30 → constante** | `rooms` sin señal ✓ (ya se había excluido) |
| `2 dorm.` | **30/30 → constante** | ✗ El ciclo 2 decía que dorm variaba; **no varía** en esta búsqueda |
| `baños` | **13×"1 baño", 16×"2 baños", 1×"3 baños"** | ✓ **Sí varía** — señal real |
| `m² tot.` | **30/30**, rango 58-141 con outliers **1** y **12500** | ✓ base del `ppm2`, pero exige guarda |
| `m² cub.` | **0 apariciones** | ✗ F1 lo usaba como denominador preferido → habría dado `unknown` en el 100% |
| `precio` | **30/30 `USD nn.nnn`**, rango 119.000-198.000 | ✓ varía, formato uniforme |
| **`expensas`** | **existe**: `data-qa="expensas"`, `$ 180.000 Expensas`, en **25/30** | ✗ **El ciclo 2 lo eliminó mal.** Dijo "0 coincidencias en el repo" — cierto del repo, **falso de Zonaprop**. Vuelve al modelo |
| `location` | `barrio, partido`: 19 únicos de 30; **solo 2** terminan en macrozona | La heurística debe ser "último segmento, salvo macrozona → primero" |

**`price_per_m2` con los datos reales:** rango **896 a 2759 USD/m²** (mediana 1872).
- bin **250** → **9 buckets** no vacíos de 28 muestras ✓ (valida el tamaño elegido en F1)
- bin 500 → 5 buckets · bin 1000 → 3 buckets

**Señal disponible en v1, honestamente:** `partido` (19 valores sobre 30 tarjetas → la mayoría de
los buckets tendrá n=1-2 y **nunca alcanzará el n≥3** para mostrar una razón), `baños` (3 valores),
`expensas` (25 valores, ARS), `ppm2` (9 buckets). El modelo **sí** tiene de qué aprender, pero la
fricción real es la cardinalidad de `partido`: es la feature más predictiva y la que menos datos
por bucket tiene.

## Efectos sobre otras fases

| Fase | Cambio |
|---|---|
| README | "Zonaprop acepta la huella TLS de Chrome" **obsoleto**; FlareSolverr obligatorio |
| B1 | `m2_cub` no existe en el DOM real → NULL casi siempre; guarda de outliers m²∈[10,1000] |
| B1 | **`Expensas *int64` vuelve** al modelo (existe, 25/30, en ARS) |
| B1 | `dorm` es constante en esta búsqueda: se parsea igual, pero no esperar señal |
| B2 | Filtro PROPERTY se conserva como red de seguridad, no por volumen (0% emprendimientos) |
| B2 | Heurística `partido` invertida: **último** segmento por defecto, primero si es macrozona |
| B3 | `Result{Status,Header}` pasa a ser bloqueante para E3 (confirmado: `cf-mitigated` es header) |
| B3 | `maxTimeout` **≤60s**, reintentos FS **≤1**, cooldown obligatorio |
| B3 | `FLARESOLVERR_URL` requerida en modo servidor; `/v1` confirmado (`/v2` = 404) |
| C2 | `listings` agrega `recency_rank INT` (posición en el fetch ordenado al verse por primera vez) |
| E5 | El baseline pasa de "páginas 1..3" a **página 1** (única que existe) |
| E2 | `/addurl` debe advertir la cobertura: "monitoréo las ~30 más nuevas de esta búsqueda" |
| F1 | Tie-break por `listing_id` **descartado**; va `recency_rank` (ver F1) |
| F1 | `ppm2` sobre `m2_tot` con outliers excluidos; `banios` y `expensas` entran; `dorm` no aporta |
| F4 | **Se elimina la paginación**; `last_card_count` monitoriza el techo de 30 |
| F5 | **Desbloqueada** por A2: teléfono vía JSON-LD, sin email |

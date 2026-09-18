# property-search

Bot de Telegram que vigila búsquedas de **Zonaprop**, manda las publicaciones nuevas todos los días
y **aprende de tus 👍/👎** para ordenarlas. El modelo predice, no filtra: decidís vos.

Cualquiera puede darse de alta con `/start`, pero **las notificaciones no arrancan hasta que el
operador habilite al usuario**. Esa es la única barrera, y es deliberada: el presupuesto de requests
contra Zonaprop es el recurso escaso.

## Cómo funciona

1. **`/start`** te pide la URL de una búsqueda de Zonaprop (la armás con los filtros que quieras y
   copiás la barra del navegador) y un nombre corto para reconocerla. Hasta 5 búsquedas por usuario.
2. El bot **valida** la URL: primero el formato (`https` y host de Zonaprop), después fetchea de
   verdad y confirma que traiga publicaciones.
3. Cada día a las **09:00** (hora de Buenos Aires) baja tus búsquedas **ordenadas por más recientes**
   y te manda **todas** las publicaciones que todavía no te mostró, cada una con foto, precio, m²,
   ambientes, expensas, ubicación, el link directo y botones 👍/👎.
4. Tus calificaciones reentrenan tu modelo. Las publicaciones se ordenan por lo que aprendió y cada
   tarjeta muestra su puesto del día (`#2 de 14 hoy`) y hasta dos razones en castellano
   (`San Isidro: te gustaron 4 de 5`). **Si no hay datos suficientes, no muestra razones ni
   porcentajes.**
5. Un **👍** te manda además el **teléfono del aviso**, extraído del detalle. Si no está, no pasa
   nada: el link ya estaba en la tarjeta.
6. Con `/model` ves qué aprendió, cuántas calificaciones tiene y qué tan bien viene ordenando.

## Estado del despliegue

Este repositorio deja el stack **listo para desplegar**. **El despliegue en la Raspberry Pi 5 no está
hecho**: no hay contenedores corriendo en el Pi, ni una alerta real verificada. El runbook de abajo es
lo que falta ejecutar. Tampoco se verificó una tarjeta llegando a Telegram (no hay token en el entorno
de desarrollo).

## Servicios

Tres contenedores en la misma red, más una base y un browser:

| Servicio | Rol |
|---|---|
| `zonaprop-bot` | El bot en Go. Imagen `scratch`, estática, sin CGO. |
| `flaresolverr` | Resuelve el challenge de Cloudflare con un Chromium real. **Obligatorio**, ver abajo. |
| `postgres` | Estado: usuarios, búsquedas, publicaciones, entregas, calificaciones y pesos. |

### Por qué FlareSolverr es obligatorio

Zonaprop responde `403` con `cf-mitigated: challenge`: un challenge que exige ejecutar JavaScript.
**Ningún cliente HTTP lo pasa, tenga la huella TLS que tenga** — se midió con `curl` y con
`tls-client` usando el perfil `Chrome_152`, ambos por proxy. La única vía que devolvió tarjetas fue
FlareSolverr.

**La versión importa, y mucho.** Con `v3.3.20` (Chromium 120, de diciembre de 2023) el challenge
**no se resolvía nunca**: se probó con y sin proxy, y desde dos egresos distintos, siempre
`Error solving the challenge`. Un browser de 2023 es trivialmente detectable. Con **`v3.5.2`**
(Chromium 152) resuelve en ~11 segundos. Por eso el pin está fijado y **no** en `:latest`: la
versión es parte de la configuración, no un detalle.

### Por qué el presupuesto de fetch es lo primero

Durante las mediciones, Cloudflare escaló **después de ~4 requests en 10 minutos** y la URL que
funcionaba dejó de resolver. Por eso:

- `FETCH_RATE_LIMIT` (default `60s`) espacia los requests a Zonaprop.
- `maxTimeout` de FlareSolverr es 60s y **no se reintenta**: cada intento lanza un browser.
- Un challenge **corta los reintentos** y aplica un enfriamiento de 5 minutos.
- Solo se pagina lo que existe: la primera página (~30 publicaciones). La paginación server-side **no
  existe** en las URLs SEO de Zonaprop — `?n_pg=2` devuelve lo mismo.

## Configuración

Ver `.env.example`. Lo obligatorio es `TELEGRAM_BOT_TOKEN`, `FLARESOLVERR_URL` y `POSTGRES_*`.

`SEED_CHAT_ID` + `SEED_URLS` son un **bootstrap opcional de una sola vez**: siembran tu usuario y
tus búsquedas en el primer arranque, para no tener que darte de alta a mano. Después de esa corrida
queda marcado en la base y no se vuelve a aplicar — **a partir de ahí la fuente de verdad es
Postgres**, y tus búsquedas se manejan con `/addurl` y `/rmurl`. Sin esa marca, un reinicio
resucitaría lo que borraste desde el chat.

### Tus búsquedas viven en Postgres

Una vez que el bot arrancó una vez, las búsquedas son filas de la base y se manejan desde el chat
(`/addurl`, `/rmurl`, `/list`). El `.env` ya no participa: editar `SEED_URLS` después no cambia
nada, porque el bootstrap ya se aplicó.

⚠️ **FlareSolverr usa `PROXY_URL`, nunca `HTTP_PROXY`/`HTTPS_PROXY`.** Chromium lee esas variables
del entorno, su test de arranque falla con `Error getting browser User-Agent` y el contenedor muere;
con él se cae todo el camino de fetch. El proxy del bot se llama `ZONAPROP_PROXY` por el mismo
motivo: Go lee `HTTP_PROXY` del entorno por defecto y enrutaría Telegram y FlareSolverr a través del
proxy rotativo.

## Despliegue (en la RPi 5)

```bash
git clone <repo> && cd property-search
cp .env.example .env       # completar TELEGRAM_*, POSTGRES_PASSWORD, FLARESOLVERR_PROXY_URL
docker compose down --remove-orphans   # quita contenedores de nombres viejos
docker compose up -d --build
docker compose logs -f zonaprop-bot
```

La imagen es multi-stage (`scratch` + CA certs + zoneinfo embebida), `linux/arm64`, y compila nativo
en el Pi.

### Activar usuarios

Nadie recibe el digest hasta que lo habilites:

```sql
-- quién está esperando
SELECT chat_id, onboarded_at FROM users
 WHERE active = false AND state = 'ready' ORDER BY onboarded_at;

-- habilitar
UPDATE users SET active = true WHERE chat_id = <id>;
```

El aviso automático al operador cuando alguien completa el onboarding está diferido: con caudal bajo
la consulta de arriba alcanza.

### Rotar la contraseña de Postgres

`POSTGRES_PASSWORD` solo se usa al inicializar el volumen. Cambiarla en `.env` después no cambia la
del cluster:

```sql
ALTER ROLE zonaprop WITH PASSWORD 'nueva';
```

y actualizá `.env` en el mismo momento.

## Logs

El bot escribe a stdout en texto legible; se leen con `docker compose logs -f zonaprop-bot`. El nivel
se controla con `LOG_LEVEL` (`debug | info | warn | error`, default `info`).

- **INFO** — hitos y una línea de resumen por operación.
- **WARN** — algo se degradó pero el bot siguió: challenge de Cloudflare, un fetch que falla, un
  update descartado, una URL que no se pudo validar.
- **ERROR** — solo un fallo que aborta el arranque o un ciclo completo.
- **DEBUG** — detalle por ítem: modo, pacing y URL de cada fetch, stats de parseo, resultado por URL.

Ejemplo del resumen diario por usuario:

```
time=... level=INFO msg="digest: user done" user=42 searches=3 fetched=3 candidates=18 sent=15 cap_hit=true ms=8420
```

Lectura: 3 búsquedas, 3 respondieron, 18 candidatas, 15 enviadas, y el tope diario cortó el resto
(`cap_hit=true`). Si `fetched` es menor que `searches`, un WARN de arriba dice cuál falló y por qué.

Ejemplo de bloqueo:

```
time=... level=WARN msg="fetch: blocked; gate cooling down" mode=flaresolverr status=403 cooldown=5m0s
```

**Privacidad.** Los identificadores (`user`, `chat`, `label`, `listing`) salen en INFO; la URL
completa de una búsqueda solo aparece en DEBUG. El token del bot sigue redactado por `httpx.Redact`.

**DEBUG para diagnosticar.** Subilo temporalmente (`LOG_LEVEL=debug` en `.env`) y volvé a `info` al
terminar: es detalle por ítem y no está pensado para dejarlo prendido.

## Desarrollo

```bash
nix develop                    # toolchain Go reproducible (esta máquina no tiene Go global)
make fmt vet test              # formato, vet y tests
make run                       # dry-run local (sin token imprime por consola)
make up / make down            # postgres + flaresolverr para desarrollo
make probe URL='https://...'   # fetch real + parseo + resumen, sin tocar la base
```

### Tests

`go test ./...` cubre, entre otras cosas:

- **Fetch**: perfiles TLS, proxy, reintentos con backoff, y la **clasificación** (`blocked` vs
  `transport` vs `http-status`) que decide si una URL se reintenta o se rechaza.
- **Parser**: contra `fixtures/search_gba_norte.html`, una página real de Zonaprop. Valida los 30
  ids, los buckets de precio y tamaño, la guarda de outliers de m² y que un valor ausente quede en
  `NULL` y no en 0.
- **Postgres**: migraciones idempotentes, seeds, y **aislamiento entre usuarios** (A no ve las
  búsquedas ni la entrenan los datos de B) contra una base real y descartable por test.
- **Digest**: que la primera indexación **baselinee** sin mandar, que re-ejecutar no re-mande, que el
  tope diario **difiera** en vez de descartar, y que un día interrumpido se reanude.
- **Scoring**: partición por `operation_type:currency`, `unknown` en vez de 0, razones solo con
  evidencia suficiente, y que un modelo vacío puntúe 0.
- **Onboarding**: rechazo de dominios que no son Zonaprop, inyección del orden por fecha, cap de
  búsquedas y colisión de nombres.
- **End-to-end**: el pipeline completo (config → Postgres → fetch real → parser → baseline) contra el
  fixture real servido por HTTP local, sin tocar Zonaprop.

## Seguridad

Qué se hizo y qué queda abierto, sin adornos.

**Fronteras de confianza.** Lo único que escribe un tercero es el **HTML de Zonaprop** y los
**updates de Telegram** (el acceso es abierto: cualquiera puede `/start`). El `.env` lo escribe el
operador, así que se trata como confiable: si alguien puede editarlo, ya puede ejecutar como el
usuario del bot.

**Qué está cubierto:**

| Riesgo | Control |
|---|---|
| SSRF por el link de la tarjeta | `canonical()` **rechaza hrefs que resuelven a otro host** y los cuenta (`SkippedOffsite`). La URL canónica se fetchea después por el detalle, así que seguir un link absoluto como `http://169.254.169.254/` convertiría al bot en un request forger dentro de la red del operador |
| SSRF por la foto | Solo se descargan imágenes de `zonapropcdn.com` / `zonaprop.com.ar` por https. Si el host no está permitido la tarjeta sale igual, **sin foto** (fail closed) |
| SSRF por la URL de búsqueda | `IsZonapropURL` en el alta: https y host exacto o sufijo con punto. Un `Contains` dejaría pasar `zonaprop.com.ar.attacker.test` |
| Token del bot en los logs | `httpx.Redact` borra el token de los errores de red: `net/http` incluye la URL, y la URL lleva `/bot<token>/` |
| Callback forjado o repetido | Se valida contra `deliveries`: una publicación que no se le entregó a ese usuario se rechaza |
| Un usuario viendo datos de otro | Toda query de `repo` lleva `user_id` y las candidatas se acotan por `listing_sources`. Hay test de aislamiento |
| Secretos en el repo | `.env`, `opencode.json`, `*.pem` y `*.key` en `.gitignore`; nada de eso está trackeado |
| Fuerza bruta de fetch | El `Gate` pacea **todo** Zonaprop a 1 req/min con cooldown tras un challenge, así que un abuso no puede producir un flood |
| Vulnerabilidades de dependencias | `govulncheck ./...` sin hallazgos alcanzables |

**Qué queda abierto, y por qué:**

- **Onboarding abierto + presupuesto compartido.** Un desconocido puede dar de alta 5 URLs de
  Zonaprop y consume turnos de validación. Como el `Gate` es global y secuencial, el daño máximo es
  **demora** (no flood, no quema de IP), pero puede atrasar el digest del operador. Un tope de
  usuarios lo cerraría; cambia la política de acceso, así que se decide aparte (ver abajo).
- **Datos personales sin TTL.** `chat_id` y las calificaciones son datos personales. `/borrardatos`
  los borra en cascada, pero no hay expiración automática. `deliveries` y `digests` crecen sin
  límite: hoy es un tema de disco, no de privacidad.
- **`golang.org/x/crypto/openpgp`** aparece en el audit como "unsafe by design" y **sin fix**. No se
  usa ni se importa; se deja documentado en vez de silenciado.

## Límites conocidos

- **Cobertura**: solo la primera página de cada búsqueda (~30 publicaciones más nuevas). Zonaprop no
  expone paginación server-side en esa URL.
- **FlareSolverr en arm64**: la imagen pinneada está validada en x86_64. Falta confirmar que
  arranque en el Pi (validación de arm64, fuera de este alcance).
- **Telegram**: verificado end-to-end localmente (llegada de la tarjeta, baseline, envío de una
  publicación nueva y calificación), pero **no en el Pi**.
- **Teléfono**: no se confirmó que el `telephone` del detalle varíe por publicación. Si resultara
  genérico, el 👍 manda solo el link.
- **Modelo**: con pocos datos el orden es prior más ruido; la UI lo dice en vez de disimularlo.

# Implementation Plan: Bot de calificación de propiedades (v2)

Plan de nivel superior. La lista de tareas ejecutable está en **`tasks/todo.md`**; el racional de
diseño en **`tasks/specs/`**; la evidencia empírica en **`tasks/research/probe-results.md`**.
La idea y sus trade-offs en `docs/ideas/bot-calificacion-propiedades.md`.

Este plan **reemplaza** al bot alertador v1, archivado en `tasks/archive/v1-zonaprop-bot/`.

## Overview

Bot conversacional de Telegram en Go. Un usuario hace `/start` en un chat privado, entrega sus URLs
de búsqueda de Zonaprop con un nombre corto y el bot las valida. Cada día a las 09:00
(America/Argentina/Buenos_Aires) baja esas URLs **ordenadas por más recientes** y manda **todas** las
publicaciones que ese usuario aún no recibió, ordenadas por un score aprendido, con foto,
prestaciones, link directo y botones 👍/👎. El modelo **predice, no filtra**. Un 👍 dispara la
extracción *best-effort* del **teléfono** desde el detalle. Estado en Postgres; ratings y pesos
**estrictamente por usuario**. Se admiten **alquileres y pesos (ARS)** además de ventas en USD: el
modelo particiona por `operation_type` × `currency` para no comparar magnitudes incomparables.
Pensado para desplegarse con docker compose (bot + FlareSolverr + Postgres) en RPi5 arm64, aunque
**el deploy en sí queda fuera de este plan** (ver V7); dev con el flake de NixOS.

## Architecture Decisions

Decisiones con evidencia detrás; el detalle y el "por qué" están en los specs.

- **FlareSolverr es obligatorio, no opcional.** Medido: el 403 de Zonaprop trae
  `cf-mitigated: challenge`; ni curl ni `tls-client` con huella `Chrome_152` vía proxy lo pasan.
  `FLARESOLVERR_URL` se requiere en modo servidor. El README del repo queda obsoleto.
- **El budget de fetch es el requisito #1.** Cloudflare escaló tras **~2-4 requests** y la URL
  conocida-buena dejó de resolver. Token bucket **1 req/60s** (ráfaga 1) con ventana deslizante, `maxTimeout`
  **= 60s**, reintentos FS **≤1**, cooldown tras challenge, carril prioritario para el operador y los
  👍, y **un fetch por `url_norm` por corrida** compartido entre suscriptores.
- **Solo existe la página 1.** Medido: `?n_pg=2` devuelve los mismos 30 `data-id` y `n_pg=2`
  aparece 0 veces en el HTML. Cobertura = las ~30 más nuevas de cada búsqueda; `/addurl` lo advierte
  y `last_card_count` monitoriza el techo.
- **Dedup por `data-id`** (`zonaprop_id`), no por URL: los hrefs traen `n_pg`/`n_pos`. Y **no por
  `listing_id`** para recencia: los `data-id` no son monotónicos y la ingesta es "más nuevo
  primero", así que el id más bajo es el más nuevo del lote → va `recency_rank`.
- **`deliveries` es la fuente de verdad de la entrega**, con snapshot de features al enviar.
  Invariante: *enviar → `INSERT … ON CONFLICT DO NOTHING`*; duplicado raro antes que pérdida
  silenciosa; reconciliación de `message_id IS NULL`.
- **Baseline silencioso sobre la página 1** al validar una URL: marca el inventario como entregado
  sin mandarlo. Reemplaza la migración de `seen.jsonl`, que se descarta.
- **Activación manual (`users.active`), no allowlist.** Cualquiera puede `/start`, pero el digest
  solo recorre `active=true`, que el operador prende a mano. `active=false` (operador) y
  `state='stopped'` (usuario) son independientes.
- **Identidad por `from.id`**, no `chat_id`; onboarding rechazado si `chat.type != private`.
- **Modelo transparente y literal:** buckets `(feature, value)` con Laplace,
  `score = mean(ln(rate/0.5))`, orden determinista, **razones solo con n≥3**. Sin porcentaje, sin
  umbral, sin exploración. Retraining nocturno **por usuario**, con `active_model_version` volteado
  en la misma transacción.
- **Se admiten alquileres y pesos.** `operation_type` (`venta`|`alquiler`) y `currency`
  (`USD`|`ARS`) son datos de primera clase, pero **claves de partición, no features de scoring**:
  toda feature numérica se namespacia (`ppm2:venta:USD:1500-1750`, `expensas:alquiler:ARS:250k-300k`).
  Como features, el modelo aprendería "te gustan los alquileres", que no significa nada. Con
  partición aprende *qué* alquileres te gustan. `currency` vacío ⇒ features numéricas a `unknown`,
  que es la guarda contra el outlier de 1000× (ARS puntuado contra banda USD). Sin tipo de cambio.
- **Features medidas sobre el DOM real:** `partido` (último segmento, o el primero si es macrozona;
  sin particionar), `ppm2_bin` sobre `m2_tot` con outliers `m²∈[10,1000]` descartados, `m2_bin`,
  `banios` (sin particionar), `expensas` (existe en `data-qa="expensas"`, namespaciada).
  **`rooms` y `dorm` fuera del scoring** (30/30 constantes). `title_token` se guarda, no puntúa.
  Solo tarjetas `PROPERTY`. Ausentes → `unknown`, **nunca 0**.
- **Contacto: solo teléfono**, vía JSON-LD `Apartment.telephone` del detalle (HTML estático, 1.9s,
  1 request **por 👍**, no por publicación). No hay email. El link va **siempre** en la tarjeta.
- **`ZONAPROP_PROXY`, no `HTTP_PROXY`** en el bot (la stdlib de Go leería `HTTP_PROXY` y envenenaría
  Telegram, FlareSolverr y las fotos). Y en el contenedor de FlareSolverr va **`PROXY_URL`
  únicamente**: medido, `HTTP_PROXY` ahí rompe su arranque.
- **Telegram bidireccional:** long-poll con offset persistido dentro de la transacción del handler,
  dead-letter, lease por advisory lock, throttle **por chat**, caption en **UTF-16** reservando la URL.
- **Scheduler tz-aware** con `import _ "time/tzdata"` (la imagen `scratch` no trae zoneinfo): sin
  eso las 09:00 disparan a las 06:00. `SCHEDULE_TZ` con default explícito.
- **Persistencia:** Postgres con `pgx` (Go puro, sin CGO), migraciones embebidas y seeds
  idempotentes al arrancar. Testcontainers para integración, con `t.Skip` si no hay daemon.

## Task List

Tareas detalladas, con acceptance criteria, verificación, archivos y alcance, en `tasks/todo.md`.
Acá va el índice y el orden.

### V1: Camino de valor central — seed → fetch → manda con botones
*El primer entregable usable. Acá vive el riesgo de Cloudflare.*
- [x] V1.1 Config mínima + contenedores (config, compose base)
- [x] V1.2 Postgres: migrador, seeds y esquema mínimo
- [x] V1.3 Fetch: `Result{Status,Header}`, FS obligatorio, budget y carril prioritario
- [x] V1.4 Parser de card tipado sobre fixture real
- [x] V1.5 Dedup + baseline silencioso + envío con 👍/👎
- [x] V1.6 Verificación E2E (dry-run y Telegram real)

### Checkpoint V1
- [ ] Un usuario sembrado recibe sus publicaciones nuevas una sola vez, con botones
- [ ] Reiniciar el proceso no re-manda nada
- [ ] El budget de fetch se respeta (log de rate)

### V2: Califico y el bot lo persiste
- [x] V2.1 Telegram inbound: long-poll, offset persistido, dead-letter, lease
- [x] V2.2 Callbacks: respuesta, keyboard revocado, first-tap-wins, tap tardío
- [x] V2.3 `ratings` + `deliveries.status` en una transacción
- [x] V2.4 `/model` con conteos por bucket

### Checkpoint V2
- [x] Un tap produce exactamente un rating, con callback respondido y keyboard revocado
- [x] Un restart no pierde ni duplica updates

### V3: Reordena por lo aprendido y explica
- [x] V3.1 Buckets + score literal + orden determinista
- [x] V3.2 Retrain por usuario con versión atómica y prune
- [x] V3.3 Razones n≥3 + concordancia pairwise en `/model`

### Checkpoint V3
- [x] Con <30 ratings la UI no inventa razones ni porcentajes
- [x] El orden con scores empatados es estable y a favor de lo más nuevo

### V4: Corre solo todos los días
- [x] V4.1 Scheduler tz-aware (tzdata, `SCHEDULE_TZ`, próximo 09:00, sin boot run)
- [x] V4.2 `digests` + resume + input auto-sanante
- [x] V4.3 Tope diario con excedente + monitoreo del techo de 30

### Checkpoint V4
- [x] El digest corre solo para `active=true` y reanuda tras un restart a mitad
- [x] Un día interrumpido no se pierde

### V5: `/start` para desconocidos
- [x] V5.1 Máquina de estados + escritor serializado + comandos
- [x] V5.2 Validación sintáctica + `url_norm` + orden auto-inyectado + cap 5 + label único
- [x] V5.3 Validación profunda con taxonomía, backoff, tope de intentos y canario
- [x] V5.4 Mensaje de activación pendiente + `/list` con estado

### Checkpoint V5
- [x] Una URL de otro dominio se rechaza con mensaje claro
- [x] Agregar una URL no reproduce su historial
- [x] El usuario sabe que las notificaciones no arrancan hasta que lo activen

### V6: Al 👍 me manda el teléfono
- [ ] V6.1 Parser de JSON-LD + `listing_contacts` con TTL
- [ ] V6.2 Flujo: link primero, intento después, segundo mensaje solo si hay teléfono

### Checkpoint V6
- [ ] Un 👍 manda el link al instante y el teléfono si aparece
- [ ] Un fallo de challenge no deja al usuario sin nada

### V7: Preparación de deploy y cierre
- [ ] V7.1 Compose final
- [ ] V7.2 `.dockerignore`, Dockerfile, runbook
- [ ] V7.3 `internal/config` final + código muerto (`internal/store`, `cmd/probe`, `internal/bot`)
- [ ] V7.4 Docs y toolchain (README, `.env.example`, Makefile, flake)

> **El despliegue real en la RPi 5 queda FUERA de este plan** (decisión del usuario). V7 deja todo
> **listo para desplegar**: compose validado con `docker compose config`, imagen que buildea, runbook
> escrito y config final. La ejecución en el Pi — levantar los contenedores, validar el arm64 y
> confirmar la alerta real — se planifica aparte, cuando haya acceso al host.

### Checkpoint: Completo
- [ ] `go test ./...` verde y `docker compose config` válido
- [ ] La imagen buildea y el runbook está escrito y revisado
- [ ] **Listo para desplegar**: el deploy en el Pi es un plan aparte, no este

## Dependency Graph

```
V1 (camino central: config mínima + PG + fetch + card + envío)
 │
 ├── V2 (inbound + callbacks + ratings)
 │     └── V3 (modelo)
 │           └── V4 (scheduler)
 │
 ├── V6 (contacto)          ← no comparte archivos con V2/V3/V4
 │
 └── V5 (onboarding público) ← reusa validación profunda de V1.3
       │
       └── V7 (deploy)
```

**Paralelizables** una vez cerrado V1: {V2→V3→V4} con {V5} con {V6}.
**Secuencial sí o sí:** V1 primero; V4 después de V3 (el digest consume el score).

## Mapping: rebanada → racional de diseño

| Rebanada | Specs (`tasks/specs/`) | Evidencia |
|---|---|---|
| V1 | `ingesta.md`, `persistencia.md`, `telegram.md`, `onboarding.md` §E5 | `probe-results.md` A1/A3 |
| V2 | `telegram.md`, `persistencia.md` | — |
| V3 | `modelo-digest.md` §F1–F3 | `probe-results.md` (DOM real) |
| V4 | `modelo-digest.md` §F4 | — |
| V5 | `onboarding.md` §E1–E4 | `probe-results.md` A1 |
| V6 | `modelo-digest.md` §F5 | `probe-results.md` A2 |
| V7 | `deploy-config.md` | `probe-results.md` A2/A3 |

## Migration and Coexistence

- `seen.jsonl` **se descarta** (residuo de localhost + sha1 de URLs malformadas). El baseline
  silencioso es el mecanismo anti-respam del día de upgrade.
- `internal/store` se elimina; `cmd/probe` pasa a tomar URLs por argv; `internal/bot` y su interfaz
  `Notifier` se reescriben.
- `TestFirstRunNotifiesEverything` se reemplaza por un test de baseline: cambia un comportamiento
  documentado en el README, y eso se anota.
- v1 archivado en `tasks/archive/v1-zonaprop-bot/`. **Ni v1 ni v2 se despliegan en el Pi dentro de
  este plan**: el alcance termina en "listo para desplegar".

## Risks and Mitigations

| Riesgo | Impacto | Mitigación |
|--------|---------|------------|
| Degradación de IP ante Cloudflare | **Crítico, medido** | 1 req/60s con ventana, `maxTimeout`=60s, reintentos ≤1, cooldown, carril prioritario |
| FlareSolverr caído = bot mudo | Alto | `FLARESOLVERR_URL` requerida en modo servidor; healthcheck real en compose |
| Modelo débil con poco volumen | Alto | Razones solo n≥3, `/model` suprimido n<30; `partido` tiene 19 valores sobre 30 tarjetas |
| Teléfono genérico en vez de por aviso | Medio | **Sin verificar**: medir ≥2 detalles con IP fresca antes de confiar en V6 |
| Techo de 30 por búsqueda | Medio | Orden reciente lo mitiga; `last_card_count` avisa de saturación |
| Instancia de dev roba updates de prod | Medio | Lease por advisory lock + token separado en dev |
| Cambio de DOM de Zonaprop | Medio | Fixture real commiteado + test de 0 tarjetas + canario del operador |
| `HTTP_PROXY` envenena entornos | Medio | `ZONAPROP_PROXY` en el bot; **solo `PROXY_URL`** en FlareSolverr |
| Crecimiento de logs en la SD del Pi | Bajo | Rotación json-file 10m×3 en los tres servicios |

## Open Questions

### Cerradas / diferidas

- **¿El teléfono del JSON-LD varía por publicación?** **Se espera que sí** (decisión del usuario).
  No está verificado porque el segundo detalle falló por challenge. **Verificar sobre ≥2 detalles con
  IP fresca antes de V6.** Si resultara genérico, V6 se degrada a "manda solo el link" — el diseño ya
  lo soporta, porque el link va siempre en la tarjeta.
- **¿Cuántos ratings juntás por mes?** **Se mide más adelante**, con uso real. Define cuándo el
  ranking con razones reemplaza al modo recencia. No bloquea nada: la UI ya se calla sola con n<3 y
  n<30.
- **Aviso al operador de usuarios esperando activación: DIFERIDO.** Con caudal bajo no se justifica
  la feature. **Proceso manual mientras tanto** (documentado en el runbook de V7.2):
  `SELECT chat_id, onboarded_at FROM users WHERE active = false AND state = 'ready' ORDER BY onboarded_at;`
  Cuando el caudal crezca, se implementa la alerta al `OPERATOR_CHAT_ID` al completar el onboarding.
- ¿Qué pasa con los alquileres y las monedas? **Resuelto:** se admiten. `operation_type` y
  `currency` se detectan en el parser y son **claves de partición** del modelo, no features de
  scoring. No se usa tipo de cambio: cada namespace tiene su escala (lineal validada para `venta:USD`,
  logarítmica para el resto).

### Abiertas

- ¿Política de retención de datos personales? `/borrardatos` existe, no hay borrado automático.
- ¿`partido` justifica su cardinalidad? Casi ningún bucket llega a n≥3 → las razones casi nunca se
  muestran. Evaluar agrupar por región o aceptar que v1 rara vez explique.

# Área: Onboarding y validación
> **Este documento es racional de diseño, no la lista de tareas.** La lista ejecutable,
> con acceptance criteria, verificación y alcance por tarea, está en `tasks/todo.md`.


**Objetivo:** que un desconocido pueda darse de alta sin que eso se convierta en un SSRF hacia la
LAN del operador, y que una URL válida no quede colgada en `pending` para siempre.

**Alimenta:** V5 (alta pública y validación). El **baseline silencioso** (§E5) pertenece a V1, no a V5.

## E1 — Máquina de estados

- AC: transiciones explícitas `idle -> await_url -> await_label -> validating -> idle`, documentadas
  en una tabla. Sin tabla, dos mensajes pegados en un solo paste o un `/start` a mitad de flujo
  interleavan transiciones y pierden o duplican filas.
- AC: **un escritor serializado por usuario** (cola por `user_id`). El poll loop y el scheduler
  corren concurrentes.
- AC: entrada no reconocida en un estado **recibe respuesta**, no se descarta en silencio.
- AC: colisión de `label` -> re-prompt en el estado ("ese nombre ya está usado"), mapeando el error
  23505 al mismo re-prompt. `UNIQUE(user_id,label)` sin camino de UX deja al usuario en silencio o
  con `state='await_label'` trabado.
- AC: solo chat privado. `chat.type != private` -> rechazo con mensaje claro. En un grupo, dos
  personas compartirían modelo, entregas y ratings.

## E2 — Validación sintáctica (inmediata)

- AC: `https` obligatorio **y** host exactamente `zonaprop.com.ar` o con sufijo `.zonaprop.com.ar`.
  `strings.Contains(host, "zonaprop.com.ar")` lo pasan `zonaprop.com.ar.attacker.test` y
  `notzonaprop.com.ar`. Como la validación profunda **fetcha** la URL y el fetch puede salir por
  FlareSolverr — Chromium real en la LAN del operador — y el acceso es abierto, eso es un SSRF no
  autenticado con oráculo de alcanzabilidad sobre la red interna y el router.
- AC: `url_norm` = host lowercase, query ordenado, params de tracking (`n_src`, `n_pg`, `n_pos`,
  `utm_*`) fuera. Sin normalizar, la misma búsqueda se registra dos veces por una barra final o por
  el orden de params: fetch duplicado contra una IP que Zonaprop ya rate-limited. Además, un
  usuario que pega una URL desde la página 2 trae `n_pg=2`. A1 descartó la paginación del bot, así
  que ya no colisiona con nada, pero **la normalización sigue haciendo falta**: sin ella, dos usuarios
  con la misma búsqueda en distinta página o distinto `n_search_id` se cuentan como URLs distintas y
  se fetchea dos veces la misma búsqueda, con `deliveries` y `listing_sources` duplicados.
- AC: **auto-inyección del orden reciente** (decisión de producto, **confirmada por A1**): si el path
  no contiene `-orden-publicado-descendente`, se reescribe la URL, se marca `sort_injected=true` y
  se avisa. A1 midió edades no-decrecientes (2d→3d→4d→5d→6d) sobre la URL con el sufijo: el orden es
  real. Es lo que hace que "las ~30 que vemos" sean efectivamente **las más nuevas**.
- AC: tope 5 URLs por usuario, enforceado dentro de la transacción de insert.
- AC: **la confirmación de `/addurl` debe decir la cobertura real.** A1 probó que la paginación
  server-side **no existe** (`?n_pg=2` devuelve los mismos 30 `data-id`, y `n_pg=2` aparece 0 veces
  en el HTML): el bot monitorea **las ~30 publicaciones más nuevas**, no las 565 que declara la
  búsqueda. Se informa al usuario en el alta, no se deja implícito.
- AC: `/addurl` de una URL ya existente -> `ON CONFLICT (user_id,url_norm) DO UPDATE` que actualiza
  `label` **y resetea** `validation_status='pending', attempts=0, next_check_at=now()`, con aviso de
  re-encolado. Actualizar solo el label deja una URL en `failed` sin forma de reintentarla: un ban
  de IP de un día la brickea para siempre con una fila que en `/list` parece viva.

## E3 — Validación profunda (asincrónica)

- AC: loop validador cada 10 minutos sobre `pending|retrying` con `next_check_at <= now()`.
- AC: clasificación usando `fetch.Result.Status` / `Header` (de B3):
  - **Bloqueado** -> `pending`/`retrying`: header `cf-mitigated`, `<title>Just a moment`, `cf_chl`
    en el body, 403/503, o FlareSolverr `status != ok`.
  - **Válido sin resultados** -> `valid_empty`: página real de Zonaprop (contenedor del listado
    presente) con 0 tarjetas. Este estado no existía y es el que impide que una búsqueda
    legítimamente estrecha quede reintentándose para siempre.
  - **Error de transporte** (dial/TLS/timeout real) -> **nunca degrada** una URL ya `valid`.
- AC: la taxonomía anterior es obligatoria porque "0 tarjetas" significa tres cosas distintas a la
  vez; sin ella la URL o se rechaza mal o queda `pending` eternamente, y como el digest solo fetcha
  URLs `valid`, un falso negativo **silencia la entrega para siempre** sin error visible — el peor
  modo de fallo para un bot cuyo trabajo es que no tengas que mirar Zonaprop vos.
- AC: **retries internos de `fetch` desactivados durante la validación**. Con `FetchRetries=3` cada
  intento son 4 fetches, y 5 intentos = hasta 20 golpes a páginas de challenge desde una IP
  compartida.
- AC: backoff exponencial de `next_check_at` con **techo de 24h** y `attempts <= 5` -> `invalid`
  terminal con motivo, notificando al usuario.
- AC: transición a `valid`, `valid_empty`, `failed` o `invalid` **notifica al usuario**.
- AC: **canario del operador**: la alerta de cambio de DOM solo se dispara si la URL conocida-buena
  del operador **también** devuelve contenedor-con-0-tarjetas en la misma corrida. Con 2-5 usuarios,
  dos búsquedas estrechas vacías el mismo día es rutina y la alerta sería puro ruido. Se entrega por
  `OPERATOR_CHAT_ID`, que no existe hoy: el `Notifier` está cableado a un único `chat_id` de env.
- AC: `/list` muestra `label`, `status` y "última revisión: hace N, sin resultados / error".

## E4 — Comandos

- AC: `/start` (onboarding y reanudación), `/addurl`, `/rmurl <label>`, `/list` (muestra `id ·
  label · status`), `/model`, `/stop` (pausa entrega, conserva datos, `state='stopped'`, verificado
  por el job diario, `/start` reanuda), `/borrardatos` (cascada), `/help`.
- AC: **el mensaje de fin de onboarding debe ser honesto sobre la activación.** Se le dice al usuario
  que quedó registrado y que **las notificaciones diarias no empiezan hasta que se lo habilite**, sin
  prometer una fecha. El usuario puede seguir usando `/list` y `/addurl` mientras tanto. Sin esto, el
  usuario espera un digest que nunca llega y concluye que el bot está roto.
- AC: ningún envío del digest para `active=false` (ver F4). El onboarding **no** activa solo: el
  operador decide, y ese control es justamente el punto de la feature.
- AC: `/criteria` **fuera de v1** — estaba listado sin comportamiento, sin estado y sin soporte en
  el esquema. Requerimiento no implementable descubierto a tiempo.
- AC: `/rmurl` por `label` (único por usuario). Por índice se desfasa al borrar.

## E5 — Baseline silencioso

- AC: cuando una URL pasa a `valid`, se insertan filas de `deliveries` para el inventario de
  **la página 1** — la única que existe server-side, ver A1 — **sin enviarlas**.
- AC: **una sola página, no 1..3.** Originalmente este spec pedía cubrir la misma ventana que leía
  el digest (1..3) porque un baseline de solo página 1 dejaba ~60 tarjetas sin marcar. A1 demostró
  que el digest tampoco puede leer más de la página 1: `?n_pg=2` devuelve **los mismos 30
  `data-id`**. Baseline y digest leen el mismo conjunto por construcción.
- AC: corre bajo el **mismo escritor serializado por usuario** que el digest. Una URL que pasa a
  `valid` a las 08:59 con el digest ya seleccionado produce una tarjeta enviada *y* baselined.
- AC: `ON CONFLICT DO NOTHING` (el send-path usa `DO UPDATE … WHERE message_id IS NULL`).
- AC: **no es bootstrap de ratings** y no contradice "sin filtrado": es la regla estándar de "no
  reproducir el historial al suscribirse". Reemplaza la migración de `seen.jsonl`, que se descarta.
- AC: test que reemplaza a `TestFirstRunNotifiesEverything` (que hoy afirma exactamente lo
  contrario) y nota del cambio de comportamiento en README y `tasks/plan.md`.

## Archivos

`internal/onboarding/*.go` (nuevo), `internal/user/*.go`, `internal/validate/*.go` (nuevo),
`internal/chat/*.go`, tests.

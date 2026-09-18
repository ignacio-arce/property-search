# Área: Telegram (salida + inbound)
> **Este documento es racional de diseño, no la lista de tareas.** La lista ejecutable,
> con acceptance criteria, verificación y alcance por tarea, está en `tasks/todo.md`.


**Objetivo:** convertir un notificador de salida en un chat bot que recibe, responde y no pierde
ni duplica nada. Hoy `internal/telegram` solo envía y tiene un único `chat_id` quemado desde env.

**Alimenta:** V1 (envío con botones), V2 (callbacks), V5 (comandos).

## D1 — Long-poll con offset duradero

- AC: `getUpdates` long-poll (no webhook: el Pi está detrás de NAT y la imagen es `scratch` sin
      listener TLS).
- AC: **cliente HTTP dedicado** con `Timeout = poll_timeout + 10s`. Si reutiliza el cliente actual
      (30s), cualquier `timeout` de long-poll ≥30s — Telegram recomienda 50-60s — se hace matar por
      el propio cliente cada ciclo y genera un flujo de errores espurios.
- AC: `last_update_id` persistido en `bot_state` y **avanzado dentro de la transacción del handler**.
      Offset solo en memoria + `restart: unless-stopped` + reboot del Pi = replay de updates
      recientes (onboardings duplicados, callbacks re-ejecutados).
- AC: **dead-letter tras N intentos** fallidos sobre el mismo `update_id`: se persista el offset
      igual y se alerta al operador. Sin esto, un update tóxico (violación de constraint, campo
      oversized, bug con un payload particular) se re-fetcha y re-falla para siempre, bloqueando
      todos los updates de todos los usuarios, y tras 24h Telegram los descarta en silencio.
- AC: handlers **idempotentes**: la redeliveria tras un commit fallido reenvía replies visibles
      ("URL agregada") si no lo son.
- AC: **lease por advisory lock** en `bot_state`. Telegram entrega cada update a **un solo** caller
      de `getUpdates`: correr `make run` en la laptop contra el token de producción **roba** los
      updates del Pi y desaparecen. El lease niega el arranque o alerta cuando hay otro holder.

## D2 — Callbacks y keyboards

- AC: `reply_markup` inline con 👍/👎; `callback_data` = `"u:<listing_id>"` / `"d:<listing_id>"`
      con el `BIGSERIAL`, **nunca el sha1** de 40 chars. El límite es 64 bytes y hoy
      `"up:"+sha1` = 43: alcanza, pero en cuanto se agrega un segmento se desborda en silencio.
- AC: **siempre** `answerCallbackQuery`. No existe en el repo (cero coincidencias de
      `callback_query`): sin él Telegram muestra spinner y luego un toast de error. Es justamente
      la fricción que mata los datos.
- AC: `editMessageReplyMarkup` revoca el teclado tras el primer tap y `editMessageCaption` agrega
      "✓ te gustó" / "✗ no te gustó" al caption original (el callback trae el caption, así que se
      agrega sin reconstruir la tarjeta y sin perder el ranking ni las razones). Si el caption ya
      está en el límite, se omite la nota: el toast y la revocación del teclado alcanzan.
      **Un toast es fácil de perder y dura segundos; la constancia en la tarjeta no.**. Sin revocación, los botones quedan vivos semanas en el scrollback y
      generan taps fantasma.
- AC: **first-tap-wins**. La revocación hace inalcanzable el `ON CONFLICT DO UPDATE` de `ratings`;
      dejar ambos es tener una semántica muerta en el esquema. Si se quisiera permitir cambiar de
      opinión, hay que conservar el teclado — decisión explícita, no las dos cosas a la vez.
- AC: tap tardío aceptado solo dentro de **14 días** de la entrega. Camino rechazado =
      `answerCallbackQuery("expiró")` + revocación. **Nunca** responder "guardado" a un tap que se
      descarta: el bot mentiría y rompería la confianza desde adentro.
- AC: 👍 repetido cuando ya existe `label=1` → `answerCallbackQuery("guardado")` y **ningún** side
      effect. Sin esto, cada re-tap re-manda el link y re-dispara un fetch de detalle.

## D3 — Envío multi-destino y throttling

- AC: `Notify(ctx, chatID, listing, opts)` — el `chat_id` pasa a ser parámetro. Hoy está quemado
      desde env en `sendPhoto`/`sendMessage`.
- AC: `dryRun()` = **solo** `token == ""`. Hoy es `token == "" || chatID == ""`: con `CHAT_ID`
      ahora opcional, un deploy sin él corre en dry-run **para siempre**, sin que nadie reciba nada
      y con la única evidencia en stdout que nadie lee.
- AC: throttle **por chat** (token bucket 1.1s) + limitador global. El mutex único actual
      serializa todo el proceso: N usuarios × 30 tarjetas × 2.5s = 12.5 minutos para 10 usuarios,
      con los taps interactivos en cola detrás del bulk. El límite real de Telegram es ~1 msg/s
      **por chat**, así que por-chat permite paralelizar usuarios.
- AC: `answerCallbackQuery` y replies cortos **exentos** del throttle bulk.
- AC: `retry_after` **realimentado** al bucket del chat (`next_free = now + retry_after`). Hoy se
      duerme una vez y se reenvía; el bucket no se entera y la siguiente tarjeta al mismo chat
      vuelve a 429 — loop de throttle que quema el budget global.
- AC: caption medido en **unidades UTF-16**, no bytes ni runas (emoji astrales cuentan 2).
      Reserva la URL y recorta título/ubicación: `caption()` agrega la URL **al final**, así que un
      recorte naive borra primero lo único imprescindible. Presupuesto: rank + razones + features +
      link, con degradación en ese orden.
- AC: en 400 por caption largo, **fallback a `sendMessage`** con texto recortado, de modo que la
      fila de `deliveries` se escriba igual. Sin fallback, la tarjeta queda sin delivery row, sigue
      siendo candidata, vuelve a fallar al día siguiente y ocupa un puesto en cada "#k de N hoy"
      para siempre: tarjeta envenenada.
- AC: `url.Values{…}.Encode()` reemplaza `urlQueryEscape`. El actual escapa **5 caracteres**
      (`% & = + \n`) y deja pasar espacios, `#`, `"`, `'`, `;` y no-ASCII — `m²`, `á`, `ñ` están en
      cada caption real. Un `#` en un título trunca el valor en el límite de fragmento.
- AC: el retry de 429 **decodifica y verifica `resp.OK`**. Hoy devuelve solo el error de transporte
      del reenvío: un segundo 429/400/403 retorna `nil`, el caller lo toma como entregado, marca la
      entrega y esa publicación **no se manda nunca**.
- AC: fallo de envío de una tarjeta se registra contra su `deliveries` (`status=failed`,
      `attempts++`) y el loop **continúa** con la siguiente. Hoy `Notify` con error hace `return err`
      y aborta el resto del batch de esa URL: la cola del digest no llega y nadie se entera.

## D4 — Estados terminales de chat

- AC: 403 `Forbidden: bot was blocked` / chat desactivado → `users.state='dead'`, entrega omitida,
      datos conservados, alerta única al operador. Sin esto, cada mañana se re-fetchan 15 páginas
      de Zonaprop para un usuario que ya no existe y se reintentan N envíos que fallan, para siempre.

## D5 — Menú lateral de comandos

- AC: `setMyCommands` publica el catálogo (`chat.BotCommands`) para que Telegram lo muestre al
      escribir `/` y en el **botón de menú junto al campo de texto**. Las descripciones en
      castellano salen de un único catálogo, y `helpText()` sigue siendo la vista de ayuda.
- AC: cada nombre cumple `^[a-z0-9_]{1,32}$` y cada descripción 1-256 caracteres. Una entrada
      inválida o duplicada hace que el Bot API rechace **toda** la lista, así que el catálogo se
      valida en test (y el test deriva los comandos de `helpText()` para que menú y ayuda no
      diverjan en ninguna dirección).
- AC: `setChatMenuButton` fija el botón en `{"type":"commands"}` para que abra la lista aunque
      antes apuntara a una Mini App. Es el comportamiento por defecto, pero explícito.
- AC: la publicación es **best-effort** al arranque: un rechazo del Bot API se loguea WARN y el bot
      sigue entregando. Sin token (dry-run) no se llama a la red.
- AC: el menú es **global**, sin scopes por usuario ni por estado. Fue una decisión explícita: las
      opciones no cambian según el estado, así que un scope por chat solo agregaría llamadas y
      superficie de fallo.

## Archivos

`internal/telegram/*.go` (reescribir), `internal/chat/*.go` (nuevo: router de updates),
`internal/bot/*` (adaptación), tests httptest.

## Tests

Cada uno de los bugs pre-existentes arreglados acá (429 que traga errores, `urlQueryEscape`,
aborto de batch) lleva su test de regresión: son defectos que el suite actual no ve.

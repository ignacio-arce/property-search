# `/buscar`: corrida inmediata a pedido del usuario

One-pager de idea. Refinado con `idea-refine`.

## Problem Statement

How might we dejar que un usuario dispare su búsqueda de propiedades **en el momento**, en vez de
esperar hasta las 09:00, sin romper el presupuesto de fetch ni duplicar envíos?

## Recommended Direction

Nuevo comando **`/buscar`** en el chat. Al recibirlo, el `Poller` valida en orden: que el usuario
exista, que no esté en `/stop`, que el operador no lo haya desactivado (`active=false`) y que tenga
al menos una búsqueda válida; si todo pasa, responde `🔎 Buscando ahora…` y corre
`digest.RunForUser` **detachado** (contexto propio con timeout), para no bloquear el poll loop de
todos los demás. Al terminar manda el resultado: `Listo, te mandé N nuevas` o `No hay publicaciones
nuevas por ahora`.

La **corrida diaria de las 09:00 sigue funcionando igual**: `/buscar` es aditivo y no la reemplaza
ni la reprograma. El usuario puede correr a mano todas las veces que quiera, con un tope de **1 por
hora**. Ese tope se lleva en memoria del `Poller` y **no sobrevive al reinicio** (decisión explícita:
el bot reinicia poco y persistirlo no vale la tabla).

Dentro de una corrida, **las búsquedas válidas se indexan en paralelo**, no una atrás de otra. Nota
honesta: con el `Gate` global a 1 req/min (y FlareSolverr con un browser por vez) esto **no reduce el
tiempo total**; el carril que acelera de verdad es la prioridad de fetch, que acá no se usa. Se hace
igual porque es la semántica pedida y porque deja que el gate ordene la cola en vez de esperar el
round-trip completo de cada búsqueda antes de pedir la siguiente.

La pieza que no es obvia: `RunForUser` **no es idempotente bajo concurrencia**. `Candidates` lee lo
que no tiene fila en `deliveries` y `MarkDelivered` se escribe después de enviar, así que dos
corridas solapadas del mismo usuario mandan la tarjeta dos veces. Por eso el `Runner` serializa por
usuario con un lock interno, del que también participa la corrida diaria. El límite de 1/hora se
aplica por usuario en el `Poller`.

El comando se inyecta al `Poller` como una interfaz (`SearchRunner`) igual que hoy se inyecta
`Contacts`, así `chat` no depende de `digest`. El resultado se envía con el mismo `Notifier`/`SendText`
que ya usa la validación.

## Key Assumptions to Validate

- [ ] `RunForUser` es idempotente sin concurrencia pero no concurrente. *Test: dos `RunForUser`
      simultáneos del mismo usuario no duplican envíos.*
- [ ] Un `/buscar` detachado no bloquea el poll loop ni se corta al terminar el update. *Test/manual:
      el comando responde al instante con el fetch en curso.*
- [ ] El tope de 1h alcanza para proteger el `Gate`, que es recurso compartido. *Test: segundo
      `/buscar` dentro de la hora no dispara y avisa cuándo.*
- [ ] Indexar búsquedas en paralelo no rompe el resumen por usuario ni el baseline. *Test: corrida
      con 2+ búsquedas mantiene `searches`, `fetched`, `sent` y baselinea una vez por búsqueda.*
- [ ] El primer `/buscar` tras validar puede mandar 0 (baseline). El texto `No hay nuevas` no debe
      leerse como fallo. *Revisar copy con un caso real.*

## MVP Scope

**In:** comando `/buscar` con los cuatro gates (existe / no pausado / `active` / con búsquedas
válidas); tope de 1/hora **en memoria** por usuario; ack inmediato + mensaje de resultado; corrida
detachada con timeout; **indexado de búsquedas en paralelo**; lock por usuario en `digest.Runner`
compartido con el scheduler; `SearchRunner` inyectado en `Poller` y cableado en `main`; `/help`
actualizado; logs INFO/WARN acordes.

**Out:** auditoría en base de la corrida manual; cooldown persistente; prioridad de fetch (sigue
esperando el pacing); selección por búsqueda (`/buscar <label>`); activar/apagar usuarios (eso es el
switch del operador); reanudar automáticamente si está pausado.

## Not Doing (and Why)

- **Cambiar `users.active`** — `/buscar` es una corrida puntual, no un switch; el operador no queda
  puenteado.
- **Prioridad de fetch** — el carril prioritario es para el operador y el 👍; un usuario no debe
  saltar el pacing.
- **Reanudar si está en `/stop`** — mezcla dos intenciones; mejor avisar y dejar que mande `/start`.
- **Bypass del baseline** — reenviar las ~30 actuales en el primer `/buscar` es justo el ruido que el
  bot evita.
- **Cooldown persistente / tabla de corridas** — decisión explícita: no sobrevive al reinicio.
- **`/buscar <label>`** — MVP corre todas; elegir una búsqueda es una feature aparte.

## Open Questions

- **¿Paralelizar debería además acelerar?** Si el objetivo termina siendo "que llegue rápido", la
  palanca real es darle prioridad de fetch al `/buscar`; hoy se descartó para no puentear el pacing.
- **¿El tope de 1h debería aplicar por chat o por usuario?** En privados coinciden; se aplica por
  `user_id`, que es la unidad del modelo.
- **¿Qué mensaje cuando hay 5 búsquedas y alguna falla?** El resumen del digest ya lo deja en logs;
  al usuario hoy se le diría solo el total de nuevas.

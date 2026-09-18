# TODO — `/buscar`: corrida inmediata a pedido del usuario

Lista ejecutable de `tasks/plan-buscar.md`. Spec aprobado en `tasks/specs/buscar.md`; idea en
`docs/ideas/buscar-ahora.md`.

> Independiente del plan v2 (`tasks/plan.md`/`tasks/todo.md`) y del plan de logs
> (`tasks/plan-logs.md`/`tasks/todo-logs.md`), que quedan intactos.

**Comandos del repo:** `nix develop -c go build ./...` · `nix develop -c go test ./...` ·
`nix develop -c go vet ./...`.

---

## Phase 1: Digest seguro y en paralelo

### Task 1: lock por usuario en `digest.Runner`
**Descripción:** `RunForUser` no es idempotente bajo concurrencia: `Candidates` lee lo no entregado y
`MarkDelivered` escribe después de enviar, así que dos corridas solapadas del mismo usuario mandan la
tarjeta dos veces. Agregar un lock por usuario dentro del `Runner` (patrón `sync.Map` de
`chat.lockUser`) que envuelva el cuerpo de `RunForUser`, de modo que scheduler y `/buscar` se
serialicen entre sí.

**Acceptance criteria:**
- [ ] `RunForUser` toma un lock por `userID` y lo libera siempre (incluso en error)
- [ ] Dos `RunForUser` concurrentes del mismo usuario no duplican entregas
- [ ] Usuarios distintos no se bloquean entre sí
- [ ] Los tests existentes de digest siguen verdes

**Verificación:**
- [ ] `nix develop -c go test ./internal/digest/`
- [ ] `nix develop -c go build ./...`

**Dependencias:** Ninguna

**Archivos probables:**
- `internal/digest/digest.go`
- `internal/digest/digest_test.go`

**Alcance:** S/M (2 archivos)

---

### Task 2: indexado de búsquedas en paralelo
**Descripción:** Dentro de `RunForUser`, indexar todas las búsquedas válidas en paralelo en vez de
secuencialmente, con contadores atómicos para `fetched` y el resumen. Mantener el baseline y el
aislamiento de fallos por búsqueda. Documentar que el `Gate` sigue pacingando, así que no baja el
tiempo total.

**Acceptance criteria:**
- [ ] Una corrida con 2+ búsquedas las indexa concurrentemente (ninguna espera el round-trip de la otra)
- [ ] El resumen `digest: user done` conserva `searches`, `fetched`, `candidates`, `sent`, `ms`
- [ ] Un fallo de una búsqueda no aborta las demás
- [ ] El baseline por búsqueda sigue ocurriendo una sola vez por búsqueda
- [ ] Test con 2 búsquedas verifica que ambas quedan indexadas

**Verificación:**
- [ ] `nix develop -c go test ./internal/digest/`
- [ ] `nix develop -c go build ./...`

**Dependencias:** Task 1

**Archivos probables:**
- `internal/digest/digest.go`
- `internal/digest/digest_test.go`

**Alcance:** M (2 archivos)

---

## Checkpoint: Digest
- [ ] `nix develop -c go test ./internal/digest/` verde
- [ ] Sin doble envío en el test concurrente

---

## Phase 2: Comando

### Task 3: comando `/buscar` en `chat`
**Descripción:** Nuevo `internal/chat/search.go` con el handler. `Poller` gana un campo
`Search SearchRunner` (interfaz con `RunForUser(ctx, userID, chatID) (int, error)`) y un `sync.Map`
para el tope. El handler valida en orden: usuario existe → no `stopped` → `active` → tiene búsquedas
válidas → tope de 1/hora. Si todo pasa, responde `🔎 Buscando ahora…` y corre detachado con timeout;
al terminar manda resultado. Actualizar `/help`.

**Acceptance criteria:**
- [ ] `/buscar` sin usuario responde guiando a `/start`
- [ ] `state='stopped'` responde que está pausado y **no** corre
- [ ] `active=false` responde que está deshabilitado y **no** corre
- [ ] Sin búsquedas válidas responde que todavía no hay y **no** corre
- [ ] Segundo `/buscar` dentro de la hora no corre y avisa cuándo reintentar
- [ ] Happy path: responde el ack, corre detachado y manda `Listo, te mandé N nuevas` (o `No hay publicaciones nuevas`)
- [ ] El handler retorna sin esperar a que la corrida termine (no bloquea el poll loop)
- [ ] `/help` incluye `/buscar`

**Verificación:**
- [ ] `nix develop -c go test ./internal/chat/`
- [ ] `nix develop -c go build ./...`
- [ ] Test con `SearchRunner` falso: gates, tope, ack y resultado

**Dependencias:** Ninguna (usa lead falsa en tests)

**Archivos probables:**
- `internal/chat/search.go` (nuevo)
- `internal/chat/search_test.go` (nuevo)
- `internal/chat/bot.go` (dispatch + `/help`)

**Alcance:** M (3 archivos)

---

### Task 4: cablear `Search` en `main`
**Descripción:** Pasar el `*digest.Runner` como `Search` del `Poller` en `startPoller`. `RunForUser`
ya cumple la firma.

**Acceptance criteria:**
- [ ] `main` construye el `Poller` con `Search: a.digest`
- [ ] `go vet` sin avisos

**Verificación:**
- [ ] `nix develop -c go build ./...`
- [ ] `nix develop -c go vet ./...`

**Dependencias:** Task 1, Task 2, Task 3

**Archivos probables:**
- `cmd/bot/main.go`

**Alcance:** S (1 archivo)

---

## Checkpoint: Comando
- [ ] Tests de `chat` y `digest` verdes
- [ ] `go build ./...` y `go vet ./...` limpios

---

## Phase 3: Cierre

### Task 5: documentar `/buscar` en README
**Descripción:** Sumar `/buscar` a la lista de comandos del README y aclarar que corre una vez ahora,
con tope de 1/hora, sin cambiar la corrida diaria.

**Acceptance criteria:**
- [ ] README menciona `/buscar` y su semántica (inmediato, 1/hora, no reemplaza la diaria)
- [ ] `go test ./...` no cambia

**Verificación:**
- [ ] Revisión humana
- [ ] `nix develop -c go test ./...`

**Dependencias:** Task 3

**Archivos probables:**
- `README.md`

**Alcance:** S (1 archivo)

---

## Checkpoint: Complete
- [ ] `nix develop -c go test ./...` verde
- [ ] `gofmt -l .` vacío y `go vet ./...` limpio
- [ ] Drill manual en base descartable: `/buscar` responde, corre y reporta (sin tocar producción)

# Implementation Plan: `/buscar` — corrida inmediata a pedido del usuario

Spec aprobado: `tasks/specs/buscar.md`. Idea y trade-offs en `docs/ideas/buscar-ahora.md`.

## Overview

Agregar el comando `/buscar` para que un usuario dispare su digest en el momento en vez de esperar
las 09:00. El comando valida cuatro gates (usuario existe / no pausado / `active` / con búsquedas
válidas), aplica un tope de 1/hora en memoria, responde `🔎 Buscando ahora…`, corre el digest
detachado y avisa el resultado. Para que sea seguro, se serializa la corrida por usuario y se indexan
las búsquedas en paralelo. La corrida diaria sigue funcionando igual. Idea y trade-offs en
`docs/ideas/buscar-ahora.md`.

## Architecture Decisions

- **`SearchRunner` como interfaz en `chat`.** `Poller` recibe un `SearchRunner` (igual que recibe
  `Contacts`), satisfecho por `*digest.Runner`. `chat` no importa `digest`.
- **Corrida detachada con timeout.** `context.WithoutCancel` + timeout para no bloquear el poll loop
  ni cortarse al terminar el update.
- **Lock por usuario en `digest.Runner`.** `Candidates` lee lo no entregado y `MarkDelivered` escribe
  después de enviar: dos corridas solapadas del mismo usuario duplican tarjetas. El lock lo comparten
  scheduler y `/buscar`.
- **Indexado en paralelo dentro de una corrida.** Semántica pedida. Con el `Gate` a 1 req/min no baja
  el tiempo total; se documenta.
- **Tope de 1/hora en memoria del `Poller`** (`sync.Map`, sin constructor nuevo). Decisión explícita:
  no sobrevive al reinicio.
- **La diaria no se toca.** `/buscar` es aditivo.

## Task List

### Phase 1: Digest seguro y en paralelo

- [ ] Task 1: lock por usuario en `digest.Runner`
- [ ] Task 2: indexado de búsquedas en paralelo

### Checkpoint: Digest

- [ ] `go test ./internal/digest/` verde, incluida la no-duplicación concurrente

### Phase 2: Comando

- [ ] Task 3: comando `/buscar` en `chat` (gates, tope, ack, corrida detachada, resultado, `/help`)
- [ ] Task 4: cablear `Search` en `main`

### Checkpoint: Comando

- [ ] Con un `SearchRunner` falso, `/buscar` responde y respeta gates y tope
- [ ] `go build ./...` y `go vet ./...` limpios

### Phase 3: Cierre

- [ ] Task 5: documentar `/buscar` en README

### Checkpoint: Complete

- [ ] `go test ./...` verde
- [ ] Drill manual: `/buscar` en dry-run con una URL sin tocar producción

## Dependency Graph

```
Task 1 (lock) ──> Task 2 (paralelo) ─┐
                                     ├─> Task 4 (wire main)
Task 3 (comando /buscar) ────────────┘
        └─> Task 5 (docs)
```

Tasks 1 y 2 tocan el mismo archivo (`digest.go`): secuenciales. Task 3 es independiente.

## Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Doble envío por corridas solapadas | Alto | Lock por usuario compartido; test concurrente |
| Indexado en paralelo rompe el resumen o el baseline | Medio | Contadores atómicos; test con 2+ búsquedas |
| Corrida detachada cuelga el proceso al apagar | Medio | Timeout explícito; no toca el `WaitGroup` del poller |
| Spam de `/buscar` degrada el `Gate` compartido | Medio | Tope de 1/hora en memoria |
| El primer `/buscar` manda 0 (baseline) y confunde | Bajo | Copy "No hay publicaciones nuevas por ahora" |

## Open Questions

- **¿Paralelizar debería acelerar?** Si el objetivo pasa a ser velocidad, la palanca real es prioridad
  de fetch; hoy descartada.
- **¿El tope por usuario o por chat?** Se aplica por `user_id`; en privados coinciden.
- **¿Mensaje cuando alguna búsqueda falla?** Hoy el usuario solo ve el total; el detalle queda en logs.

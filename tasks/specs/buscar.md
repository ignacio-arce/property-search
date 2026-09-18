# Spec: `/buscar` — corrida inmediata a pedido del usuario

Estado: borrador para aprobación. Idea en `docs/ideas/buscar-ahora.md`.
Plan en `tasks/plan-buscar.md`, tareas en `tasks/todo-buscar.md`.

## Assumptions (corregir antes de implementar)

1. El actor es **el propio usuario** en chat privado; no hay admin ni activación de terceros.
2. `/buscar` **no** cambia `users.active` ni `state`; es una corrida puntual, no un switch.
3. El tope de 1/hora se lleva **en memoria** y no sobrevive al reinicio (decisión explícita).
4. La corrida diaria de las 09:00 sigue igual; `/buscar` es aditivo.
5. Las búsquedas válidas del usuario se indexan **en paralelo** dentro de la corrida.
6. El resultado se mide en publicaciones enviadas (`sent` de `RunForUser`).
7. Un `/buscar` con 0 nuevas por baseline se reporta como "No hay publicaciones nuevas por ahora".

## Objective

Permitir que un usuario dispare su búsqueda de propiedades **en el momento** en vez de esperar las
09:00, sin romper el presupuesto de fetch ni duplicar envíos. Usuario: cualquiera que ya completó el
onboarding y tiene al menos una búsqueda validada. Éxito: el usuario manda `/buscar`, recibe un acuse
inmediato, y al terminar un mensaje con cuántas publicaciones nuevas recibió; los demás usuarios no
se ven bloqueados.

## Tech Stack

- Go 1.26 (`go.mod`), sin dependencias nuevas.
- Postgres vía `pgx/v5` (tests contra base real y descartable con `internal/dbtest`).
- Telegram vía `internal/telegram`; el `Poller` de `internal/chat` despacha comandos.
- `log/slog` para logs (trabajo previo de logging).

## Commands

```
Build:  nix develop -c go build ./...
Test:   nix develop -c go test ./...
Vet:    nix develop -c go vet ./...
Fmt:    nix develop -c gofmt -l .
Foco:   nix develop -c go test ./internal/chat/ -run TestBuscar
```

## Project Structure

```
internal/chat/        → Poller, comandos y onboarding (search.go nuevo vive acá)
internal/digest/      → Runner.RunForUser, lock por usuario, indexado paralelo
internal/repo/        → acceso a datos scoped por user_id
internal/logging/     → construcción del logger slog
tasks/specs/          → este spec
tools/...             → no aplica
```

## Code Style

Seguir el estilo existente: comentarios en español que expliquen el **por qué**, nombres en inglés,
comandos con `//` doc corto. Ejemplo del handler esperado:

```go
// search handles /buscar: a digest run triggered by the user instead of waiting
// for the daily schedule.
func (p *Poller) search(ctx context.Context, userID, chatID int64, chat string) error {
    if !p.canRunSearch(...) {
        return p.API.SendText(ctx, chat, ...)
    }
    ...
}
```

## Testing Strategy

- Go `testing` con tests de tabla; integración contra Postgres real vía `dbtest.NewPool` (se saltea sola
  si no hay base). Sin mocks pesados: se prefiere un **fake** de `SearchRunner` en `chat` y un
  `fakeFetcher` en `digest`.
- **Test-first**: cada comportamiento nuevo arranca con un test que falla.
- Niveles: unit/fake para los gates y el tope; integración real para la serialización de `digest`.
- Cobertura exigida: gates (existe / pausado / inactivo / sin búsquedas), tope 1/hora, ack + resultado,
  no-bloqueo del handler, y no-duplicación concurrente en `digest`.

## Boundaries

- **Always:** correr `go test` antes de commitear; un commit por incremento; comentar el porqué.
- **Ask first:** cambios de esquema/migraciones; agregar dependencias; cambiar rutas o firmas
  públicas existentes (`RunForUser` ya está commiteado con lock).
- **Never:** commitear secretos; borrar o saltear tests para poner verde; loguear el token de Telegram
  o la URL de búsqueda en INFO.

## Requirements

- **R1 — Comando.** `/buscar` en chat privado; afecta solo al usuario que lo envía.
- **R2 — Gates, en orden.** (a) si el usuario no existe, guiar a `/start`; (b) si `state='stopped'`,
  avisar que está pausado y **no** correr; (c) si `active=false`, avisar que falta habilitación y
  **no** correr; (d) si no hay búsquedas válidas, avisar; (e) si no hay `SearchRunner`, avisar.
- **R3 — Tope.** Máximo una corrida manual por `user_id` por hora, en memoria. Un segundo intento
  dentro de la ventana no corre e informa los minutos restantes. Atómico ante mensajes simultáneos.
- **R4 — No bloqueo.** El handler responde `🔎 Buscando ahora…` y lanza la corrida en un goroutine
  con `context.WithoutCancel` + timeout; retorna sin esperar. Un run largo no debe frenar el poll loop.
- **R5 — Resultado.** Al terminar: `0` → "No hay publicaciones nuevas por ahora."; `1` → "✅ Listo, te
  mandé 1 publicación nueva."; `N` → "✅ Listo, te mandé N publicaciones nuevas."; error → "⚠️ No pude
  completar la búsqueda. Probá de nuevo más tarde.".
- **R6 — Misma semántica de digest.** Corre `RunForUser`, así que respeta dedup, scoring, tope diario
  (`MaxPerRun`) y baseline. La diaria de las 09:00 no cambia.
- **R7 — Serialización.** `/buscar` y la corrida diaria del mismo usuario no se solapan (lock por
  usuario en `digest.Runner`, ya implementado).
- **R8 — Paralelismo.** Dentro de una corrida, las búsquedas válidas se indexan en paralelo (ya
  implementado).
- **R9 — Ayuda.** `/help` incluye `/buscar`.
- **R10 — Logs.** INFO al aceptar (`user`, `searches`) y al terminar (`sent`); WARN si la corrida
  falla; sin URL de búsqueda en INFO.

## Success Criteria

- [ ] Existe un test por cada gate (R2) que prueba que **no** se corre y que se responde.
- [ ] Test del tope (R3): el segundo `/buscar` dentro de la hora no llama al runner e informa minutos.
- [ ] Test de no bloqueo (R4): el handler retorna con el runner todavía bloqueado.
- [ ] Test de resultado (R5) para 0, 1, N y error.
- [ ] Test de serialización (R7) y de paralelismo (R8) en `digest` (ya verdes).
- [ ] `nix develop -c go test ./...` verde y `go vet ./...` limpio.
- [ ] `/help` menciona `/buscar`.

## Open Questions

- **¿El tope cuenta la corrida diaria?** No: la ventana es solo de corridas manuales; son intenciones
  distintas.
- **¿Mensaje cuando algunas búsquedas fallan?** Hoy el usuario ve solo el total; el detalle queda en
  logs. Si se quiere precisión, es una feature aparte.
- **¿Paralelizar debería acelerar?** Con el `Gate` a 1 req/min no baja el tiempo total; si el objetivo
  pasa a ser velocidad, la palanca es prioridad de fetch.

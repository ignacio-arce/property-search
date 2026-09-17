# Bot de calificación de propiedades

One-pager de idea. Refinado con `idea-refine` y estresado con dos ciclos de `doubt-driven-development`.
El plan de implementación vive en `tasks/plan.md`.

## Problem Statement

How might we convertir la búsqueda de propiedad en un hábito diario de un tap, donde un bot
aprende qué quiere *este* comprador y ordena lo que llega — sin decidir por él?

## Recommended Direction

Bot conversacional de Telegram en Go. Cualquier usuario hace `/start` **en un chat privado**,
entrega sus URLs de búsqueda de Zonaprop con un nombre corto, y el bot las valida. Cada día a
las 09:00 baja esas URLs (ordenadas por *más recientes*), y manda **todas** las publicaciones
que ese usuario todavía no recibió, ordenadas por un score aprendido, cada una con foto,
prestaciones, el link directo y botones 👍/👎 de un solo tap.

El modelo **predice, no filtra**: no hay umbral, no hay cuota de exploración, no hay porcentaje
de probabilidad. Cada tarjeta muestra su puesto del día ("#2 de 14 hoy") y, cuando hay datos
suficientes, hasta dos razones en lenguaje llano ("San Isidro: te gustaron 4 de 5"). La decisión
queda siempre del lado del usuario. Un 👍 dispara la extracción *best-effort* de **teléfono** desde
el detalle (A2 confirmó: hay teléfono en el JSON-LD, no hay email); si no aparece, el link ya estaba
en la tarjeta.

El estado vive en Postgres. Ratings y pesos del modelo son **estrictamente por usuario** y nunca
se comparten. Se despliega con docker compose (bot + FlareSolverr + Postgres) en una Raspberry
Pi 5; el toolchain de desarrollo es el flake de NixOS existente.

Esta dirección gana frente a las alternativas porque el loop de calificación resuelve el
cold-start sin obligar al usuario a declarar pesos que no sabe expresar, y porque el orden
reciente de Zonaprop hace que el volumen diario sea chico: la URL de búsqueda ya filtra lo duro
(zona, ambientes, m², cochera, antigüedad, banda de precio), así que el modelo solo tiene que
aprender gusto fino sobre el remanente.

## Key Assumptions to Validate

Medido en las sondas de la Fase A (2026-09-16, ver `tasks/research/probe-results.md`). Dos confirmadas, dos refutadas, una
aún abierta:

- [x] **El orden reciente funciona — CONFIRMADO.** `-orden-publicado-descendente.html` ordena por
      publicación: las edades del DOM real son no-decrecientes (2d, 3d, 4d, 5d, 6d).
- [x] **La paginación NO existe — REFUTADO.** `?n_pg=2` devuelve **los mismos 30 `data-id`** y
      `n_pg=2` aparece **0 veces** en el HTML: el listado lo monta el SPA. Cobertura real del bot =
      **las ~30 más nuevas de cada búsqueda**, no las 565 que declara. Se advierte en `/addurl`.
- [x] **FlareSolverr es obligatorio — la "huella TLS de Chrome" ya no alcanza.** El 403 de Zonaprop
      trae `cf-mitigated: challenge`; ni curl ni `tls-client` con `Chrome_152` vía proxy lo pasan.
      Solo FlareSolverr devolvió tarjetas (30, en 2.8s). El README del repo está obsoleto.
- [x] **El contacto es extraíble — PARCIAL: teléfono SÍ, email NO.** El detalle trae un JSON-LD
      `Apartment` con `"telephone"` en el HTML estático (`54 9 1168690900`, 1.9s, sin click ni JS).
      **No hay email del anunciante**, solo placeholders. Bonus: el mismo bloque trae `numberOfRooms`,
      `floorSize` en m², `streetAddress` y barrio estructurados. **Caveat:** no se pudo confirmar que
      el teléfono varíe entre publicaciones (el 2º detalle falló por challenge) — hay que medirlo
      sobre ≥2 antes de confiar en la feature.
- [ ] **Hay publicaciones nuevas suficientes.** Se puede estimar: 565 resultados totales, ~30 por
      página, y la página 1 muestra 2 a 6 días de antigüedad → sugiere **~5-15 nuevas por día** en
      esta búsqueda. Suficiente para el techo de 30, flaco para juntar ratings.
- [ ] **Calificar de un tap no genera fricción.** Mide la primera semana de uso real.
- [ ] **El proxy y FlareSolverr son alcanzables desde compose.** El proxy de dev responde y pasa
      tráfico; falta la verificación dentro de la red de compose (ver nota de riesgo abajo).

## MVP Scope

**In:** onboarding por `/start` en chat privado con validación de URLs (sintáctica + profunda
asincrónica); auto-inyección del orden reciente; hasta 5 URLs por usuario con nombre corto único;
ingesta tipada solo de tarjetas PROPERTY; dedup por `data-id` de Zonaprop; digest diario a las
09:00 sobre la página 1, baseline silencioso y tope diario con excedente para el día siguiente;
**activación manual (`users.active`)**: cualquiera puede `/start` pero el digest solo corre para
usuarios que el operador habilita a mano; score transparente por usuario con razones solo cuando el
bucket tiene n≥3; `/model`, `/list`, `/addurl`, `/rmurl`, `/stop`, `/borrardatos`; **contacto al 👍
solo teléfono** (vía JSON-LD del detalle), con caché y TTL; Postgres con migraciones y seeds
automáticos; compose de tres servicios con FlareSolverr **obligatorio**.

**Out:** multi-portal, umbral o filtrado automático, probabilidad porcentual, cuota de exploración,
modelos por dimensión, `title_token` en el scoring, `rooms`/`dorm` en el scoring (medidos constantes),
tarjetas de emprendimientos, paginación (no existe server-side), UI web, automatizar contacto o
visitas, historial de precios y comparables.

## Not Doing (and Why)

- **Filtrar por umbral** — el modelo decide qué ves y deja de aprender de los bordes. Anotar y
  ordenar conserva el control del lado del usuario y elimina el sesgo de selección de raíz.
- **Porcentaje de probabilidad** — no es derivable de like-rates por bucket, y con n=7 el
  intervalo de confianza va de 40% a 96%. Es falsa precisión justo donde hace falta confianza.
- **Gatear el link detrás del 👍** — si el link solo llega al decir "sí", la gente toca 👍 para
  obtener el link. Los positivos se inflan a ~100% y el modelo pierde poder discriminativo.
  El link va siempre en la tarjeta; el 👍 queda como opinión pura.
- **Modelos por dimensión** — con 10-40 ratings por mes, cada dimensión se muere de hambre.
- **ML opaco** — si no se puede interrogar, no se confía; y si no se confía, se deja de calificar.
- **Multi-portal** — parsers nuevos más resolución de identidad entre portales. Es la expansión
  natural *después* de que el score gane confianza, no antes.
- **Agente que contacta y agenda solo** — ToS de los portales más integración de calendario.
  North star, no milestone.
- **Deal-check / comparables** — necesita un corpus de precios que recién existe tras semanas.
- **Ignorado = negativo** — en v1 se entrena solo con labels explícitos y `/model` lo dice.
  Convertir la inacción en negativo inventa señal donde no la hay.
- **UI web** — el chat es la interfaz. Una UI es scope creep con deadline difuso.

## Open Questions

- **¿El teléfono del JSON-LD varía por publicación?** A2 lo encontró pero no pudo medir un segundo
  detalle (challenge). **Se espera que sí**; verificar antes de V6. Si fuera un número genérico, el
  👍 solo manda el link — el diseño ya lo soporta, porque el link va siempre en la tarjeta.
- **¿Cuántos ratings juntás por mes?** Se mide más adelante, con uso real. Define cuándo el ranking
  con razones reemplaza al modo "N nuevas hoy"; pueden ser semanas. No bloquea nada: la UI se calla
  sola con n<3 y n<30.
- **¿Alquileres y pesos? Resuelto: se admiten.** `operation_type` (`venta`/`alquiler`) y `currency`
  (`USD`/`ARS`) son datos de primera clase, pero **claves de partición del modelo, no features de
  scoring**: un alquiler de ARS 450.000/mes y una venta de USD 144.000 tienen magnitudes
  incomparables. Cada feature numérica se namespacia (`ppm2:venta:USD:…`, `expensas:alquiler:ARS:…`),
  así se aprende *qué* alquileres te gustan en vez de "te gustan los alquileres". Sin tipo de cambio.
- **¿Cómo te enterás de un usuario esperando activación?** **Diferido** mientras el caudal sea bajo:
  se resuelve con una query manual documentada. Cuando crezca, alerta al operador al completar el
  onboarding.
- **¿Qué pasa si el teléfono no varía o el detalle deja de exponerlo?** V6 se degrada a "manda el
  link". La feature es aditiva y su caída no rompe nada.

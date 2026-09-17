# zonaprop-bot

Bot en Go que monitorea búsquedas de **compra** en Zonaprop (zona norte de Buenos Aires) y
te notifica por **Telegram** cada publicación nueva con tarjeta completa: precio, m², ambientes,
ubicación, foto y link directo. Metodología equivalente al bot Python de
[nazarenads/zonaprop-bot](https://github.com/nazarenads/zonaprop-bot) (request + parseo de
selectors + dedup por `sha1(link)` + loop), pero en Go, con estado persistente y pensado para
correr como imagen Docker ARM64 en una Raspberry Pi 5.

## Comportamiento

- Cada `CHECK_INTERVAL` (default `60m`) se revisan las URLs de búsqueda configuradas.
- La **primera corrida notifica todo el inventario** actual (historial vacío).
- En corridas siguientes sólo se notifican publicaciones nuevas (dedup por `sha1` de la URL).
- El historial vive en `DATA_DIR/seen.jsonl` (append-only, fsync por alta; tolera un corte).
- Si una URL falla (403 de Cloudflare, timeout, 5xx), se reintenta con backoff y se sigue con las
  demás: un fallo no detiene el ciclo.
- Sin credenciales de Telegram, corre en **dry-run** imprimiendo las alertas por consola.

## Servicios de red opcionales

`FLARESOLVERR_URL` y `ZONAPROP_PROXY` son **opcionales**: sólo se usan si su variable está definida.

| Config | Efecto |
|--------|--------|
| `FLARESOLVERR_URL` definida | Resuelve el challenge de Cloudflare con FlareSolverr (navegador real). |
| Sin FS, con `ZONAPROP_PROXY` | Cliente Go con huella TLS de Chrome (equivalente a `cloudscraper`) saliendo por el proxy rotativo; cada reintento abre conexión nueva (nueva IP). |
| Sin ninguna | Conexión directa con huella TLS de Chrome. |

En la práctica Zonaprop acepta la huella TLS de Chrome; el proxy/FlareSolverr ayudan cuando
Cloudflare marca el IP por exceso de pedidos.

## Configuración (env vars)

Ver `.env.example`. Obligatorias: `SEARCH_URLS` y (para noquear en dry-run) `TELEGRAM_BOT_TOKEN` +
`TELEGRAM_CHAT_ID`.

## Despliegue en la Raspberry Pi (Alpine + Docker)

```bash
cp .env.example .env        # completar TELEGRAM_* y SEARCH_URLS
docker compose up -d --build
docker compose logs -f      # verificar la primera corrida
```

La imagen es multi-stage (`scratch` + CA certs), sin CGO, ~15 MB, `linux/arm64`. Compila nativo
en el propio Pi5.

### Cómo armar la URL de búsqueda

En Zonaprop: buscá lo que quieras (p.ej. `Departamentos > Compra > GBA Norte > San Isidro`),
copiá la URL de la barra del navegador y usala en `SEARCH_URLS`. Se admiten varias URLs
(una por línea o separadas por coma).

## Desarrollo

```bash
nix develop            # toolchain Go reproducible (esta máquina no tiene Go global)
make fmt vet test      # formato, vet y tests
make run               # dry-run local (sin credenciales)
make probe             # fetch de la primera URL + parseo + resumen
```

### Pruebas

`go test ./...` cubre: parseo (contra `fixtures/search_gba_norte.html`, una pagina real de Zonaprop), dedup
del historial, reintentos con backoff, modos de fetch (FlareSolverr / TLS / proxy), payload y
rate-limit de Telegram, y el loop de orquestación con fakes.
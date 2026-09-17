# Área: Persistencia (Postgres + repositorio por usuario)
> **Este documento es racional de diseño, no la lista de tareas.** La lista ejecutable,
> con acceptance criteria, verificación y alcance por tarea, está en `tasks/todo.md`.


**Objetivo:** persistencia relacional con migraciones y seeds automáticos, y un repositorio donde
el aislamiento multi-usuario sea **estructural**, no una convención.

**Alimenta:** V1 (esquema mínimo), V2 (ratings y deliveries), V4 (digests), V5 (search_urls).

## C1 — Compose antes que el código

La dev machine no tenía Docker; ahora sí (29.7.2, x86_64). El deploy es arm64.

- AC: servicio `postgres:17-alpine` con volumen nombrado `pgdata`, `restart: unless-stopped`.
- AC: healthcheck `pg_isready -h 127.0.0.1 -U $${POSTGRES_USER} -d $${POSTGRES_DB}`. **Con `-h`**:
      sin él el entrypoint oficial levanta un servidor temporal de init con socket unix solamente,
      `pg_isready` da 0 durante el initdb, el servicio pasa a `healthy`, `depends_on` libera el bot
      y el connect TCP a `postgres:5432` falla. Es exactamente el primer arranque con volumen vacío.
- AC: `${POSTGRES_USER:?required}` / `${POSTGRES_PASSWORD:?required}` / `${POSTGRES_DB:?required}`
      — sin `:?`, compose sustituye vacío y Postgres se niega a inicializar sin error visible.
- AC: servicio `flaresolverr` **pinneado en `v3.3.20`** (la validada en las sondas; **confirmar
      arm64 al desplegar**, que está fuera de este plan), no `:latest`. Hay regresiones
      arm64/RPi en `latest` (Chromium que no arranca, CPU al 100% en idle); un `compose pull` de
      rutina puede romper la cadena de fetch sin que nadie toque nada.
- AC: imagen de test **pinneada a la misma tag** que compose, en una constante compartida. Probar
      migraciones contra una major distinta de la que corre el Pi no prueba nada.

## C2 — `internal/db`

- AC: pool pgx; `DSN` construido **en Go** con `url.UserPassword`, no interpolado en compose.
      Un password con `@`, `:`, `/`, `#` o `%` rompe la interpolación y el fallo se manifiesta como
      error de auth, apuntando a la causa equivocada.
- AC: arranque con **retry acotado** alrededor de pool-ping + migrar (por el race del healthcheck).
- AC: migrador versionado propio: `.sql` embebidos con `go:embed`, tabla `schema_migrations`,
      aplicación en orden, idempotente. Se evita `golang-migrate` como dependencia.
- AC: seeds idempotentes: fila de settings globales con defaults; datos del operador si
      `SEED_CHAT_ID` y `SEED_URLS` están presentes. **Los seeds pasan por el mismo validador de
      URLs que el onboarding** — un typo del operador no puede convertirse en un zombie inmortal.
- AC: **`SEED_URLS` = entradas separadas por coma, cada una `label|url`, en una sola línea.** En
      `env_file` los multi-línea sin comillas se parsean como entradas inválidas separadas.
- AC: tests con `testcontainers-go`, con **`t.Skip` cuando el daemon no es alcanzable** — si no,
      `go test ./...` queda acoplado al host y rompe en cualquier runner sin Docker.
- AC: migraciones y seeds corren **dos veces seguidas sin error**.

## C3 — `internal/user` y esquema

- AC: capa repositorio donde **todo método exportado recibe `user_id` como primer argumento** y no
      se puede escribir una query sin él. El aislamiento es la propiedad más fácil de romper en
      silencio y no hay type system que la defienda.
- AC: test de aislamiento con dos usuarios sembrados: A nunca recibe ni entrena con listings,
      ratings ni pesos de B. Es el primer test de integración con DB del proyecto.

```sql
users(user_id BIGINT PRIMARY KEY,          -- Telegram from.id, NO chat_id
      chat_id BIGINT NOT NULL,             -- destino de entrega
      state TEXT NOT NULL DEFAULT 'idle',  -- idle|await_url|await_label|validating|ready|stopped|dead
      state_data JSONB,
      onboarded_at TIMESTAMPTZ,
      settings JSONB NOT NULL DEFAULT '{}',
      active BOOLEAN NOT NULL DEFAULT false,  -- interruptor de notificación diaria.
                                             -- /start y el onboarding funcionan igual (carga y
                                             -- valida URLs), pero NADIE recibe el digest hasta que
                                             -- el operador lo active a mano:
                                             --   UPDATE users SET active = true WHERE chat_id = <id>;
      active_model_version INT NOT NULL DEFAULT 0,
      created_at TIMESTAMPTZ NOT NULL DEFAULT now())

search_urls(id BIGSERIAL PRIMARY KEY,
      user_id BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
      label TEXT NOT NULL,
      url TEXT NOT NULL,
      url_norm TEXT NOT NULL,              -- host lowercase, query ordenado, tracking fuera
      sort_injected BOOLEAN NOT NULL DEFAULT false,
      validation_status TEXT NOT NULL DEFAULT 'pending',  -- pending|valid|valid_empty|failed|invalid
      attempts INT NOT NULL DEFAULT 0,
      next_check_at TIMESTAMPTZ,
      last_fetch_status INT,
      last_card_count INT,
      last_checked_at TIMESTAMPTZ,
      created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
      UNIQUE(user_id, url_norm), UNIQUE(user_id, label))

listings(id BIGSERIAL PRIMARY KEY,
      zonaprop_id TEXT UNIQUE NOT NULL,     -- data-id de la tarjeta, NO sha1 de la URL
      canonical_url TEXT NOT NULL,          -- origin+path sin query
      operation_type TEXT,                  -- 'venta' | 'alquiler' (+ '' desconocido)
      currency TEXT,                        -- 'USD' | 'ARS' (+ '' desconocido)
      features JSONB NOT NULL,              -- último conocido, solo para mostrar
      recency_rank INT,                     -- posición ordinal en el fetch ordenado por recencia
                                            -- al verse POR PRIMERA VEZ (1 = la más nueva)
      first_indexed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
      last_indexed_at TIMESTAMPTZ NOT NULL DEFAULT now())
      -- operation_type y currency son Claves de PARTICIÓN del modelo (ver modelo-digest.md),
      -- no features de scoring. Van como columnas para poder indexar y depurar; el bucket
      -- numérico se namespacia con ellas: 'ppm2:venta:USD:1500-1750'.
      -- first_indexed_at es GLOBAL: jamás se usa para decidir novedad.
      -- Novedad = deliveries por usuario.
      -- recency_rank existe porque ni zonaprop_id ni id sirven como proxy de recencia:
      -- A1 midió que los data-id no son monotónicos, y el orden de ingesta es
      -- "más nuevo primero", así que el id más BAJO es el más NUEVO del lote.

listing_sources(listing_id BIGINT REFERENCES listings ON DELETE CASCADE,
      search_url_id BIGINT REFERENCES search_urls ON DELETE CASCADE,
      first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
      PRIMARY KEY(listing_id, search_url_id))
      -- Sin esto, "candidatas = NOT EXISTS(deliveries)" sobre listings global
      -- le filtra a B todo el inventario de A.

deliveries(user_id BIGINT REFERENCES users ON DELETE CASCADE,
      listing_id BIGINT REFERENCES listings ON DELETE CASCADE,
      message_id BIGINT,
      position INT,
      score REAL,
      model_version INT NOT NULL DEFAULT 0,
      features JSONB NOT NULL,             -- SNAPSHOT al enviar: se entrena con lo que el
                                           -- usuario vio, no con lo que mutó después
      status TEXT NOT NULL DEFAULT 'pending',  -- pending|sent|failed|dead
      attempts INT NOT NULL DEFAULT 0,
      sent_at TIMESTAMPTZ,
      PRIMARY KEY(user_id, listing_id))

ratings(id BIGSERIAL PRIMARY KEY,
      user_id BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
      listing_id BIGINT NOT NULL REFERENCES listings ON DELETE CASCADE,
      label SMALLINT NOT NULL,             -- 1 = 👍, 0 = 👎
      created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
      UNIQUE(user_id, listing_id))

model_weights(user_id BIGINT REFERENCES users ON DELETE CASCADE,
      feature TEXT NOT NULL,
      value TEXT NOT NULL,                 -- el bucket: 'San Isidro', 'ppm2:1500-1750', 'unknown'
      ups INT NOT NULL DEFAULT 0,
      downs INT NOT NULL DEFAULT 0,
      model_version INT NOT NULL,
      updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
      PRIMARY KEY(user_id, feature, value, model_version))
      -- (user_id, feature) a secas NO alcanza: una like-rate es por (feature, value).
      -- Con ese PK todos los buckets colapsan al prior global y el orden es una moneda al aire.

listing_contacts(listing_id BIGINT PRIMARY KEY REFERENCES listings ON DELETE CASCADE,
      phone TEXT, email TEXT, fetched_at TIMESTAMPTZ NOT NULL DEFAULT now())
      -- TTL 30 días: contacto más viejo se re-fetcha por el carril prioritario.

digests(user_id BIGINT REFERENCES users ON DELETE CASCADE,
      run_date DATE NOT NULL,
      status TEXT NOT NULL DEFAULT 'pending',  -- pending|running|done|error
      started_at TIMESTAMPTZ, finished_at TIMESTAMPTZ, error TEXT,
      PRIMARY KEY(user_id, run_date))

bot_state(id INT PRIMARY KEY CHECK (id = 1),
      last_update_id BIGINT NOT NULL DEFAULT 0,
      poll_lease_holder TEXT, poll_lease_expires_at TIMESTAMPTZ)
```

- AC: `ON CONFLICT` especificado para cada escritor de `deliveries`: baseline `DO NOTHING`;
      send-path `DO UPDATE … WHERE message_id IS NULL`. Sin esto, `/rmurl` + `/addurl` de la misma
      URL revienta contra el PK, y una URL que pasa a `valid` a las 08:59 con el digest ya
      seleccionado produce una tarjeta enviada sin `message_id` (rompe revocación de teclado y
      ventana de tap tardío).
- AC: tope de 5 URLs por usuario y unicidad de label enforceados **dentro de la transacción de
      insert**, no con check-then-insert en la aplicación: el poll loop y el scheduler corren
      concurrentes.

## Archivos

`docker-compose.yml`, `internal/db/*.go`, `internal/user/*.go`, `migrations/*.sql`, `go.mod`.

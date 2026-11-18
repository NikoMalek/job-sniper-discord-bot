# Job Sniper Discord Bot

Bot multi-servidor para detectar vacantes recientes de LinkedIn, ChileTrabajos,
Computrabajo y Get on Board y publicarlas en un canal de Discord.

## Requisitos

- Python 3.12 o superior
- Una aplicación/bot de Discord con el scope `bot` y `applications.commands`
- Permisos `View Channel`, `Send Messages`, `Embed Links` y `Use Slash Commands`

## Desarrollo local

```powershell
py -3.12 -m venv .venv
.venv\Scripts\Activate.ps1
pip install -r requirements.txt
Copy-Item .env.example .env
python -m src.main
```

Completa `DISCORD_BOT_TOKEN` en `.env`. `DB_PATH` usa `data/jobs.db` por
defecto y `CHECK_INTERVAL_MINUTES` usa 5 minutos. `LOG_LEVEL` controla el
detalle de los logs (`DEBUG`, `INFO`, `WARNING`, `ERROR`); por defecto `INFO`.
Las variables opcionales del proxy (`JOB_PROXY_URL`, `JOB_PROXY_TOKEN` y
`JOB_PROXY_MODE`) se explican en la sección [Proxy opcional para
portales](#proxy-opcional-para-portales). Para pruebas locales, usa
`JOB_PROXY_MODE=off` y deja vacías la URL y el token del proxy.

## Proxy opcional para portales

El proyecto incluye un Worker en
[`worker/job-sniper-proxy.js`](worker/job-sniper-proxy.js), diseñado para
desplegarse en Cloudflare Workers y actuar como proxy de salida hacia los
portales de empleo. Esto permite que una plataforma de despliegue, como
Railway, pueda consultar sitios a los que no llega directamente o cuya IP
bloquea las peticiones.

El comportamiento actual es:

- **ChileTrabajos**: usa el Worker siempre cuando el proxy está configurado,
  porque la plataforma donde se ejecuta el bot puede sufrir timeouts al
  conectarse directamente.
- **LinkedIn**: intenta conectarse directamente y usa el Worker como fallback
  si recibe un bloqueo o un error temporal, como `429`.
- **Get on Board**: se consulta directo, sin pasar por el Worker.
- **Computrabajo**: igual que LinkedIn, intenta directo y usa el Worker como
  fallback.

El Worker solo reenvía peticiones `GET` hacia los hosts permitidos y requiere
un token compartido con el bot. `ALLOWED_HOSTS` acepta hosts exactos y
comodines de subdominio como `*.computrabajo.com`, que cubre todos los países
del portal.

### Modos

| `JOB_PROXY_MODE` | Comportamiento |
|---|---|
| vacío (automático) | Cada portal usa su modo por defecto |
| `off` | Desactiva el proxy para todos los portales |
| `fallback` | Intenta directo y, si falla, reintenta por el Worker |
| `always` | Todo el tráfico del portal pasa por el Worker |

| Portal | Modo por defecto |
|---|---|
| ChileTrabajos | `always` |
| LinkedIn | `fallback` |
| Computrabajo | `fallback` |
| Get on Board | `off` (conexión directa) |

Solo se proxean los hosts permitidos por cada scraper (`PROXY_HOSTS`) y el
Worker valida además su lista `ALLOWED_HOSTS`. Si el Worker no está
configurado (falta URL o token), todo funciona directo como antes.

### Configurar el bot

```powershell
JOB_PROXY_URL=https://<nombre>.<subdominio>.workers.dev
JOB_PROXY_TOKEN=<el mismo valor del Secret>
JOB_PROXY_MODE=
```

En un entorno desplegado, por ejemplo Railway, `JOB_PROXY_MODE` vacío deja el
modo automático por portal. También se puede usar `fallback` o `always` para
forzar un comportamiento común.

En **local**, donde normalmente la conexión directa sí funciona, por ende se
debe configurar así:

```powershell
JOB_PROXY_URL=
JOB_PROXY_TOKEN=
JOB_PROXY_MODE=off
```

Así las pruebas locales nunca pasan por el Worker.

## Flujo de contribución

Los cambios no deben hacerse directamente sobre `main`. Para cada cambio:

1. Actualiza tu copia local:

   ```powershell
   git checkout main
   git pull origin main
   ```

2. Crea una rama descriptiva:

   ```powershell
   git checkout -b feature/nombre-del-cambio
   ```

3. Implementa y valida el cambio.
4. Guarda los cambios y publica la rama:

   ```powershell
   git add .
   git commit -m "Describe el cambio"
   git push -u origin feature/nombre-del-cambio
   ```

5. Abre un Pull Request hacia `main`, espera la revisión y fusiónalo solo
   después de que pasen las validaciones.

## Uso

Al unirse a un servidor, y una sola vez en los servidores donde ya estaba, el
bot envía una guía con los comandos principales. Se marca como enviada para no
repetirla.

Los comandos de configuración requieren `Manage Server`. Para dar acceso a un
rol o usuario concreto, usa Ajustes del servidor → Integraciones → Job Sniper.

Comandos:

- `/help`: muestra los pasos iniciales, la configuración avanzada y los
  comandos para consultar el estado del bot.
- `/set-channel`: selecciona el canal de avisos.
- `/set-location`: reemplaza tus ubicaciones por una sola, usando el formato
  estándar (ver [Ubicaciones](#ubicaciones)).
- `/add-location` y `/remove-location`: agrega o quita ubicaciones. Se pueden
  guardar hasta 3 y se combinan con lógica OR. Si quitas la última, quedan
  **0/3**: `Worldwide` se usa solo por defecto, sin contar como ubicación
  guardada.
- `/set-max-age`: define cuántas horas hacia atrás buscar, desde 10 minutos
  (0.1667 horas) hasta 24 horas. Por ejemplo `0.5` son 30 minutos.
- `/add-keyword` y `/remove-keyword`: administra los términos de búsqueda.
  `/add-keyword` acepta varias keywords separadas por comas, por ejemplo:
  `backend, frontend, python`. `/remove-keyword` acepta el mismo formato.
- `/add-blacklist-company` y `/remove-blacklist-company`: ignora o reactiva
  empresas específicas, aceptando coincidencias parciales sin distinguir
  mayúsculas.
- `/add-language` y `/remove-language`: configura idiomas permitidos,
  por ejemplo `español, inglés`. La lista aparece en `/list-keywords`.
- `/list-languages`: muestra los idiomas disponibles y los activos.
- `/add-portal` y `/remove-portal`: activa o desactiva portales de empleo
  desde una lista (`linkedin`, `chiletrabajos`, `computrabajo`, `getonbrd`).
  Cada servidor debe conservar al menos uno activo. `/add-portal` rechaza un
  portal incompatible con las ubicaciones actuales y explica cómo corregirlo.
- `/list-portals`: muestra cada portal como `activo (✅)` o `pausado(⏸️)`, con la
  búsqueda resultante y su cobertura.
- `/list-locations`: muestra las ubicaciones actuales, cómo se traducen en
  cada portal y los formatos aceptados.
- `/list-keywords`: muestra la configuración actual, incluidas las ubicaciones,
  los portales y su estado.
- `/status`: muestra latencia, portales (con su estado) y número de avisos
  enviados.

## Ubicaciones

`/set-location` y `/add-location` aceptan un formato estándar:

| Formato | Ejemplo | Alcance |
|---|---|---|
| `Worldwide` | `Worldwide` | Cualquier modalidad, sin filtro geográfico |
| `Remoto` | `Remoto` | Solo ofertas remotas en todo el mundo |
| `Remoto, país` | `Remoto, Chile` | Remoto en un país |
| `País` | `Chile`, `España` | Todo el país |
| `Ciudad, país` | `Santiago, Chile` | Esa ciudad |

`Worldwide` es la ubicación por defecto: busca ofertas de cualquier modalidad
en todos los países, y cada portal aporta lo que puede. También se aceptan
`Todo el mundo`, `Global`, `Anywhere` y `Cualquier lugar` como alias.

- Se pueden guardar hasta **3** ubicaciones y se combinan con lógica **OR**.
- También se acepta una ciudad chilena sola (`Santiago`): se completa como
  `Santiago, Chile`, pero el formato recomendado es `ciudad, país`.
- Si el país no se reconoce (por ejemplo `Narnia`) o la modalidad no es válida
  (por ejemplo `online`), el bot pide corregirla y sugiere usar un país real,
  `Remoto` o `Worldwide`.
- Cada portal tiene su propia cobertura. Si **todas** las ubicaciones quedan
  fuera de la cobertura de un portal, ese portal queda **pausado (⏸️)** y se
  reactiva solo al volver a una ubicación compatible. `/list-portals`,
  `/list-keywords`, `/status` y `/list-locations` muestran ese estado.
- Por ejemplo, ChileTrabajos solo cubre Chile. Además ofrece 58 ciudades: si
  escribes una ciudad chilena que no está en esa lista, se busca todo Chile y
  se avisa; si hay una equivalencia (por ejemplo `Viña del Mar` →
  `Valparaíso`, `Providencia` → `Santiago`), se usa el bucket correspondiente
  y también se avisa.
- LinkedIn usa el nombre del país en inglés por dentro, pero el bot conserva y
  muestra lo que escribiste.

### Datos de países y ciudades

Los datos editables viven en JSON; permite agregar
países, alias o equivalencias de ciudades:

- `src/data/countries.json`
  - `countries`: lista de `{ "code", "name_es", "name_en" }`.
  - `aliases`: pares `"texto que escribe el usuario": "CÓDIGO"`. Se normalizan
    mayúsculas y acentos, así que `"EE.UU."` o `"eeuu"` funcionan igual.
- `src/data/chiletrabajos_cities.json`
  - Lista de `{ "name", "bucket", "region", "aliases" }`.
  - `bucket` es el valor que usa ChileTrabajos para esa ciudad.
  - `aliases` agrega equivalencias, por ejemplo `"Viña del Mar"` dentro de
    `Valparaíso`.
- `src/data/computrabajo_locations.json`
  - `default_country`: país usado con `Worldwide`, `Remoto` o ubicaciones no
    reconocidas (`CL` por defecto).
  - `countries`: por código ISO, `label`, `subdomain`, `aliases` y `locations`.
  - Cada `location` tiene `slug` (el segmento exacto que exige el portal),
    `label`, `region`, `city` y `aliases`. El `slug` se usa textualmente:
    `...-en-<slug>`, nunca se genera a partir de texto libre.

Después de editar un JSON, reinicia el bot para que tome los cambios. Si el
JSON tiene un error (por ejemplo, un alias apunta a un país inexistente), el
bot falla al iniciar indicando el problema.

Cada servidor tiene sus propias ubicaciones, canal, keywords, portales e
historial de vacantes notificadas. El bot consulta las ofertas publicadas
durante las últimas 24 horas y evita reenviar la misma vacante del mismo
portal. El historial de avisos se purga automáticamente después de 25 horas
para no llenar la base de datos.

Si una misma vacante aparece en varios portales dentro del mismo ciclo, se envía
un solo mensaje con un link por portal. Si aparece en otro portal en un ciclo
posterior, se envía de nuevo, porque puede ser el portal que más le interese al
servidor.

El filtro local prioriza coincidencias en el título, reconoce variantes como
`backend`/`back-end` y `ingeniero de software`/`software engineer`, y usa la
descripción como respaldo para términos técnicos.

La API guest de LinkedIn puede aplicar rate limits o modificar su HTML. En
caso de `429`, el ciclo se omite y el worker continúa ejecutándose.

ChileTrabajos se consulta con las mismas keywords y se puede activar con
`/add-portal` (por ejemplo `chiletrabajos`). Usa la fecha exacta del aviso para
respetar la antigüedad configurada, por lo que el primer ciclo puede tardar algo
más. Revisa hasta 5 páginas de 30 avisos por keyword; para términos de rol
normalmente basta la primera. Para ese portal conviene priorizar keywords de rol
(`backend`, `frontend`, `desarrollador`, `programador`, `fullstack`, `devops`,
`qa`) antes que términos genéricos como `informatica`. Requiere `tzdata` para
interpretar las fechas de Chile en Windows.

Get on Board se puede activar con `/add-portal` usando `getonbrd`. Usa su API
pública (`https://www.getonbrd.com/api/v0/`), que no requiere token, y trae la
descripción completa y la empresa en la misma búsqueda, así que no necesita una
petición extra por aviso. La API no permite filtrar por país ni ciudad ni
ordenar por fecha: el bot revisa hasta 3 páginas de 50 resultados por keyword y
filtra localmente por antigüedad y ubicación. Cubre cualquier país, aunque su
catálogo se concentra en Latinoamérica y ofertas remotas. Para `Remoto` se
incluyen las remotas globales y las limitadas a un país; para `País` se incluyen
las presenciales de ese país y las remotas disponibles para él. Ignora los
avisos sin fecha y se consulta directo, sin pasar por el Worker del proxy.

ChileTrabajos **solo publica ofertas de Chile**. Si el servidor configura una
ubicación de otro país, el portal busca en todo Chile y `/add-portal` y
`/list-portals` muestran ese aviso.

Computrabajo se activa con `/add-portal` (`computrabajo`) y cubre los países
`cl`, `ar`, `mx`, `pe`, `co`, `ec`, `uy`, `ve`, `gt`, `cr`, `pa`, `do`, `sv`,
`hn`, `ni`, `bo` y `py`. **No admite texto libre para la ubicación**: cada país
tiene un catálogo de slugs exactos extraídos de sus propios filtros, guardado en
`src/data/computrabajo_locations.json`.

El formato de la URL depende del país:

- **Chile y Perú**: `-en-<región>-en-<ciudad>`.
  `Santiago, Chile` → `-en-rmetropolitana-en-santiago-centro`;
  `Concepción, Chile` → `-en-biobio-en-concepcion`;
  `Trujillo, Perú` → `-en-la-libertad-en-trujillo`;
  `Lima, Perú` → `-en-lima-en-lima`.
- **México**: los slugs de ciudad son de un solo nivel e incluyen el contexto
  del estado. `Guadalajara, México` → `-en-guadalajara`;
  `Puebla, México` → `-en-puebla-ciudad`;
  `Miguel Hidalgo, México` → `-en-miguel-hidalgo`.
- **País solo** (`Chile`, `Perú`, `México`, ...): se usa el subdominio
  correspondiente sin filtro de ubicación, por ejemplo
  `pe.computrabajo.com/trabajo-de-python`.
- Con `Worldwide` o `Remoto` se busca en todo Chile por defecto; con
  `Remoto, México` se usa `mx.computrabajo.com`. Computrabajo no filtra por
  modalidad, así que esas búsquedas incluyen ofertas presenciales y remotas.
- Una ciudad que no esté en el catálogo de ese país no se convierte en slug:
  se busca en todo el país y `/list-portals` lo advierte. Un país sin portal
  (por ejemplo `España`) deja el portal **pausado (⏸️)**.

Lee hasta 3 páginas por keyword, filtra por la fecha de la tarjeta antes de
descargar cada aviso y usa el `JobPosting` del detalle para la descripción y la
empresa. Revisa `MAX_PAGES_PER_KEYWORD` y `MAX_DETAILS_PER_CYCLE` en
`src/scrapers/computrabajo.py`, y el JSON indicado arriba para agregar países,
regiones, ciudades o alias.

### Probar un portal sin Discord

Para probar un scraper en local sin levantar el bot:

```powershell
@'
import asyncio
from src.scrapers.computrabajo import ComputrabajoScraper

jobs = asyncio.run(
    ComputrabajoScraper().fetch_jobs(["python"], "Santiago, Chile", max_age_hours=2)
)
for job in jobs:
    print(job.published_at, "|", job.company, "|", job.title, "|", job.url)
print("total:", len(jobs))
'@ | .\.venv\Scripts\python.exe -
```

Cambia el portal importado (`chiletrabajos`, `linkedin`, `computrabajo`,
`getonbrd`), la ubicación, las keywords y la antigüedad según lo que quieras
revisar.

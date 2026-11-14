# Job Sniper Discord Bot

Bot multi-servidor para detectar vacantes recientes de LinkedIn y ChileTrabajos
y publicarlas en un canal de Discord.

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

## Proxy de portales (Cloudflare Workers)

El bot puede enviar el tráfico de los portales a un Worker de Cloudflare que
reenvía las peticiones al sitio real. Se usa para dos casos:

- **ChileTrabajos**: Railway no puede conectarse al sitio (timeout a nivel
  TCP), así que **todo** su tráfico pasa por el Worker.
- **LinkedIn**: conecta directo y solo usa el Worker **como respaldo** cuando
  el sitio responde `429` o bloquea la IP de Railway.

Solo se usa el plan **Free** de Cloudflare Workers: 100.000 requests por día,
sin tarjeta y sin servicios extra (no hace falta KV, D1, R2 ni Pages).

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

Solo se proxean los hosts permitidos por cada scraper (`PROXY_HOSTS`) y el
Worker valida además su lista `ALLOWED_HOSTS`. Si el Worker no está
configurado (falta URL o token), todo funciona directo como antes.

### Desplegar el Worker

1. Entra a <https://dash.cloudflare.com> → **Workers & Pages** →
   **Create application** → **Create Worker**.
2. Ponle un nombre, por ejemplo `job-sniper-proxy`, y despliega.
3. Abre **Edit code**, borra el contenido de ejemplo y pega
   `worker/job-sniper-proxy.js`. Vuelve a desplegar.
4. En **Settings** → **Variables and Secrets** agrega un **Secret** llamado
   `PROXY_TOKEN` con un valor largo y aleatorio (por ejemplo
   `python -c "import secrets; print(secrets.token_urlsafe(48))"`).
5. Opcional: agrega la variable `ALLOWED_HOSTS` con los hosts separados por
   coma. Por defecto permite `www.chiletrabajos.cl`, `www.linkedin.com` y
   `cl.linkedin.com`.
6. Copia la URL del Worker: `https://<nombre>.<subdominio>.workers.dev`.

Para agregar un portal nuevo no hay que tocar el código del Worker: basta con
sumar su host a `ALLOWED_HOSTS` (y a `PROXY_HOSTS` del scraper en el repo).
Solo hay que volver a pegar el código si cambia la lógica del Worker; los
cambios del bot se despliegan por Railway.

### Configurar el bot

```powershell
JOB_PROXY_URL=https://<nombre>.<subdominio>.workers.dev
JOB_PROXY_TOKEN=<el mismo valor del Secret>
JOB_PROXY_MODE=
```

`JOB_PROXY_MODE` vacío deja el modo automático. La URL del Worker debe apuntar
a la raíz (sin ruta adicional).

### Probar

```powershell
# 1. ¿Está desplegado?
curl.exe -i "https://<nombre>.<subdominio>.workers.dev/__health"

# 2. ¿Responde ChileTrabajos a través del Worker?
curl.exe -i -H "X-Proxy-Token: <token>" -H "X-Target-Host: www.chiletrabajos.cl" `
  "https://<nombre>.<subdominio>.workers.dev/encuentra-un-empleo?2=python&fecha=2"
```

El segundo comando debe devolver `HTTP 200` con HTML del sitio. Si devuelve
`403`, el token no coincide o el host no está permitido; si devuelve `502`, el
sitio rechazó la conexión del Worker. En los logs del bot, cada ciclo imprime
`ChileTrabajos transport: proxy <url> (always)` o
`LinkedIn transport: proxy <url> (fallback)` según corresponda.

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
- `/set-location`: define una ciudad, país o ubicación como `Remote`.
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
  desde una lista (`linkedin`, `chiletrabajos`). Cada servidor debe conservar
  al menos uno activo.
- `/list-portals`: muestra los portales disponibles y los activos.
- `/list-keywords`: muestra la configuración actual, incluidos los portales.
- `/status`: muestra latencia, portales y número de avisos enviados.

Cada servidor tiene su propia ubicación, canal, keywords, portales e historial de
vacantes notificadas. El bot consulta las ofertas publicadas durante las
últimas 24 horas y evita reenviar la misma vacante del mismo portal. El
historial de avisos se purga automáticamente después de 25 horas para no llenar
la base de datos.

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

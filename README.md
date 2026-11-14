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

El Worker solo reenvía peticiones `GET` hacia los hosts permitidos y requiere
un token compartido con el bot.

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

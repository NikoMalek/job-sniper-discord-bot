# Job Sniper Discord Bot

Bot multi-servidor para detectar vacantes recientes de LinkedIn y publicarlas
en un canal de Discord.

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
defecto y `CHECK_INTERVAL_MINUTES` usa 5 minutos.

## Uso

Todos los comandos administrativos requieren `Manage Server`:

- `/set-channel`: selecciona el canal de avisos.
- `/set-location`: define una ciudad, país o ubicación como `Remote`.
- `/set-max-age`: define cuántas horas hacia atrás buscar, desde 0.0167
  horas (1 minuto) hasta 168 horas.
- `/add-keyword` y `/remove-keyword`: administra los términos de búsqueda.
- `/list-keywords`: muestra la configuración actual.
- `/status`: muestra latencia, portales y número de avisos enviados.

Cada servidor tiene su propia ubicación, canal, keywords e historial de
vacantes notificadas. El bot consulta las ofertas publicadas durante las
últimas 24 horas y evita duplicados por servidor.

La API guest de LinkedIn puede aplicar rate limits o modificar su HTML. En
caso de `429`, el ciclo se omite y el worker continúa ejecutándose.

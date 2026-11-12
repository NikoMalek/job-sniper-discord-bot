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

Todos los comandos administrativos requieren `Manage Server`:

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
- `/list-keywords`: muestra la configuración actual.
- `/status`: muestra latencia, portales y número de avisos enviados.

Cada servidor tiene su propia ubicación, canal, keywords e historial de
vacantes notificadas. El bot consulta las ofertas publicadas durante las
últimas 24 horas y evita duplicados por servidor. El historial de avisos se
purga automáticamente después de 25 horas para no llenar la base de datos.

El filtro local prioriza coincidencias en el título, reconoce variantes como
`backend`/`back-end` y `ingeniero de software`/`software engineer`, y usa la
descripción como respaldo para términos técnicos.

La API guest de LinkedIn puede aplicar rate limits o modificar su HTML. En
caso de `429`, el ciclo se omite y el worker continúa ejecutándose.

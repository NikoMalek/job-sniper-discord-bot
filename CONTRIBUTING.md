# Cómo aportar a Job Sniper

¡Gracias por querer ayudar! Job Sniper es de código abierto y los aportes son
bienvenidos: corregir errores, proponer funciones, sumar un portal de empleo,
ampliar países y ciudades, o mejorar la documentación.

## Antes de empezar

- Para errores o ideas pequeñas, abre un
  [issue](../../issues/new) o ve directo a un Pull Request.
- Para cambios grandes (un portal nuevo, cambios en comandos o en la base de
  datos), abre primero un issue para acordar el enfoque y evitar trabajo
  perdido.
- Si buscas por dónde empezar, revisa los issues abiertos; los marcados con
  `good first issue` son los más simples.

## Preparar tu entorno

Sigue la sección **Desarrollo local** del [README](README.md): Python 3.12 o
superior, entorno virtual, `pip install -r requirements.txt` y un `.env` con
tu propio token de bot de prueba.

En local usa `JOB_PROXY_MODE=off` y deja vacías `JOB_PROXY_URL` y
`JOB_PROXY_TOKEN`, para que las pruebas nunca pasen por el Worker.

## Flujo de ramas

- `main`: producción. Está protegida; no se trabaja directamente sobre ella.
- `develop`: integración y pruebas antes de publicar.

Crea tu rama desde `develop`:

```powershell
git checkout develop
git pull origin develop
git checkout -b feature/nombre-del-cambio   # o fix/nombre-del-cambio
```

Cuando termines:

```powershell
git add .
git commit -m "feat: describe el cambio"
git push -u origin feature/nombre-del-cambio
```

Abre un Pull Request hacia `develop`. Las correcciones urgentes de producción
(`hotfix/...`) salen desde `main` y van con PR a `main`; el mantenedor se
encarga de llevarlas también a `develop`.

## Validar tu cambio

- Si tocaste un scraper, pruébalo sin Discord con el ejemplo de la sección
  **Probar un portal sin Discord** del README.
- Si editaste un JSON de `src/data/`, reinicia el bot: si el JSON tiene un
  error, falla al iniciar y te indica el problema.
- Si tocaste comandos o mensajes, pruébalo en un servidor de prueba propio.

## Aportes frecuentes

**Ubicaciones y alias (JSON, sin tocar Python).** Se editan en `src/data/`:
- `countries.json`, `chiletrabajos_cities.json`, `computrabajo_locations.json` y `firstjob_locations.json`: países/ciudades/alias de ubicaciones.
- `keyword_aliases.json`: alias de palabras clave usadas al buscar coincidencias en ofertas.
- `blacklist_keyword_alias_groups.json`: grupos de alias comunes para `/add-blacklist-keyword` con la opción `incluir_alias`.

El README explica el uso de ubicaciones y filtros; al editar estos JSON, reinicia el bot para validar el formato.

**Un portal nuevo.** Toma uno de los scrapers de `src/scrapers/` como
plantilla. Además:

- Respeta los límites de páginas y de detalles por ciclo para no sobrecargar
  el portal.
- Declara los hosts que consulta (`PROXY_HOSTS`) y, si usa el proxy, agrégalos
  también a `ALLOWED_HOSTS` del Worker.
- Documenta en el README su cobertura, sus filtros y sus limitaciones (por
  ejemplo, si no publica fecha).
- Busca dónde aparecen los demás portales en el código (por ejemplo, buscando
  `firstjob`) y registra el nuevo en los mismos lugares.

## Reglas del proyecto

- Nunca subas secretos: `.env`, tokens de Discord ni el token del proxy.
- Los textos que ve el usuario del bot van en español.
- Usa **Conventional Commits** en los mensajes de commit. Ejemplos simples:
  - `feat: add blacklist keyword alias option`
  - `fix: handle empty blacklist keyword input`
  - `docs: clarify blacklist keyword command usage`
- Prefiere Pull Requests pequeños y enfocados, con una descripción de qué
  cambia y cómo lo probaste.

## Seguridad

Si encuentras un problema de seguridad (por ejemplo, una filtración de
credenciales), no abras un issue público: usa **Report a vulnerability** en la
pestaña *Security* del repositorio.

## Licencia

Al enviar un aporte aceptas que se publique bajo la licencia del repositorio.
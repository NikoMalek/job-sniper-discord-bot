"""Discord embed construction and safe delivery."""

from __future__ import annotations

import io
import logging
from collections.abc import Sequence
from datetime import datetime, timezone
from urllib.parse import urlsplit

import discord
import httpx

from .database import Database
from .languages import preferred_ui_language, tracking_text
from .scrapers.base import Job

LOGGER = logging.getLogger(__name__)

LINKEDIN_ICON = "https://img.icons8.com/color/96/linkedin.png"
CHILETRABAJOS_ICON = "https://s3.amazonaws.com/cht2/front/img/logo.png"
GETONBRD_ICON = (
    "https://d2dgum4gsvdsrq.cloudfront.net/assets/"
    "icon192-64920e83a21d9477c8d163577f6cd2f6691c9bcd0499e37325dcb1246709e3e8.png"
)
COMPUTRABAJO_ICON = "https://cl.computrabajo.com/c/img/logos/logoct-ogp.png"
FIRSTJOB_ICON = "https://cdn.firstjob.me/asset-web/header_fj.png"
SOURCE_COLORS = {
    "linkedin": discord.Colour.from_rgb(10, 102, 194),
    "chiletrabajos": discord.Colour.from_rgb(31, 83, 114),
    "computrabajo": discord.Colour.from_rgb(242, 107, 33),
    "get on board": discord.Colour.from_rgb(0, 147, 211),
    "firstjob": discord.Colour.from_rgb(233, 64, 108),
}
SOURCE_ICONS = {
    "linkedin": LINKEDIN_ICON,
    "chiletrabajos": CHILETRABAJOS_ICON,
    "computrabajo": COMPUTRABAJO_ICON,
    "get on board": GETONBRD_ICON,
    "firstjob": FIRSTJOB_ICON,
}

# Company logos served from these hosts are sometimes too slow for Discord's
# image proxy, which ends up showing "an error occurred while loading the
# image". We download them here and attach them to the message so Discord
# serves them from the upload instead of fetching them externally.
PREFETCH_LOGO_HOSTS = frozenset(
    {"ii.ct-stc.com", "firstjob.me", "www.firstjob.me"}
)
LOGO_MAX_BYTES = 8 * 1024 * 1024
LOGO_CONNECT_TIMEOUT_SECONDS = 3.0
LOGO_READ_TIMEOUT_SECONDS = 6.0


def _should_prefetch_logo(url: str) -> bool:
    """Whether ``url`` is a company logo we should download and attach."""
    if not url:
        return False
    try:
        parts = urlsplit(url)
    except ValueError:
        return False
    return parts.scheme == "https" and (parts.hostname or "").casefold() in (
        PREFETCH_LOGO_HOSTS
    )


def _logo_extension(content: bytes, content_type: str) -> str:
    """Return a Discord-friendly extension when ``content`` is an image."""
    media_type = content_type.split(";", 1)[0].strip().casefold()
    if media_type.startswith("text/") or "json" in media_type or "xml" in media_type:
        return ""
    if content.startswith(b"\x89PNG\r\n\x1a\n"):
        return "png"
    if content.startswith(b"\xff\xd8\xff"):
        return "jpg"
    if content[:6] in (b"GIF87a", b"GIF89a"):
        return "gif"
    if len(content) >= 12 and content[:4] == b"RIFF" and content[8:12] == b"WEBP":
        return "webp"
    return ""


async def _download_logo(url: str) -> tuple[str, bytes] | None:
    """Fetch a company logo, validating that it is a small supported image."""
    timeout = httpx.Timeout(
        LOGO_READ_TIMEOUT_SECONDS,
        connect=LOGO_CONNECT_TIMEOUT_SECONDS,
    )
    try:
        async with httpx.AsyncClient(timeout=timeout, follow_redirects=True) as client:
            response = await client.get(url)
    except httpx.HTTPError as exc:
        LOGGER.debug("Could not prefetch company logo %s: %s", url, exc)
        return None
    if response.status_code != 200:
        LOGGER.debug("Company logo %s returned HTTP %s", url, response.status_code)
        return None
    content = response.content
    if not 16 <= len(content) <= LOGO_MAX_BYTES:
        return None
    extension = _logo_extension(content, response.headers.get("content-type", ""))
    if not extension:
        LOGGER.debug("Company logo %s is not a supported image", url)
        return None
    return extension, content


def _display(value: str, fallback: str = "No especificada", limit: int = 1024) -> str:
    clean_value = " ".join(value.split()).strip()
    if not clean_value:
        return fallback
    return clean_value[:limit]

def _short_description(job: Job, *, experimental: bool = True) -> str:
    description = _display(job.description, "", 280)
    if not description:
        return ""
    # In the experimental zone the link lives only in the tracking button, so
    # the card just ends with dots. Outside it the original linked "Ver más"
    # suffix is restored.
    suffix = "…" if experimental else f"… [Ver más...]({job.url})"
    available = 4000 - len(suffix)
    if len(description) > available:
        description = f"{description[:available].rstrip(' .,;:')}..."
    return f"{description}{suffix}"


def _relative_published(value: datetime) -> str:
    posted = value.astimezone(timezone.utc)
    seconds = max(int((datetime.now(timezone.utc) - posted).total_seconds()), 0)
    if seconds < 60:
        return "hace unos segundos"
    minutes = seconds // 60
    if minutes < 60:
        return f"hace {minutes} minuto{'s' if minutes != 1 else ''}"
    hours = minutes // 60
    if hours < 24:
        return f"hace {hours} hora{'s' if hours != 1 else ''}"
    days = hours // 24
    return f"hace {days} día{'s' if days != 1 else ''}"


def build_job_embed(job: Job) -> discord.Embed:
    return build_jobs_embed([job])


def build_jobs_embed(
    jobs: Sequence[Job], *, experimental: bool = True
) -> discord.Embed:
    """Build one embed for one job or a group of the same job across portals.

    Experimental guilds get a clean card whose only link is the tracking
    button. Other guilds keep the original card: linked title and "Ver más".
    """
    if not jobs:
        raise ValueError("At least one job is required to build an embed.")
    primary = jobs[0]
    sources = list(dict.fromkeys(_display(job.source, "Portal de empleo", 256) for job in jobs))
    source = sources[0]
    source_key = source.casefold()
    embed = discord.Embed(
        title=_display(primary.title, "Vacante sin título", 256),
        url=None if experimental else primary.url,
        colour=SOURCE_COLORS.get(source_key, discord.Colour.blurple()),
    )
    if len(sources) == 1:
        author = f"Nueva vacante · {source}"
        icon = SOURCE_ICONS.get(source_key)
        if icon:
            embed.set_author(name=author, icon_url=icon)
        else:
            embed.set_author(name=author)
    else:
        embed.set_author(name=f"Nueva vacante · {len(sources)} portales")

    short_description = _short_description(primary, experimental=experimental)
    description_parts = [
        f"**Empresa**\n{_display(primary.company)}",
        f"**Ubicación**\n{_display(primary.location)}",
    ]
    if short_description:
        description_parts.insert(0, short_description)
    if len(sources) > 1:
        links = []
        seen_sources: set[str] = set()
        for job in jobs:
            display_source = _display(job.source, "Portal de empleo", 256)
            if display_source in seen_sources:
                continue
            seen_sources.add(display_source)
            links.append(f"• [{display_source}]({job.url})")
        description_parts.append("**Disponible en**\n" + "\n".join(links))
    embed.description = "\n\n".join(description_parts)

    if primary.published_at_datetime is not None:
        published = _relative_published(primary.published_at_datetime)
    else:
        published = _display(primary.published_at, "Fecha no disponible", 256)
    source_label = "Fuentes" if len(sources) > 1 else "Fuente"
    embed.set_footer(
        text=f"{source_label}: {', '.join(sources)} • Publicada: {published}",
    )
    if primary.company_logo_url:
        embed.set_thumbnail(url=primary.company_logo_url)
    return embed


async def build_job_view(
    job: Job,
    *,
    guild_id: int | None = None,
    language: str | None = None,
    database: Database | None = None,
    experimental: bool | None = None,
) -> discord.ui.View | None:
    """Build the experimental tracking button, or ``None`` outside the zone.

    Outside the experimental zone notifications keep the original card (linked
    title and "Ver más"), so there is no button at all. Experimental guilds get
    a button whose short token is stored in the database; it has no local
    callback because the experimental cog handles its ``custom_id`` from
    ``on_interaction`` so it survives bot restarts.
    """
    if experimental is None:
        experimental = (
            database is not None
            and guild_id is not None
            and await database.is_experimental_enabled(guild_id)
        )
    if not experimental:
        return None

    if language is None:
        if database is not None and guild_id is not None:
            config = await database.get_guild_config(guild_id)
            language = preferred_ui_language(config.languages)
        else:
            language = "spanish"

    view = discord.ui.View(timeout=None)
    label = tracking_text("view_job", language)
    if database is not None and guild_id is not None:
        token = await database.store_tracked_job(
            guild_id,
            job.id,
            job.source,
            job.url,
            job.title,
            job.company,
        )
        button = discord.ui.Button(
            label=label,
            style=discord.ButtonStyle.primary,
            custom_id=f"job_track:{token}",
        )
    else:
        button = discord.ui.Button(
            label=label,
            style=discord.ButtonStyle.link,
            url=job.url,
        )
    view.add_item(button)
    return view


async def send_jobs(
    channel: discord.abc.Messageable,
    jobs: Sequence[Job],
    *,
    guild_id: int | None = None,
    language: str | None = None,
    database: Database | None = None,
) -> bool:
    group = list(jobs)
    if not group:
        return False
    first = group[0]
    experimental = (
        database is not None
        and guild_id is not None
        and await database.is_experimental_enabled(guild_id)
    )
    view = await build_job_view(
        first,
        guild_id=guild_id,
        language=language,
        database=database,
        experimental=experimental,
    )

    logo: tuple[str, bytes] | None = None
    if _should_prefetch_logo(first.company_logo_url):
        logo = await _download_logo(first.company_logo_url)

    # Try the reliable attached logo first, then fall back to a plain embed.
    attempts = ([True] if logo is not None else []) + [False]
    for with_file in attempts:
        embed = build_jobs_embed(group, experimental=experimental)
        send_kwargs: dict[str, object] = {"embed": embed}
        # Passing view=None explicitly makes discord.py call None.is_finished().
        if view is not None:
            send_kwargs["view"] = view
        if with_file:
            assert logo is not None
            extension, content = logo
            filename = f"company-logo.{extension}"
            embed.set_thumbnail(url=f"attachment://{filename}")
            send_kwargs["files"] = [
                discord.File(io.BytesIO(content), filename=filename)
            ]
        try:
            await channel.send(**send_kwargs)
            return True
        except discord.Forbidden as exc:
            if with_file:
                LOGGER.info(
                    "Cannot attach logo for job %s; retrying without it: %s",
                    first.id,
                    exc,
                )
                continue
            LOGGER.warning("Could not send job %s to channel: %s", first.id, exc)
            return False
        except discord.NotFound as exc:
            LOGGER.warning("Could not send job %s to channel: %s", first.id, exc)
            return False
        except discord.HTTPException:
            LOGGER.exception("Discord rejected notification for job %s", first.id)
            return False
    return False


async def send_job(
    channel: discord.abc.Messageable,
    job: Job,
    *,
    guild_id: int | None = None,
    language: str | None = None,
    database: Database | None = None,
) -> bool:
    return await send_jobs(
        channel,
        [job],
        guild_id=guild_id,
        language=language,
        database=database,
    )

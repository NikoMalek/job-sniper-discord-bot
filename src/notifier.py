"""Discord embed construction and safe delivery."""

from __future__ import annotations

import logging
from collections.abc import Sequence

import discord

from .scrapers.base import Job

LOGGER = logging.getLogger(__name__)

LINKEDIN_ICON = "https://img.icons8.com/color/96/linkedin.png"


def _display(value: str, fallback: str = "No especificada", limit: int = 1024) -> str:
    clean_value = " ".join(value.split()).strip()
    if not clean_value:
        return fallback
    return clean_value[:limit]

def _short_description(job: Job) -> str:
    description = _display(job.description, "", 280)
    if not description:
        return ""
    suffix = f"… [Ver más...]({job.url})"
    available = 4000 - len(suffix)
    if len(description) > available:
        description = f"{description[:available].rstrip(' .,;:')}..."
    return f"{description}{suffix}"


def build_job_embed(job: Job) -> discord.Embed:
    return build_jobs_embed([job])


def build_jobs_embed(jobs: Sequence[Job]) -> discord.Embed:
    """Build one embed for one job or a group of the same job across portals."""
    if not jobs:
        raise ValueError("At least one job is required to build an embed.")
    primary = jobs[0]
    sources = list(dict.fromkeys(_display(job.source, "Portal de empleo", 256) for job in jobs))
    source = sources[0]
    source_key = source.casefold()
    source_colors = {
        "linkedin": discord.Colour.from_rgb(10, 102, 194),
    }
    embed = discord.Embed(
        title=_display(primary.title, "Vacante sin título", 256),
        url=primary.url,
        colour=source_colors.get(source_key, discord.Colour.blurple()),
    )
    if len(sources) == 1:
        author = f"Nueva vacante · {source}"
        if source_key == "linkedin":
            embed.set_author(name=author, icon_url=LINKEDIN_ICON)
        else:
            embed.set_author(name=author)
    else:
        embed.set_author(name=f"Nueva vacante · {len(sources)} portales")

    short_description = _short_description(primary)
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

    published = _display(primary.published_at, "Fecha no disponible", 256)
    source_label = "Fuentes" if len(sources) > 1 else "Fuente"
    embed.set_footer(
        text=f"{source_label}: {', '.join(sources)} • Publicada: {published}",
    )
    if primary.company_logo_url:
        embed.set_thumbnail(url=primary.company_logo_url)
    return embed


async def send_jobs(channel: discord.abc.Messageable, jobs: Sequence[Job]) -> bool:
    group = list(jobs)
    if not group:
        return False
    first = group[0]
    try:
        await channel.send(embed=build_jobs_embed(group))
    except (discord.Forbidden, discord.NotFound) as exc:
        LOGGER.warning("Could not send job %s to channel: %s", first.id, exc)
        return False
    except discord.HTTPException:
        LOGGER.exception("Discord rejected notification for job %s", first.id)
        return False
    return True


async def send_job(channel: discord.abc.Messageable, job: Job) -> bool:
    return await send_jobs(channel, [job])

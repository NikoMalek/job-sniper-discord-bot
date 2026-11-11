"""Discord embed construction and safe delivery."""

from __future__ import annotations

import logging

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
    source = _display(job.source, "Portal de empleo", 256)
    source_key = source.casefold()
    source_colors = {
        "linkedin": discord.Colour.from_rgb(10, 102, 194),
    }
    embed = discord.Embed(
        title=_display(job.title, "Vacante sin título", 256),
        url=job.url,
        colour=source_colors.get(source_key, discord.Colour.blurple()),
    )
    if source_key == "linkedin":
        embed.set_author(name=f"Nueva vacante · {source}", icon_url=LINKEDIN_ICON)
    else:
        embed.set_author(name=f"Nueva vacante · {source}")

    short_description = _short_description(job)
    description_parts = [
        f"**Empresa**\n{_display(job.company)}",
        f"**Ubicación**\n{_display(job.location)}",
    ]
    if short_description:
        description_parts.insert(0, short_description)
    embed.description = "\n\n".join(description_parts)

    published = _display(job.published_at, "Fecha no disponible", 256)
    embed.set_footer(
        text=f"Fuente: {source} • Publicada: {published}",
    )
    if job.company_logo_url:
        embed.set_thumbnail(url=job.company_logo_url)
    return embed


async def send_job(channel: discord.abc.Messageable, job: Job) -> bool:
    try:
        await channel.send(embed=build_job_embed(job))
    except (discord.Forbidden, discord.NotFound) as exc:
        LOGGER.warning("Could not send job %s to channel: %s", job.id, exc)
        return False
    except discord.HTTPException:
        LOGGER.exception("Discord rejected notification for job %s", job.id)
        return False
    return True

"""Discord embed construction and safe delivery."""

from __future__ import annotations

import logging

import discord

from .scrapers.base import Job

LOGGER = logging.getLogger(__name__)


def build_job_embed(job: Job) -> discord.Embed:
    embed = discord.Embed(
        title=job.title,
        url=job.url,
        colour=discord.Colour.blurple(),
        description=f"**Empresa:** {job.company}",
    )
    embed.add_field(name="Ubicación", value=job.location or "No especificada", inline=True)
    embed.add_field(name="Portal", value=job.source, inline=True)
    if job.published_at:
        embed.add_field(name="Publicado", value=job.published_at, inline=False)
    embed.set_footer(text="Job Sniper • nueva vacante detectada")
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


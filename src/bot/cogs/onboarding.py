"""One-time setup guide delivered when the bot joins or starts."""

from __future__ import annotations

import logging
from typing import TYPE_CHECKING

import discord
from discord.ext import commands

if TYPE_CHECKING:
    from ..client import JobSniperBot

LOGGER = logging.getLogger(__name__)


def build_setup_embed() -> discord.Embed:
    embed = discord.Embed(
        title="¡Gracias por invitar a Job Sniper! 🎯",
        description=(
            "Job Sniper monitorea ofertas de empleo en tiempo real "
            "y te avisa cuando encuentra una oportunidad."
        ),
        colour=0x0A66C2,
    )
    embed.add_field(
        name="🚀 Primeros Pasos",
        value=(
            "1. Configura el canal de avisos: `/set-channel`\n"
            "2. Define tu país o ciudad: `/set-location` "
            "(por defecto: `Worldwide`)\n"
            "3. Agrega términos para buscar: `/add-keyword`"
        ),
        inline=False,
    )
    embed.add_field(
        name="💡 Configuración avanzada",
        value=(
            "Por defecto busca ofertas de las últimas **24 horas** en "
            "**LinkedIn** y acepta todos los idiomas.\n"
            "Puedes activar otros portales como **ChileTrabajos** con "
            "`/add-portal`. Para ajustar la antigüedad, empresas ignoradas, "
            "idiomas y portales, usa `/help`."
        ),
        inline=False,
    )
    return embed


class OnboardingCog(commands.Cog):
    def __init__(self, bot: JobSniperBot) -> None:
        self.bot = bot
        self.database = bot.database
        self._checked_existing = False
        self._sending: set[int] = set()

    @commands.Cog.listener()
    async def on_ready(self) -> None:
        if self._checked_existing:
            return
        self._checked_existing = True
        for guild in self.bot.guilds:
            await self._send_if_needed(guild)

    @commands.Cog.listener()
    async def on_guild_join(self, guild: discord.Guild) -> None:
        await self._send_if_needed(guild)

    async def _send_if_needed(self, guild: discord.Guild) -> None:
        if guild.id in self._sending:
            return
        self._sending.add(guild.id)
        try:
            if await self.database.has_setup_message(guild.id):
                return
            if await self._send_guide(guild):
                await self.database.mark_setup_message_sent(guild.id)
        except Exception:
            LOGGER.exception("Could not deliver setup guide to guild %s", guild.id)
        finally:
            self._sending.discard(guild.id)

    async def _send_guide(self, guild: discord.Guild) -> bool:
        embed = build_setup_embed()
        for channel in await self._candidate_channels(guild):
            try:
                await channel.send(embed=embed)
            except (discord.Forbidden, discord.NotFound, discord.HTTPException) as exc:
                LOGGER.warning(
                    "Could not send setup guide to #%s in guild %s: %s",
                    channel.name,
                    guild.id,
                    exc,
                )
                continue
            LOGGER.info(
                "Sent setup guide to #%s in guild %s", channel.name, guild.id
            )
            return True

        owner = guild.owner
        if owner is None:
            try:
                owner = await self.bot.fetch_user(guild.owner_id)
            except (discord.NotFound, discord.HTTPException):
                owner = None
        if owner is not None:
            try:
                await owner.send(embed=embed)
            except (discord.Forbidden, discord.HTTPException) as exc:
                LOGGER.warning(
                    "Could not DM setup guide to owner of guild %s: %s",
                    guild.id,
                    exc,
                )
            else:
                LOGGER.info(
                    "Sent setup guide by DM to owner of guild %s", guild.id
                )
                return True

        LOGGER.warning(
            "No channel or owner DM available for setup guide in guild %s",
            guild.id,
        )
        return False

    async def _candidate_channels(
        self, guild: discord.Guild
    ) -> list[discord.TextChannel]:
        candidates: list[discord.abc.GuildChannel] = []
        config = await self.database.get_guild_config(guild.id)
        if config.channel_id:
            channel = guild.get_channel(config.channel_id)
            if channel is None:
                try:
                    channel = await self.bot.fetch_channel(config.channel_id)
                except (discord.NotFound, discord.Forbidden, discord.HTTPException):
                    channel = None
            if channel is not None and getattr(channel, "guild", None) == guild:
                candidates.append(channel)
        if guild.system_channel is not None:
            candidates.append(guild.system_channel)
        candidates.extend(guild.text_channels)

        me = guild.me
        if me is None and self.bot.user is not None:
            me = guild.get_member(self.bot.user.id)
        if me is None and self.bot.user is not None:
            try:
                me = await guild.fetch_member(self.bot.user.id)
            except (discord.NotFound, discord.Forbidden, discord.HTTPException):
                me = None

        result: list[discord.TextChannel] = []
        seen: set[int] = set()
        for channel in candidates:
            if not isinstance(channel, discord.TextChannel) or channel.id in seen:
                continue
            seen.add(channel.id)
            if me is None:
                continue
            permissions = channel.permissions_for(me)
            if permissions.send_messages and permissions.embed_links:
                result.append(channel)
        return result

"""Administrative slash commands."""

from __future__ import annotations

import discord
from discord import app_commands
from discord.ext import commands
from typing import TYPE_CHECKING

from ...database import Database

if TYPE_CHECKING:
    from ..client import JobSniperBot


class AdminCog(commands.Cog):
    def __init__(self, bot: JobSniperBot) -> None:
        self.bot = bot
        self.database: Database = bot.database

    @staticmethod
    def _clean(value: str, field: str) -> str:
        cleaned = " ".join(value.split()).strip()
        if not cleaned:
            raise ValueError(f"{field} no puede estar vacío.")
        return cleaned[:200]

    @app_commands.command(name="set-channel", description="Configura el canal de notificaciones.")
    @app_commands.guild_only()
    @app_commands.checks.has_permissions(manage_guild=True)
    async def set_channel(self, interaction: discord.Interaction, channel: discord.TextChannel) -> None:
        await self.database.set_channel(interaction.guild_id or 0, channel.id)
        await interaction.response.send_message(
            f"Las vacantes se enviarán en {channel.mention}.", ephemeral=True
        )

    @app_commands.command(name="set-location", description="Configura país o ciudad de búsqueda.")
    @app_commands.guild_only()
    @app_commands.checks.has_permissions(manage_guild=True)
    async def set_location(self, interaction: discord.Interaction, location: str) -> None:
        try:
            clean_location = self._clean(location, "La ubicación")
        except ValueError as exc:
            await interaction.response.send_message(str(exc), ephemeral=True)
            return
        await self.database.set_location(interaction.guild_id or 0, clean_location)
        await interaction.response.send_message(f"Ubicación configurada: **{clean_location}**.", ephemeral=True)

    @app_commands.command(name="set-max-age", description="Configura la antigüedad máxima de las vacantes.")
    @app_commands.guild_only()
    @app_commands.checks.has_permissions(manage_guild=True)
    async def set_max_age(self, interaction: discord.Interaction, hours: float) -> None:
        if not 1 / 60 <= hours <= 168:
            await interaction.response.send_message(
                "La antigüedad debe estar entre 0.0167 horas (1 minuto) y 168 horas (7 días).",
                ephemeral=True,
            )
            return
        await self.database.set_max_age_hours(interaction.guild_id or 0, hours)
        await interaction.response.send_message(
            f"Buscaré vacantes publicadas en las últimas **{hours:g} horas**.",
            ephemeral=True,
        )

    @app_commands.command(name="add-keyword", description="Agrega una palabra clave.")
    @app_commands.guild_only()
    @app_commands.checks.has_permissions(manage_guild=True)
    async def add_keyword(self, interaction: discord.Interaction, keyword: str) -> None:
        try:
            clean_keyword = self._clean(keyword, "La palabra clave")
        except ValueError as exc:
            await interaction.response.send_message(str(exc), ephemeral=True)
            return
        added = await self.database.add_keyword(interaction.guild_id or 0, clean_keyword)
        message = "Palabra clave agregada." if added else "Esa palabra clave ya estaba configurada."
        await interaction.response.send_message(message, ephemeral=True)

    @app_commands.command(name="remove-keyword", description="Elimina una palabra clave.")
    @app_commands.guild_only()
    @app_commands.checks.has_permissions(manage_guild=True)
    async def remove_keyword(self, interaction: discord.Interaction, keyword: str) -> None:
        clean_keyword = " ".join(keyword.split()).strip()
        removed = await self.database.remove_keyword(interaction.guild_id or 0, clean_keyword)
        message = "Palabra clave eliminada." if removed else "No encontré esa palabra clave."
        await interaction.response.send_message(message, ephemeral=True)

    @app_commands.command(name="list-keywords", description="Muestra la configuración del servidor.")
    @app_commands.guild_only()
    @app_commands.checks.has_permissions(manage_guild=True)
    async def list_keywords(self, interaction: discord.Interaction) -> None:
        config = await self.database.get_guild_config(interaction.guild_id or 0)
        channel = f"<#{config.channel_id}>" if config.channel_id else "No configurado"
        keywords = ", ".join(config.keywords) if config.keywords else "Ninguna"
        await interaction.response.send_message(
            f"**Canal:** {channel}\n**Ubicación:** {config.location}\n"
            f"**Antigüedad máxima:** {config.max_age_hours:g} horas\n"
            f"**Palabras clave:** {keywords}",
            ephemeral=True,
        )

    @app_commands.command(name="status", description="Muestra el estado del bot en este servidor.")
    @app_commands.guild_only()
    @app_commands.checks.has_permissions(manage_guild=True)
    async def status(self, interaction: discord.Interaction) -> None:
        config = await self.database.get_guild_config(interaction.guild_id or 0)
        total = await self.database.count_notified(interaction.guild_id or 0)
        interval = self.bot.settings.check_interval_minutes
        await interaction.response.send_message(
            f"**Estado:** online\n**Portales activos:** LinkedIn\n"
            f"**Latencia:** {self.bot.latency * 1000:.0f} ms\n"
            f"**Ofertas enviadas:** {total}\n**Intervalo:** {interval:g} min\n"
            f"**Antigüedad máxima:** {config.max_age_hours:g} horas",
            ephemeral=True,
        )

    async def cog_app_command_error(
        self, interaction: discord.Interaction, error: app_commands.AppCommandError
    ) -> None:
        if isinstance(error, app_commands.MissingPermissions):
            message = "Necesitas permisos de administrador del servidor para usar este comando."
        else:
            message = "No se pudo completar el comando. Revisa los logs del bot."
        if interaction.response.is_done():
            await interaction.followup.send(message, ephemeral=True)
        else:
            await interaction.response.send_message(message, ephemeral=True)

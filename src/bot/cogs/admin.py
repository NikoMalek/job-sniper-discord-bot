"""Administrative slash commands."""

from __future__ import annotations

import discord
from discord import app_commands
from discord.ext import commands
from typing import TYPE_CHECKING

from ...database import MAX_MAX_AGE_HOURS, MIN_MAX_AGE_HOURS, Database
from ...languages import SUPPORTED_LANGUAGES, parse_languages

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

    @staticmethod
    def _format_age(hours: float) -> str:
        total_minutes = round(hours * 60)
        if total_minutes < 60:
            return f"{total_minutes} minuto{'s' if total_minutes != 1 else ''}"
        whole_hours, minutes = divmod(total_minutes, 60)
        label = f"{whole_hours} hora{'s' if whole_hours != 1 else ''}"
        return f"{label} {minutes} min" if minutes else label

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

    @app_commands.command(
        name="set-max-age",
        description="Configura la antigüedad máxima en horas (0,5 = 30 minutos).",
    )
    @app_commands.describe(
        hours="Horas hacia atrás. En español usa coma decimal: 0,5 son 30 minutos."
    )
    @app_commands.guild_only()
    @app_commands.checks.has_permissions(manage_guild=True)
    async def set_max_age(self, interaction: discord.Interaction, hours: float) -> None:
        if not MIN_MAX_AGE_HOURS <= hours <= MAX_MAX_AGE_HOURS:
            await interaction.response.send_message(
                "La antigüedad debe estar entre 10 minutos y 24 horas "
                "(por ejemplo 0,5 = 30 minutos, 8 = 8 horas).",
                ephemeral=True,
            )
            return
        await self.database.set_max_age_hours(interaction.guild_id or 0, hours)
        await interaction.response.send_message(
            f"Buscaré vacantes publicadas en los últimos **{self._format_age(hours)}**.",
            ephemeral=True,
        )

    @app_commands.command(name="add-keyword", description="Agrega una palabra clave.")
    @app_commands.guild_only()
    @app_commands.checks.has_permissions(manage_guild=True)
    async def add_keyword(self, interaction: discord.Interaction, keyword: str) -> None:
        try:
            keywords = [
                self._clean(value, "La palabra clave")
                for value in keyword.split(",")
                if value.strip()
            ]
        except ValueError as exc:
            await interaction.response.send_message(str(exc), ephemeral=True)
            return
        if not keywords:
            await interaction.response.send_message(
                "Debes indicar al menos una palabra clave.",
                ephemeral=True,
            )
            return

        added_keywords = [
            value
            for value in dict.fromkeys(keywords)
            if await self.database.add_keyword(interaction.guild_id or 0, value)
        ]
        if added_keywords:
            message = f"Palabras clave agregadas: **{', '.join(added_keywords)}**."
        else:
            message = "Todas las palabras clave ya estaban configuradas."
        await interaction.response.send_message(message, ephemeral=True)

    @app_commands.command(name="remove-keyword", description="Elimina una palabra clave.")
    @app_commands.guild_only()
    @app_commands.checks.has_permissions(manage_guild=True)
    async def remove_keyword(self, interaction: discord.Interaction, keyword: str) -> None:
        keywords = [" ".join(value.split()).strip() for value in keyword.split(",") if value.strip()]
        if not keywords:
            await interaction.response.send_message(
                "Debes indicar al menos una palabra clave.",
                ephemeral=True,
            )
            return
        removed_keywords = [
            value
            for value in dict.fromkeys(keywords)
            if await self.database.remove_keyword(interaction.guild_id or 0, value)
        ]
        if removed_keywords:
            message = f"Palabras clave eliminadas: **{', '.join(removed_keywords)}**."
        else:
            message = "No encontré ninguna de esas palabras clave."
        await interaction.response.send_message(message, ephemeral=True)

    @app_commands.command(name="add-blacklist-company", description="Ignora vacantes de una empresa.")
    @app_commands.guild_only()
    @app_commands.checks.has_permissions(manage_guild=True)
    async def add_blacklist_company(self, interaction: discord.Interaction, company: str) -> None:
        try:
            clean_company = self._clean(company, "La empresa")
        except ValueError as exc:
            await interaction.response.send_message(str(exc), ephemeral=True)
            return
        added = await self.database.add_blacklisted_company(interaction.guild_id or 0, clean_company)
        message = (
            f"Las vacantes de **{clean_company}** serán ignoradas."
            if added
            else "Esa empresa ya estaba en la blacklist."
        )
        await interaction.response.send_message(message, ephemeral=True)

    @app_commands.command(name="remove-blacklist-company", description="Deja de ignorar una empresa.")
    @app_commands.guild_only()
    @app_commands.checks.has_permissions(manage_guild=True)
    async def remove_blacklist_company(self, interaction: discord.Interaction, company: str) -> None:
        clean_company = " ".join(company.split()).strip()
        removed = await self.database.remove_blacklisted_company(interaction.guild_id or 0, clean_company)
        message = "Empresa eliminada de la blacklist." if removed else "No encontré esa empresa."
        await interaction.response.send_message(message, ephemeral=True)

    @app_commands.command(name="add-language", description="Configura idiomas permitidos para vacantes.")
    @app_commands.guild_only()
    @app_commands.checks.has_permissions(manage_guild=True)
    async def add_language(self, interaction: discord.Interaction, language: str) -> None:
        languages = parse_languages(language)
        if not languages:
            await interaction.response.send_message(
                "Debes indicar al menos un idioma.", ephemeral=True
            )
            return
        unsupported = [
            value for value in dict.fromkeys(languages) if value not in SUPPORTED_LANGUAGES
        ]
        if unsupported:
            await interaction.response.send_message(
                f"Idiomas no soportados: **{', '.join(unsupported)}**. "
                "Usa español, inglés, portugués, francés o alemán.",
                ephemeral=True,
            )
            return
        added = [
            value for value in dict.fromkeys(languages)
            if await self.database.add_language(interaction.guild_id or 0, value)
        ]
        message = f"Idiomas agregados: **{', '.join(added)}**." if added else "Esos idiomas ya estaban configurados."
        await interaction.response.send_message(message, ephemeral=True)

    @app_commands.command(name="remove-language", description="Elimina idiomas permitidos.")
    @app_commands.guild_only()
    @app_commands.checks.has_permissions(manage_guild=True)
    async def remove_language(self, interaction: discord.Interaction, language: str) -> None:
        languages = parse_languages(language)
        if not languages:
            await interaction.response.send_message(
                "Debes indicar al menos un idioma.", ephemeral=True
            )
            return
        removed = [
            value for value in dict.fromkeys(languages)
            if await self.database.remove_language(interaction.guild_id or 0, value)
        ]
        message = f"Idiomas eliminados: **{', '.join(removed)}**." if removed else "No encontré esos idiomas."
        await interaction.response.send_message(message, ephemeral=True)

    @app_commands.command(name="list-keywords", description="Muestra la configuración del servidor.")
    @app_commands.guild_only()
    @app_commands.checks.has_permissions(manage_guild=True)
    async def list_keywords(self, interaction: discord.Interaction) -> None:
        config = await self.database.get_guild_config(interaction.guild_id or 0)
        channel = f"<#{config.channel_id}>" if config.channel_id else "No configurado"
        keywords = ", ".join(config.keywords) if config.keywords else "Ninguna"
        blacklist = ", ".join(config.blacklisted_companies) if config.blacklisted_companies else "Ninguna"
        languages = ", ".join(config.languages) if config.languages else "Todos"
        await interaction.response.send_message(
            f"**Canal:** {channel}\n**Ubicación:** {config.location}\n"
            f"**Antigüedad máxima:** {self._format_age(config.max_age_hours)}\n"
            f"**Palabras clave:** {keywords}\n**Empresas ignoradas:** {blacklist}\n"
            f"**Idiomas:** {languages}",
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
            f"**Antigüedad máxima:** {self._format_age(config.max_age_hours)}",
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

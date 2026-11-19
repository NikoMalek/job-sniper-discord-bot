"""Administrative slash commands."""

from __future__ import annotations

import logging
import discord
from discord import app_commands
from discord.ext import commands
from typing import TYPE_CHECKING

from ...countries import COUNTRIES_BY_CODE
from ...database import (
    DEFAULT_LOCATION,
    MAX_MAX_AGE_HOURS,
    MIN_MAX_AGE_HOURS,
    Database,
)
from ...languages import LANGUAGE_LABELS, SUPPORTED_LANGUAGES, parse_languages
from ...locations import (
    LOCATION_FORMAT_HELP,
    MAX_LOCATIONS,
    LocationError,
    LocationIntent,
    normalize,
    parse_location,
    parse_location_or_none,
)
from ...scrapers import SCRAPERS, portal_names

if TYPE_CHECKING:
    from ..client import JobSniperBot

LOGGER = logging.getLogger(__name__)

PORTAL_CHOICES = [
    app_commands.Choice(name=scraper.source, value=slug)
    for slug, scraper in SCRAPERS.items()
]


def _portal_label(source: str) -> str:
    """Human-readable portal name for a configured slug."""
    scraper = SCRAPERS.get(source)
    return scraper.source if scraper is not None else source


LANGUAGE_CHOICES = [
    app_commands.Choice(name=f"{label.title()} ({language})", value=language)
    for language, label in LANGUAGE_LABELS.items()
]
PAUSE_HELP = (
    "**Cobertura y pausa**\n"
    "Cada portal tiene su propia cobertura. Si **todas** tus ubicaciones "
    "quedan fuera de la cobertura de un portal, ese portal queda "
    "**pausado (⏸️)** y se reactiva automáticamente cuando vuelvas a una "
    "ubicación compatible."
)


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

    @staticmethod
    def _intents(locations) -> list[LocationIntent]:
        intents: list[LocationIntent] = []
        for value in tuple(locations) or (DEFAULT_LOCATION,):
            intent = parse_location_or_none(value)
            if intent is not None:
                intents.append(intent)
        return intents

    @staticmethod
    def _location_key(value: str) -> str:
        intent = parse_location_or_none(value)
        return normalize(intent.label if intent is not None else value)

    @staticmethod
    def _scraper_plans(
        scraper, intents: list[LocationIntent]
    ) -> tuple[list[str], list[str], list[str]]:
        """Return searches, notes and pause reasons for one portal."""
        searches: list[str] = []
        notes: list[str] = []
        reasons: list[str] = []
        for intent in intents:
            plan = scraper.plan(intent)
            if not plan.supported:
                if plan.reason and plan.reason not in reasons:
                    reasons.append(plan.reason)
                continue
            if plan.search_location and plan.search_location not in searches:
                searches.append(plan.search_location)
            if plan.note and plan.note not in notes:
                notes.append(plan.note)
        return searches, notes, reasons

    @classmethod
    def _plan_lines(
        cls, portals, intents: list[LocationIntent]
    ) -> tuple[list[str], list[str]]:
        """Return per-portal resolution lines and pause warnings."""
        lines: list[str] = []
        warnings: list[str] = []
        for slug in portals:
            scraper = SCRAPERS.get(slug)
            if scraper is None:
                continue
            searches, notes, reasons = cls._scraper_plans(scraper, intents)
            if searches:
                lines.append(f"• **{scraper.source}** → {' · '.join(searches)}")
                for note in notes:
                    lines.append(f"   ↳ {note}")
            else:
                lines.append(f"• **{scraper.source}** → pausado (⏸️)")
                for reason in reasons:
                    warnings.append(f"**{scraper.source}**: {reason}")
        return lines, warnings

    @classmethod
    def _portal_summary(cls, portals, intents: list[LocationIntent]) -> str:
        labels: list[str] = []
        for slug in portals:
            scraper = SCRAPERS.get(slug)
            if scraper is None:
                labels.append(slug)
                continue
            searches, _, _ = cls._scraper_plans(scraper, intents)
            state = "✅" if searches else "⏸️"
            labels.append(f"{scraper.source} {state}")
        return ", ".join(labels) or "Ninguno"

    @classmethod
    def _pause_warnings(
        cls, portals, intents: list[LocationIntent]
    ) -> list[str]:
        warnings: list[str] = []
        for slug in portals:
            scraper = SCRAPERS.get(slug)
            if scraper is None:
                continue
            searches, _, reasons = cls._scraper_plans(scraper, intents)
            if searches:
                continue
            reason = reasons[0] if reasons else "sin ubicaciones compatibles."
            warnings.append(f"**{scraper.source}** está en pausa: {reason}")
        return warnings

    @staticmethod
    def _portal_supported(scraper, intents: list[LocationIntent]) -> bool:
        return any(scraper.plan(intent).supported for intent in intents)

    @staticmethod
    def _covered_countries(scraper) -> str:
        if scraper.COVERED_COUNTRIES is None:
            return "cualquier país"
        names = []
        for code in sorted(scraper.COVERED_COUNTRIES):
            country = COUNTRIES_BY_CODE.get(code)
            names.append(country.name_es if country else code)
        return ", ".join(names)

    @app_commands.command(name="set-channel", description="Configura el canal de notificaciones.")
    @app_commands.guild_only()
    @app_commands.default_permissions(manage_guild=True)
    async def set_channel(self, interaction: discord.Interaction, channel: discord.TextChannel) -> None:
        await self.database.set_channel(interaction.guild_id or 0, channel.id)
        await interaction.response.send_message(
            f"Las vacantes se enviarán en {channel.mention}.", ephemeral=True
        )

    @app_commands.command(
        name="set-location",
        description="Configura la ubicación de búsqueda (reemplaza las anteriores).",
    )
    @app_commands.describe(
        location="Ej.: Worldwide · Remoto · Santiago, Chile · Remoto, España"
    )
    @app_commands.guild_only()
    @app_commands.default_permissions(manage_guild=True)
    async def set_location(self, interaction: discord.Interaction, location: str) -> None:
        try:
            intent = parse_location(location)
        except LocationError as exc:
            await interaction.response.send_message(str(exc), ephemeral=True)
            return
        guild_id = interaction.guild_id or 0
        await self.database.set_location(guild_id, intent.label)
        config = await self.database.get_guild_config(guild_id)
        intents = self._intents(config.locations)
        lines, warnings = self._plan_lines(config.portals, intents)

        parts = [f"Ubicación configurada: **{intent.label}**"]
        if intent.inferred_country:
            parts.append(
                f"Interpreté **{intent.city}** como ciudad de Chile. "
                "El formato recomendado es `ciudad, país`."
            )
        parts.append("\n".join(lines))
        if warnings:
            parts.append("\n".join(warnings))
        if not any(
            self._portal_supported(SCRAPERS[slug], intents)
            for slug in config.portals
            if slug in SCRAPERS
        ):
            parts.append(
                "⚠️ Ningún portal activo cubre esta ubicación. "
                "Activa LinkedIn o cambia la ubicación."
            )
        await interaction.response.send_message("\n\n".join(parts), ephemeral=True)

    @app_commands.command(
        name="add-location",
        description="Agrega una ubicación de búsqueda (máximo 3).",
    )
    @app_commands.describe(
        location="Ej.: Osorno, Chile · La Unión, Chile · Remoto, España"
    )
    @app_commands.guild_only()
    @app_commands.default_permissions(manage_guild=True)
    async def add_location(self, interaction: discord.Interaction, location: str) -> None:
        try:
            intent = parse_location(location)
        except LocationError as exc:
            await interaction.response.send_message(str(exc), ephemeral=True)
            return
        guild_id = interaction.guild_id or 0
        config = await self.database.get_guild_config(guild_id)
        if any(
            self._location_key(existing) == self._location_key(intent.label)
            for existing in config.locations
        ):
            await interaction.response.send_message(
                f"**{intent.label}** ya estaba configurada.", ephemeral=True
            )
            return
        if len(config.locations) >= MAX_LOCATIONS:
            await interaction.response.send_message(
                f"Ya tienes **{MAX_LOCATIONS}** ubicaciones, el máximo permitido. "
                "Quita una con `/remove-location` antes de agregar otra.",
                ephemeral=True,
            )
            return
        await self.database.add_location(guild_id, intent.label)
        config = await self.database.get_guild_config(guild_id)
        intents = self._intents(config.locations)
        lines, warnings = self._plan_lines(config.portals, intents)

        parts = [
            f"Ubicación agregada: **{intent.label}** "
            f"({len(config.locations)}/{MAX_LOCATIONS})."
        ]
        if intent.inferred_country:
            parts.append(
                f"Interpreté **{intent.city}** como ciudad de Chile. "
                "El formato recomendado es `ciudad, país`."
            )
        parts.append("\n".join(lines))
        if warnings:
            parts.append("\n".join(warnings))
        await interaction.response.send_message("\n\n".join(parts), ephemeral=True)

    @app_commands.command(
        name="remove-location",
        description="Quita una ubicación de búsqueda configurada.",
    )
    @app_commands.describe(location="Ubicación que quieres quitar.")
    @app_commands.guild_only()
    @app_commands.default_permissions(manage_guild=True)
    async def remove_location(self, interaction: discord.Interaction, location: str) -> None:
        guild_id = interaction.guild_id or 0
        config = await self.database.get_guild_config(guild_id)
        target = next(
            (
                value
                for value in config.locations
                if self._location_key(value) == self._location_key(location)
            ),
            None,
        )
        if target is None:
            await interaction.response.send_message(
                f"No encontré **{location}** entre tus ubicaciones.", ephemeral=True
            )
            return
        was_last = len(config.locations) <= 1
        await self.database.remove_location(guild_id, target)
        config = await self.database.get_guild_config(guild_id)
        intents = self._intents(config.locations)
        lines, warnings = self._plan_lines(config.portals, intents)
        parts = [f"Ubicación eliminada: **{target}**."]
        if was_last:
            parts.append(
                "No quedan ubicaciones guardadas; se usará **Worldwide** "
                "por defecto."
            )
        parts.append("\n".join(lines))
        if warnings:
            parts.append("\n".join(warnings))
        await interaction.response.send_message("\n\n".join(parts), ephemeral=True)

    @remove_location.autocomplete("location")
    async def remove_location_autocomplete(
        self, interaction: discord.Interaction, current: str
    ) -> list[app_commands.Choice[str]]:
        if interaction.guild_id is None:
            return []
        locations = await self.database.get_locations(interaction.guild_id)
        normalized_current = normalize(current)
        return [
            app_commands.Choice(name=value, value=value)
            for value in locations
            if normalized_current in normalize(value)
        ][:25]

    @app_commands.command(
        name="set-max-age",
        description="Configura la antigüedad máxima en horas (0,5 = 30 minutos).",
    )
    @app_commands.describe(
        hours="Horas hacia atrás. En español usa coma decimal: 0,5 son 30 minutos."
    )
    @app_commands.guild_only()
    @app_commands.default_permissions(manage_guild=True)
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
    @app_commands.default_permissions(manage_guild=True)
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
    @app_commands.default_permissions(manage_guild=True)
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

    @remove_keyword.autocomplete("keyword")
    async def remove_keyword_autocomplete(
        self, interaction: discord.Interaction, current: str
    ) -> list[app_commands.Choice[str]]:
        if interaction.guild_id is None:
            return []
        config = await self.database.get_guild_config(interaction.guild_id)
        normalized_current = current.casefold().strip()
        return [
            app_commands.Choice(name=keyword, value=keyword)
            for keyword in config.keywords
            if normalized_current in keyword.casefold()
        ][:25]

    @app_commands.command(name="add-blacklist-company", description="Ignora vacantes de una empresa.")
    @app_commands.guild_only()
    @app_commands.default_permissions(manage_guild=True)
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
    @app_commands.default_permissions(manage_guild=True)
    async def remove_blacklist_company(self, interaction: discord.Interaction, company: str) -> None:
        clean_company = " ".join(company.split()).strip()
        removed = await self.database.remove_blacklisted_company(interaction.guild_id or 0, clean_company)
        message = "Empresa eliminada de la blacklist." if removed else "No encontré esa empresa."
        await interaction.response.send_message(message, ephemeral=True)

    @app_commands.command(name="help", description="Muestra cómo configurar Job Sniper.")
    @app_commands.guild_only()
    @app_commands.default_permissions(manage_guild=True)
    async def help_command(self, interaction: discord.Interaction) -> None:
        embed = discord.Embed(
            title="Ayuda de Job Sniper",
            description=(
                "Job Sniper monitorea ofertas de empleo y envía avisos al canal "
                "que configures cuando encuentra coincidencias."
            ),
            colour=0x0A66C2,
        )
        embed.add_field(
            name="🚀 Primeros Pasos",
            value=(
                "1. `/set-channel` — elige dónde se publicarán las alertas. "
                "No hay un canal predeterminado.\n"
                "2. `/set-location` — define la ubicación con un formato "
                "estándar: `Worldwide`, `Remoto`, `Remoto, país`, `País` "
                "o `Ciudad, país`. Por ejemplo `Worldwide`, `Chile`, "
                "`Santiago, Chile` o `Remoto, España`. Por defecto se busca "
                "en `Worldwide`. Puedes sumar hasta 3 con `/add-location`.\n"
                "3. `/add-keyword` — agrega los cargos o tecnologías que "
                "quieres encontrar, por ejemplo `python, backend`."
            ),
            inline=False,
        )
        embed.add_field(
            name="⚙️ Configuración avanzada",
            value=(
                "`/set-max-age` — limita la antigüedad de las ofertas. "
                "Por defecto busca las últimas 24 horas.\n"
                "`/add-language` y `/remove-language` — filtra por idioma. "
                "Por defecto acepta todos los idiomas.\n"
                "`/add-portal` y `/remove-portal` — activa o desactiva "
                "portales. LinkedIn está activo por defecto y puedes sumar "
                "ChileTrabajos o Get on Board. Cada portal tiene su propia "
                "cobertura: si todas tus ubicaciones quedan fuera, se pausa "
                "automáticamente.\n"
                "`/add-blacklist-company` y `/remove-blacklist-company` — "
                "ignora o vuelve a permitir empresas."
            ),
            inline=False,
        )
        embed.add_field(
            name="📋 Consultar configuración",
            value=(
                "Usa `/list-keywords`, `/list-languages`, "
                "`/list-portals` o `/list-locations` para revisar cada "
                "sección. `/status` muestra un resumen general y confirma "
                "si el bot está listo."
            ),
            inline=False,
        )
        embed.add_field(
            name="🧪 Experimental Zone",
            value=(
                "Usa `/experimental` para revisar y activar las funciones "
                "experimentales."
            ),
            inline=False,
        )
        await interaction.response.send_message(embed=embed, ephemeral=True)

    @app_commands.command(name="add-language", description="Configura idiomas permitidos para vacantes.")
    @app_commands.describe(language="Idioma que quieres activar.")
    @app_commands.choices(language=LANGUAGE_CHOICES)
    @app_commands.guild_only()
    @app_commands.default_permissions(manage_guild=True)
    async def add_language(
        self, interaction: discord.Interaction, language: app_commands.Choice[str]
    ) -> None:
        languages = parse_languages(language.value)
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
    @app_commands.describe(language="Idioma que quieres desactivar.")
    @app_commands.choices(language=LANGUAGE_CHOICES)
    @app_commands.guild_only()
    @app_commands.default_permissions(manage_guild=True)
    async def remove_language(
        self, interaction: discord.Interaction, language: app_commands.Choice[str]
    ) -> None:
        languages = parse_languages(language.value)
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

    @app_commands.command(
        name="add-portal",
        description="Activa un portal de empleo para este servidor.",
    )
    @app_commands.describe(portal="Portal que quieres activar.")
    @app_commands.choices(portal=PORTAL_CHOICES)
    @app_commands.guild_only()
    @app_commands.default_permissions(manage_guild=True)
    async def add_portal(
        self, interaction: discord.Interaction, portal: app_commands.Choice[str]
    ) -> None:
        guild_id = interaction.guild_id or 0
        config = await self.database.get_guild_config(guild_id)
        if portal.value in config.portals:
            await interaction.response.send_message(
                f"El portal **{portal.name}** ya estaba activo.", ephemeral=True
            )
            return
        scraper = SCRAPERS.get(portal.value)
        intents = self._intents(config.locations)
        if scraper is not None and not self._portal_supported(scraper, intents):
            await interaction.response.send_message(
                f"No activé **{portal.name}**: solo cubre "
                f"{self._covered_countries(scraper)} y tus ubicaciones "
                f"actuales son **{', '.join(config.locations) or 'ninguna'}**.\n\n"
                "Cambia la ubicación con `/set-location` o agrega una "
                "compatible con `/add-location`, y vuelve a intentarlo.",
                ephemeral=True,
            )
            return
        await self.database.add_portal(guild_id, portal.value)
        lines, warnings = self._plan_lines((portal.value,), intents)
        parts = [f"Portal activado: **{portal.name}**."]
        if lines:
            parts.append("\n".join(lines))
        if warnings:
            parts.append("\n".join(warnings))
        await interaction.response.send_message("\n\n".join(parts), ephemeral=True)

    @app_commands.command(
        name="remove-portal",
        description="Desactiva un portal de empleo de este servidor.",
    )
    @app_commands.describe(portal="Portal activo o pausado que quieres desactivar.")
    @app_commands.guild_only()
    @app_commands.default_permissions(manage_guild=True)
    async def remove_portal(
        self, interaction: discord.Interaction, portal: str
    ) -> None:
        config = await self.database.get_guild_config(interaction.guild_id or 0)
        label = _portal_label(portal)
        if portal not in config.portals:
            await interaction.response.send_message(
                f"El portal **{label}** no estaba activo.",
                ephemeral=True,
            )
            return
        if len(config.portals) <= 1:
            await interaction.response.send_message(
                "No puedes desactivar el último portal. Activa otro antes de quitarlo.",
                ephemeral=True,
            )
            return
        removed = await self.database.remove_portal(
            interaction.guild_id or 0, portal
        )
        message = (
            f"Portal desactivado: **{label}**."
            if removed
            else f"El portal **{label}** no estaba activo."
        )
        await interaction.response.send_message(message, ephemeral=True)

    @remove_portal.autocomplete("portal")
    async def remove_portal_autocomplete(
        self, interaction: discord.Interaction, current: str
    ) -> list[app_commands.Choice[str]]:
        if interaction.guild_id is None:
            return []
        config = await self.database.get_guild_config(interaction.guild_id)
        normalized = current.casefold().strip()
        return [
            app_commands.Choice(name=_portal_label(slug), value=slug)
            for slug in config.portals
            if normalized in _portal_label(slug).casefold()
            or normalized in slug.casefold()
        ][:25]

    @app_commands.command(
        name="list-languages",
        description="Muestra los idiomas disponibles y los activos en este servidor.",
    )
    @app_commands.guild_only()
    @app_commands.default_permissions(manage_guild=True)
    async def list_languages(self, interaction: discord.Interaction) -> None:
        config = await self.database.get_guild_config(interaction.guild_id or 0)
        available = "\n".join(
            f"• **{label}** — `{language}`"
            for language, label in LANGUAGE_LABELS.items()
        )
        active = (
            ", ".join(LANGUAGE_LABELS.get(value, value) for value in config.languages)
            or "Todos (sin filtro)"
        )
        await interaction.response.send_message(
            f"**Idiomas disponibles**\n{available}\n\n"
            f"**Activos en este servidor:** {active}\n\n"
            "Puedes usar el nombre o el código con `/add-language` y `/remove-language`.",
            ephemeral=True,
        )

    @app_commands.command(
        name="list-portals",
        description="Muestra los portales disponibles, su estado y cobertura.",
    )
    @app_commands.guild_only()
    @app_commands.default_permissions(manage_guild=True)
    async def list_portals(self, interaction: discord.Interaction) -> None:
        guild_id = interaction.guild_id or 0
        config = await self.database.get_guild_config(guild_id)
        intents = self._intents(config.locations)
        active_slugs = set(config.portals)
        lines = ["**Portales disponibles**"]
        running = 0
        for slug, scraper in SCRAPERS.items():
            coverage = f" · cobertura: {self._covered_countries(scraper)}"
            if slug not in active_slugs:
                lines.append(
                    f"• **{scraper.source}** (`{slug}`) — no activo{coverage}"
                )
                continue
            searches, notes, reasons = self._scraper_plans(scraper, intents)
            if searches:
                running += 1
                lines.append(
                    f"• **{scraper.source}** (`{slug}`) — ✅ activo{coverage}"
                )
                lines.append(f"   ↳ Busca: {' · '.join(searches)}")
                for note in notes:
                    lines.append(f"   ↳ {note}")
            else:
                lines.append(
                    f"• **{scraper.source}** (`{slug}`) — pausado (⏸️) {coverage}"
                )
                for reason in reasons:
                    lines.append(f"   ↳ {reason}")

        active = ", ".join(portal_names(config.portals)) or "Ninguno"
        parts = [
            "\n".join(lines),
            f"**Activos en este servidor:** {active}\n"
            f"**Ejecutándose ahora:** {running} de {len(config.portals)}",
        ]
        if running == 0:
            parts.append(
                "⚠️ Ningún portal se está ejecutando con las ubicaciones "
                "actuales. Revisa `/list-locations`."
            )
        parts.append(PAUSE_HELP)
        parts.append("Usa `/add-portal` y `/remove-portal` para cambiarlos.")
        await interaction.response.send_message("\n\n".join(parts), ephemeral=True)

    @app_commands.command(
        name="list-locations",
        description="Muestra tus ubicaciones y cómo se traducen en cada portal.",
    )
    @app_commands.guild_only()
    @app_commands.default_permissions(manage_guild=True)
    async def list_locations(self, interaction: discord.Interaction) -> None:
        guild_id = interaction.guild_id or 0
        config = await self.database.get_guild_config(guild_id)
        intents = self._intents(config.locations)
        current = (
            "\n".join(f"• **{intent.label}**" for intent in intents)
            if config.locations
            else f"• Sin ubicaciones guardadas · se usa **{DEFAULT_LOCATION}**"
        )
        lines, warnings = self._plan_lines(config.portals, intents)
        parts = [
            f"**Tus ubicaciones ({len(config.locations)}/{MAX_LOCATIONS})**\n"
            f"{current}",
            "**Traducción por portal**\n" + "\n".join(lines),
        ]
        if warnings:
            parts.append("\n".join(warnings))
        parts.append(LOCATION_FORMAT_HELP)
        parts.append(PAUSE_HELP)

        embed = discord.Embed(
            title="Ubicaciones de búsqueda",
            description="\n\n".join(parts),
            colour=0x0A66C2,
        )
        await interaction.response.send_message(embed=embed, ephemeral=True)

    @app_commands.command(name="list-keywords", description="Muestra la configuración del servidor.")
    @app_commands.guild_only()
    @app_commands.default_permissions(manage_guild=True)
    async def list_keywords(self, interaction: discord.Interaction) -> None:
        config = await self.database.get_guild_config(interaction.guild_id or 0)
        intents = self._intents(config.locations)
        channel = f"<#{config.channel_id}>" if config.channel_id else "No configurado"
        locations = (
            " · ".join(f"**{intent.label}**" for intent in intents)
            if config.locations
            else f"**{DEFAULT_LOCATION}** (por defecto)"
        )
        keywords = ", ".join(config.keywords) if config.keywords else "Ninguna"
        blacklist = ", ".join(config.blacklisted_companies) if config.blacklisted_companies else "Ninguna"
        languages = ", ".join(config.languages) if config.languages else "Todos"
        portals = self._portal_summary(config.portals, intents)
        parts = [
            f"**Canal:** {channel}",
            f"**Ubicaciones ({len(config.locations)}/{MAX_LOCATIONS}):** "
            f"{locations}",
            f"**Portales:** {portals}",
            f"**Antigüedad máxima:** {self._format_age(config.max_age_hours)}",
            f"**Palabras clave:** {keywords}",
            f"**Empresas ignoradas:** {blacklist}",
            f"**Idiomas:** {languages}",
        ]
        warnings = self._pause_warnings(config.portals, intents)
        if warnings:
            parts.append("\n".join(warnings))
            parts.append(PAUSE_HELP)
        await interaction.response.send_message(
            "\n".join(parts), ephemeral=True
        )

    @app_commands.command(name="status", description="Muestra el estado del bot en este servidor.")
    @app_commands.guild_only()
    @app_commands.default_permissions(manage_guild=True)
    async def status(self, interaction: discord.Interaction) -> None:
        config = await self.database.get_guild_config(interaction.guild_id or 0)
        intents = self._intents(config.locations)
        total = await self.database.count_notified(interaction.guild_id or 0)
        interval = self.bot.settings.check_interval_minutes
        portals = self._portal_summary(config.portals, intents)
        parts = [
            "**Estado:** online",
            f"**Portales activos:** {portals}",
            f"**Latencia:** {self.bot.latency * 1000:.0f} ms",
            f"**Ofertas enviadas:** {total}",
            f"**Intervalo:** {interval:g} min",
            f"**Antigüedad máxima:** {self._format_age(config.max_age_hours)}",
        ]
        warnings = self._pause_warnings(config.portals, intents)
        if warnings:
            parts.append("\n".join(warnings))
        running = any(
            self._portal_supported(SCRAPERS[slug], intents)
            for slug in config.portals
            if slug in SCRAPERS
        )
        if not running:
            parts.append(
                "⚠️ Ningún portal se está ejecutando con las ubicaciones "
                "actuales. Revisa `/list-locations`."
            )
        await interaction.response.send_message("\n".join(parts), ephemeral=True)

    async def cog_app_command_error(
        self, interaction: discord.Interaction, error: app_commands.AppCommandError
    ) -> None:
        if isinstance(error, app_commands.CheckFailure):
            message = "No tienes permiso para usar este comando."
        else:
            LOGGER.error("Command failed in guild %s", interaction.guild_id, exc_info=error)
            message = "No se pudo completar el comando. Revisa los logs del bot."
        if interaction.response.is_done():
            await interaction.followup.send(message, ephemeral=True)
        else:
            await interaction.response.send_message(message, ephemeral=True)

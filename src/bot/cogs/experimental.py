"""Experimental-zone commands, modal tracking and guild leaderboards."""

from __future__ import annotations

import logging
import math
from datetime import datetime, timezone
from typing import TYPE_CHECKING

import discord
from discord import app_commands, ui
from discord.ext import commands

from ...database import (
    DEFAULT_LOCATION,
    ApplicationRecord,
    ApplicationSummary,
    DebugReport,
    TrackedJob,
)
from ...languages import preferred_ui_language, tracking_text
from ...locations import parse_location_or_none
from ...scrapers import SCRAPERS

if TYPE_CHECKING:
    from ..client import JobSniperBot

LOGGER = logging.getLogger(__name__)

JOB_TRACK_PREFIX = "job_track:"
EXPERIMENTAL_TOGGLE_PREFIX = "experimental_toggle:"
PAGE_SIZE = 5
RANK_MEDALS = ("🥇", "🥈", "🥉")


def _display(value: str, fallback: str, limit: int) -> str:
    cleaned = " ".join(value.split()).strip()
    return (cleaned or fallback)[:limit]


def _format_clicked_at(value: str) -> str:
    """Build a Discord timestamp so each viewer sees his own local time."""
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return "—"
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=timezone.utc)
    epoch = int(parsed.timestamp())
    return f"<t:{epoch}:d> · <t:{epoch}:t>"


def _format_clicked_date(value: str) -> str:
    """Render an ISO timestamp as ``DD/MM/YYYY`` in UTC."""
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return value[:10]
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=timezone.utc)
    return parsed.astimezone(timezone.utc).strftime("%d/%m/%Y")


def _active_portal_sources(config) -> list[str]:
    """Display names of configured portals that can run in this guild.

    Portals are stored as slugs and may be paused when none of the guild
    locations is supported, so the active ones are derived from the scrapers.
    """
    intents = [
        intent
        for value in tuple(config.locations) or (DEFAULT_LOCATION,)
        if (intent := parse_location_or_none(value)) is not None
    ]
    sources: list[str] = []
    for slug in config.portals:
        scraper = SCRAPERS.get(slug)
        if scraper is None:
            continue
        active = any(scraper.plan(intent).supported for intent in intents)
        if active and scraper.source not in sources:
            sources.append(scraper.source)
    return sources


def _status_text(applied: bool | None, language: str) -> str:
    """Shared, localized status label for /applications and /debug."""
    key = {
        True: "status_applied",
        False: "status_skipped",
        None: "status_viewed",
    }[applied]
    return tracking_text(key, language)


def _application_line(record: ApplicationRecord, language: str) -> str:
    title = discord.utils.escape_markdown(
        _display(record.job_title, tracking_text("untitled", language), 90)
    )
    company = discord.utils.escape_markdown(
        _display(record.company, tracking_text("unknown_company", language), 60)
    )
    portal = discord.utils.escape_markdown(_display(record.source, "—", 30))
    date = _format_clicked_at(record.clicked_at)
    # Notes are debug-only: they are stored with the answer but listed solely
    # in /debug, never in the public /applications history.
    return (
        f"{date} [{title}]({record.job_url}) · {company} · {portal} · "
        f"{_status_text(record.applied, language)}"
    )


class JobTrackingModal(ui.Modal):
    """Private modal with the real link and the application answer.

    The view is recorded when the button opens the modal; submitting only
    updates that same row with the Yes/No answer. Dismissing the modal with
    X/Escape leaves the row as unanswered because Discord sends no callback.
    """

    def __init__(
        self,
        cog: ExperimentalCog,
        interaction: discord.Interaction,
        tracked: TrackedJob,
        language: str,
        record_id: int,
    ) -> None:
        super().__init__(
            title=tracking_text("modal_title", language)[:45],
            timeout=300,
            custom_id=f"job_modal:{tracked.token}:{interaction.user.id}",
        )
        self.cog = cog
        self.guild_id = interaction.guild_id
        self.user_id = interaction.user.id
        self.tracked = tracked
        self.language = language
        self.record_id = record_id

        title = discord.utils.escape_markdown(
            _display(tracked.job_title, tracking_text("untitled", language), 120)
        )
        company = discord.utils.escape_markdown(
            _display(tracked.company, tracking_text("unknown_company", language), 100)
        )
        self.add_item(
            ui.TextDisplay(
                f"[{tracking_text('open_link', language)}]({tracked.job_url})\n\n"
                + tracking_text(
                    "applied_question", language, title=title, company=company
                )
            )
        )
        self.choice = ui.Select(
            placeholder=tracking_text("choice_placeholder", language),
            options=[
                discord.SelectOption(
                    label=tracking_text("yes_button", language), value="yes"
                ),
                discord.SelectOption(
                    label=tracking_text("no_button", language), value="no"
                ),
            ],
            custom_id="job_apply_choice",
            min_values=1,
            max_values=1,
            required=True,
        )
        # Discord only accepts selects inside a Label in modals. A bare select
        # produces an invalid component payload (HTTP 50035) on send_modal().
        self.add_item(
            ui.Label(
                text=tracking_text("choice_placeholder", language),
                component=self.choice,
            )
        )
        self.notes = ui.TextInput(
            custom_id="job_notes",
            style=discord.TextStyle.long,
            placeholder=tracking_text("observation_placeholder", language),
            required=False,
            max_length=250,
        )
        self.add_item(
            ui.Label(
                text=tracking_text("observation_label", language),
                component=self.notes,
            )
        )

    async def on_submit(self, interaction: discord.Interaction) -> None:
        if interaction.guild_id != self.guild_id or interaction.user.id != self.user_id:
            await interaction.response.send_message(
                tracking_text("not_allowed", self.language), ephemeral=True
            )
            return
        if not await self.cog.database.is_experimental_enabled(self.guild_id or 0):
            await interaction.response.send_message(
                tracking_text("disabled", self.language), ephemeral=True
            )
            return

        applied = (self.choice.values or ["no"])[0] == "yes"
        notes = (self.notes.value or "").strip()
        # The click was already stored when the modal opened; only the answer
        # changes here so views and applications never get double counted.
        await self.cog.database.update_application_status(
            self.record_id, applied, notes or None
        )
        # Acknowledges the submit without posting any message in the channel.
        await interaction.response.defer()


class ApplicationsPaginator(ui.View):
    """Short-lived pagination for `/applications`."""

    def __init__(
        self,
        cog: ExperimentalCog,
        interaction: discord.Interaction,
        target: discord.abc.User,
        language: str,
        summary: ApplicationSummary,
    ) -> None:
        super().__init__(timeout=180)
        self.cog = cog
        self.viewer_id = interaction.user.id
        self.guild_id = interaction.guild_id or 0
        self.target = target
        self.language = language
        self.summary = summary
        self.page = 0
        self.pages = max(1, math.ceil(summary.clicked / PAGE_SIZE))
        self.message: discord.Message | None = None

        self.previous = ui.Button(
            label=tracking_text("previous_button", language),
            style=discord.ButtonStyle.secondary,
            custom_id="applications_previous",
        )
        self.previous.callback = self._on_previous
        self.next = ui.Button(
            label=tracking_text("next_button", language),
            style=discord.ButtonStyle.secondary,
            custom_id="applications_next",
        )
        self.next.callback = self._on_next
        self.add_item(self.previous)
        self.add_item(self.next)
        self._sync_buttons()

    def _sync_buttons(self) -> None:
        self.previous.disabled = self.page == 0
        self.next.disabled = self.page >= self.pages - 1

    def _base_embed(self) -> discord.Embed:
        if self.target.id == self.viewer_id:
            title = tracking_text("applications_self_title", self.language)
            description = tracking_text("applications_self_desc", self.language)
        else:
            title = tracking_text(
                "applications_other_title",
                self.language,
                user=self.target.display_name,
            )
            description = tracking_text(
                "applications_other_desc",
                self.language,
                user=self.target.mention,
            )
        return discord.Embed(title=title, description=description, colour=0x9B59B6)

    async def build_embed(self) -> discord.Embed:
        records = await self.cog.database.get_user_applications(
            self.guild_id,
            self.target.id,
            limit=PAGE_SIZE,
            offset=self.page * PAGE_SIZE,
        )
        # A single chronological list: the query already sorts newest first
        # and every line carries its own status, so section headers are noise.
        lines = [_application_line(record, self.language) for record in records]
        embed = self._base_embed()
        totals = tracking_text(
            "totals_value",
            self.language,
            clicked=self.summary.clicked,
            applied=self.summary.applied,
            skipped=self.summary.skipped,
            viewed=self.summary.viewed,
        )
        embed.description = "\n\n".join(
            (
                embed.description or "",
                f"**{tracking_text('totals_label', self.language)}**\n{totals}",
                "\n".join(lines) or tracking_text("no_data", self.language),
            )
        )
        embed.set_footer(
            text=tracking_text(
                "page_footer",
                self.language,
                page=self.page + 1,
                pages=self.pages,
                total=self.summary.clicked,
            )
        )
        return embed

    async def _refresh(self, interaction: discord.Interaction) -> None:
        # The message is public, so the wording adapts to whoever pages.
        self.viewer_id = interaction.user.id
        self._sync_buttons()
        await interaction.response.edit_message(
            embed=await self.build_embed(), view=self
        )

    async def _on_previous(self, interaction: discord.Interaction) -> None:
        self.page = max(0, self.page - 1)
        await self._refresh(interaction)

    async def _on_next(self, interaction: discord.Interaction) -> None:
        self.page = min(self.pages - 1, self.page + 1)
        await self._refresh(interaction)

    async def on_timeout(self) -> None:
        for child in self.children:
            child.disabled = True
        if self.message is not None:
            try:
                await self.message.edit(view=self)
            except discord.HTTPException:
                pass


class DebugPaginator(ui.View):
    """Short-lived pagination for `/debug` notes."""

    def __init__(
        self,
        cog: ExperimentalCog,
        interaction: discord.Interaction,
        language: str,
        source: str | None,
        total: int,
    ) -> None:
        super().__init__(timeout=180)
        self.cog = cog
        self.guild_id = interaction.guild_id or 0
        self.language = language
        self.source = _display(source or "", "", 50) or None
        self.total = total
        self.page = 0
        self.pages = max(1, math.ceil(total / PAGE_SIZE))
        self.message: discord.Message | None = None

        self.previous = ui.Button(
            label=tracking_text("previous_button", language),
            style=discord.ButtonStyle.secondary,
            custom_id="debug_previous",
        )
        self.previous.callback = self._on_previous
        self.next = ui.Button(
            label=tracking_text("next_button", language),
            style=discord.ButtonStyle.secondary,
            custom_id="debug_next",
        )
        self.next.callback = self._on_next
        self.add_item(self.previous)
        self.add_item(self.next)
        self._sync_buttons()

    def _sync_buttons(self) -> None:
        self.previous.disabled = self.page == 0
        self.next.disabled = self.page >= self.pages - 1

    async def build_embed(self) -> discord.Embed:
        reports = await self.cog.database.get_debug_reports(
            self.guild_id,
            source=self.source,
            limit=PAGE_SIZE,
            offset=self.page * PAGE_SIZE,
        )
        lines: list[str] = []
        for report in reports:
            title = discord.utils.escape_markdown(
                _display(report.job_title, tracking_text("untitled", self.language), 90)
            )
            source = discord.utils.escape_markdown(
                _display(report.source, "—", 30)
            )
            notes = discord.utils.escape_markdown(_display(report.notes, "", 250))
            lines.append(
                f"`{_format_clicked_date(report.clicked_at)}` · <@{report.user_id}> · "
                f"{source} · [{title}]({report.job_url}) · "
                f"{_status_text(report.applied, self.language)}\n> {notes}"
            )

        embed = discord.Embed(
            title=tracking_text("debug_title", self.language),
            description=tracking_text("debug_desc", self.language),
            colour=0x9B59B6,
        )
        if self.source:
            embed.description += "\n" + tracking_text(
                "debug_filter", self.language, source=discord.utils.escape_markdown(self.source)
            )
        embed.description += "\n\n" + (
            "\n\n".join(lines) or tracking_text("debug_empty", self.language)
        )
        embed.set_footer(
            text=tracking_text(
                "page_footer",
                self.language,
                page=self.page + 1,
                pages=self.pages,
                total=self.total,
            )
        )
        return embed

    async def _refresh(self, interaction: discord.Interaction) -> None:
        self._sync_buttons()
        await interaction.response.edit_message(
            embed=await self.build_embed(), view=self
        )

    async def _on_previous(self, interaction: discord.Interaction) -> None:
        self.page = max(0, self.page - 1)
        await self._refresh(interaction)

    async def _on_next(self, interaction: discord.Interaction) -> None:
        self.page = min(self.pages - 1, self.page + 1)
        await self._refresh(interaction)

    async def on_timeout(self) -> None:
        for child in self.children:
            child.disabled = True
        if self.message is not None:
            try:
                await self.message.edit(view=self)
            except discord.HTTPException:
                pass


class ExperimentalCog(commands.Cog):
    """Opt-in click tracking, application answers and per-guild leaderboards."""

    def __init__(self, bot: JobSniperBot) -> None:
        self.bot = bot
        self.database = bot.database

    async def _language(self, guild_id: int | None) -> str:
        if guild_id is None:
            return "spanish"
        config = await self.database.get_guild_config(guild_id)
        return preferred_ui_language(config.languages)

    def _status_embed(self, guild_id: int, enabled: bool, language: str) -> discord.Embed:
        state = tracking_text(
            "experimental_enabled" if enabled else "experimental_disabled",
            language,
        )
        embed = discord.Embed(
            title=tracking_text("experimental_title", language),
            description=tracking_text("experimental_desc", language),
            colour=0x9B59B6,
        )
        embed.add_field(
            name=tracking_text("status_label", language),
            value=f"**{state}**",
            inline=False,
        )
        return embed

    def _toggle_view(
        self, guild_id: int, enabled: bool, language: str
    ) -> discord.ui.View:
        label = tracking_text(
            "disable_button" if enabled else "enable_button", language
        )
        style = (
            discord.ButtonStyle.danger if enabled else discord.ButtonStyle.success
        )
        view = discord.ui.View(timeout=None)
        view.add_item(
            discord.ui.Button(
                label=label,
                style=style,
                custom_id=f"{EXPERIMENTAL_TOGGLE_PREFIX}{guild_id}",
            )
        )
        return view

    @staticmethod
    def _ranking_lines(rows, language: str) -> str:
        """One unified list: medals for the podium, numbers afterwards."""
        if not rows:
            return tracking_text("no_data", language)
        lines: list[str] = []
        for index, row in enumerate(rows, start=1):
            rank = (
                RANK_MEDALS[index - 1]
                if index <= len(RANK_MEDALS)
                else f"**`{index}.`**"
            )
            applications = tracking_text(
                "snipers_applications_one"
                if row.applications == 1
                else "snipers_applications_many",
                language,
                count=row.applications,
            )
            views = tracking_text(
                "snipers_views_one" if row.viewed == 1 else "snipers_views_many",
                language,
                count=row.viewed,
            )
            lines.append(f"{rank} <@{row.user_id}>\n-# → {applications} · {views}")
        return "\n\n".join(lines)

    @app_commands.command(
        name="experimental",
        description="Activa o desactiva la Zona Experimental en este servidor.",
    )
    @app_commands.guild_only()
    @app_commands.default_permissions(manage_guild=True)
    async def experimental(self, interaction: discord.Interaction) -> None:
        guild_id = interaction.guild_id or 0
        enabled = await self.database.is_experimental_enabled(guild_id)
        language = await self._language(guild_id)
        await interaction.response.send_message(
            embed=self._status_embed(guild_id, enabled, language),
            view=self._toggle_view(guild_id, enabled, language),
            ephemeral=True,
        )

    @app_commands.command(
        name="snipers",
        description="Muestra el ranking de postulaciones de este servidor.",
    )
    @app_commands.guilds()
    @app_commands.guild_only()
    async def snipers(self, interaction: discord.Interaction) -> None:
        guild_id = interaction.guild_id or 0
        language = await self._language(guild_id)
        rows = await self.database.get_guild_snipers_ranking(guild_id, limit=10)
        total_applied, total_views = await self.database.get_guild_snipers_totals(
            guild_id
        )
        summary = tracking_text(
            "snipers_summary",
            language,
            total_applied=total_applied,
            total_views=total_views,
        )
        embed = discord.Embed(
            title=tracking_text("snipers_title", language),
            description=f"{summary}\n\n{self._ranking_lines(rows, language)}",
            colour=0x9B59B6,
        )
        await interaction.response.send_message(embed=embed)

    @app_commands.command(
        name="applications",
        description="Muestra tus postulaciones o las de un usuario mencionado.",
    )
    @app_commands.describe(
        user="Usuario del que quieres ver el historial (omítelo para ver el tuyo)."
    )
    @app_commands.guilds()
    @app_commands.guild_only()
    @app_commands.default_permissions(manage_guild=True)
    async def applications(
        self,
        interaction: discord.Interaction,
        user: discord.Member | None = None,
    ) -> None:
        guild_id = interaction.guild_id or 0
        target = user or interaction.user
        language = await self._language(guild_id)
        summary = await self.database.get_user_application_summary(guild_id, target.id)
        paginator = ApplicationsPaginator(self, interaction, target, language, summary)
        embed = await paginator.build_embed()
        if paginator.pages > 1:
            await interaction.response.send_message(embed=embed, view=paginator)
            paginator.message = await interaction.original_response()
        else:
            # Passing view=None explicitly makes discord.py call
            # None.is_finished(), so omit the keyword entirely here.
            await interaction.response.send_message(embed=embed)

    @app_commands.command(
        name="debug",
        description="Muestra las observaciones dejadas al registrar postulaciones.",
    )
    @app_commands.describe(
        source="Filtra por un portal activo en este servidor."
    )
    @app_commands.guilds()
    @app_commands.guild_only()
    @app_commands.default_permissions(manage_guild=True)
    async def debug(
        self,
        interaction: discord.Interaction,
        source: str | None = None,
    ) -> None:
        guild_id = interaction.guild_id or 0
        language = await self._language(guild_id)
        clean_source = _display(source or "", "", 50) or None
        total = await self.database.get_debug_report_count(guild_id, clean_source)
        paginator = DebugPaginator(self, interaction, language, clean_source, total)
        embed = await paginator.build_embed()
        if paginator.pages > 1:
            await interaction.response.send_message(embed=embed, view=paginator)
            paginator.message = await interaction.original_response()
        else:
            # Passing view=None explicitly makes discord.py call
            # None.is_finished(), so omit the keyword entirely here.
            await interaction.response.send_message(embed=embed)

    @debug.autocomplete("source")
    async def debug_source_autocomplete(
        self, interaction: discord.Interaction, current: str
    ) -> list[app_commands.Choice[str]]:
        if interaction.guild_id is None:
            return []
        config = await self.database.get_guild_config(interaction.guild_id)
        normalized = current.casefold().strip()
        return [
            app_commands.Choice(name=source, value=source)
            for source in _active_portal_sources(config)
            if normalized in source.casefold()
        ][:25]

    @commands.Cog.listener()
    async def on_interaction(self, interaction: discord.Interaction) -> None:
        """Handle persistent buttons; modals handle their own submit callback."""
        if interaction.type != discord.InteractionType.component:
            return
        data = interaction.data or {}
        custom_id = data.get("custom_id")
        if not isinstance(custom_id, str):
            return
        try:
            if custom_id.startswith(JOB_TRACK_PREFIX):
                await self._handle_job_track(
                    interaction, custom_id[len(JOB_TRACK_PREFIX) :]
                )
            elif custom_id.startswith(EXPERIMENTAL_TOGGLE_PREFIX):
                await self._handle_toggle(
                    interaction, custom_id[len(EXPERIMENTAL_TOGGLE_PREFIX) :]
                )
        except (ValueError, discord.HTTPException):
            LOGGER.exception("Could not handle tracking interaction %s", custom_id)
            if not interaction.response.is_done():
                language = await self._language(interaction.guild_id)
                await interaction.response.send_message(
                    tracking_text("expired", language), ephemeral=True
                )

    async def _handle_job_track(
        self, interaction: discord.Interaction, token: str
    ) -> None:
        guild_id = interaction.guild_id
        if guild_id is None:
            return
        language = await self._language(guild_id)
        tracked = await self.database.get_tracked_job(token.strip())
        if tracked is None or tracked.guild_id != guild_id:
            await interaction.response.send_message(
                tracking_text("expired", language), ephemeral=True
            )
            return
        if not await self.database.is_experimental_enabled(guild_id):
            await interaction.response.send_message(
                f"{tracking_text('disabled', language)}\n\n"
                f"[{tracking_text('open_link', language)}]({tracked.job_url})",
                ephemeral=True,
            )
            return
        # Opening the modal counts as viewing the offer. The UNIQUE constraint
        # on (guild, user, job) makes repeat clicks update the same row instead
        # of inflating the counters, and preserves any previous answer.
        record_id = await self.database.record_job_click(
            guild_id,
            interaction.user.id,
            tracked.job_url,
            tracked.job_title,
            tracked.company,
            tracked.source,
        )
        await interaction.response.send_modal(
            JobTrackingModal(self, interaction, tracked, language, record_id)
        )

    async def _handle_toggle(
        self, interaction: discord.Interaction, raw_guild_id: str
    ) -> None:
        guild_id = int(raw_guild_id)
        if interaction.guild_id != guild_id:
            return
        language = await self._language(guild_id)
        if (
            not isinstance(interaction.user, discord.Member)
            or not interaction.user.guild_permissions.manage_guild
        ):
            await interaction.response.send_message(
                tracking_text("no_permission", language), ephemeral=True
            )
            return
        enabled = not await self.database.is_experimental_enabled(guild_id)
        await self.database.set_experimental_enabled(guild_id, enabled)
        sync_ok = True
        try:
            await self.bot.sync_experimental_commands(guild_id, enabled)
        except discord.HTTPException:
            sync_ok = False
            LOGGER.exception(
                "Could not sync experimental commands in guild %s", guild_id
            )
        embed = self._status_embed(guild_id, enabled, language)
        if not sync_ok:
            embed.set_footer(text=tracking_text("sync_fallback", language))
        await interaction.response.edit_message(
            embed=embed,
            view=self._toggle_view(guild_id, enabled, language),
        )

    async def cog_app_command_error(
        self, interaction: discord.Interaction, error: app_commands.AppCommandError
    ) -> None:
        language = await self._language(interaction.guild_id)
        if isinstance(error, app_commands.CheckFailure):
            message = tracking_text("no_permission", language)
        else:
            LOGGER.error(
                "Command failed in guild %s", interaction.guild_id, exc_info=error
            )
            message = tracking_text("command_error", language)
        if interaction.response.is_done():
            await interaction.followup.send(message, ephemeral=True)
        else:
            await interaction.response.send_message(message, ephemeral=True)


async def setup(bot: JobSniperBot) -> None:  # pragma: no cover - extension helper
    await bot.add_cog(ExperimentalCog(bot))

"""Discord client setup and command synchronization."""

from __future__ import annotations

import logging

import discord
from discord.ext import commands

from ..config import Settings
from ..database import Database

LOGGER = logging.getLogger(__name__)


class JobSniperBot(commands.Bot):
    def __init__(self, settings: Settings, database: Database) -> None:
        intents = discord.Intents.none()
        intents.guilds = True
        super().__init__(command_prefix=commands.when_mentioned, intents=intents)
        self.settings = settings
        self.database = database
        self._experimental_commands_synced = False

    async def setup_hook(self) -> None:
        await self.add_cog(self._admin_cog())
        await self.add_cog(self._monitor_cog())
        await self.add_cog(self._onboarding_cog())
        await self.add_cog(self._experimental_cog())
        synced = await self.tree.sync()
        LOGGER.info("Synchronized %d application commands", len(synced))
        await self.sync_experimental_commands_for_enabled_guilds()

    def _admin_cog(self) -> commands.Cog:
        from .cogs.admin import AdminCog

        return AdminCog(self)

    def _monitor_cog(self) -> commands.Cog:
        from .cogs.monitor import MonitorCog

        return MonitorCog(self)

    def _onboarding_cog(self) -> commands.Cog:
        from .cogs.onboarding import OnboardingCog

        return OnboardingCog(self)

    def _experimental_cog(self) -> commands.Cog:
        from .cogs.experimental import ExperimentalCog

        return ExperimentalCog(self)

    async def sync_experimental_commands(self, guild_id: int, enabled: bool) -> None:
        """Register or remove the experimental-only commands in one guild."""
        cog = self.get_cog("ExperimentalCog")
        if cog is None:  # pragma: no cover - the cog is always loaded
            return
        target = discord.Object(id=guild_id)
        for command in (
            getattr(cog, "snipers"),
            getattr(cog, "applications"),
            getattr(cog, "debug"),
        ):
            existing = self.tree.get_command(command.name, guild=target)
            if enabled and existing is None:
                self.tree.add_command(command, guild=target)
            elif not enabled and existing is not None:
                self.tree.remove_command(command.name, guild=target)
        synced = await self.tree.sync(guild=target)
        LOGGER.info(
            "Synchronized %d experimental commands in guild %s (enabled=%s)",
            len(synced),
            guild_id,
            enabled,
        )

    async def sync_experimental_commands_for_enabled_guilds(self) -> None:
        """Re-register guild-scoped commands after a restart."""
        if self._experimental_commands_synced:
            return
        guild_ids = await self.database.get_experimental_enabled_guild_ids()
        for guild_id in guild_ids:
            try:
                await self.sync_experimental_commands(guild_id, True)
            except discord.HTTPException:
                LOGGER.exception(
                    "Could not synchronize experimental commands in guild %s",
                    guild_id,
                )
        self._experimental_commands_synced = True

    async def on_ready(self) -> None:
        if self.user:
            LOGGER.info("Connected as %s (%s)", self.user, self.user.id)
        if not self._experimental_commands_synced:
            await self.sync_experimental_commands_for_enabled_guilds()


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

    async def setup_hook(self) -> None:
        await self.add_cog(self._admin_cog())
        await self.add_cog(self._monitor_cog())
        await self.add_cog(self._onboarding_cog())
        synced = await self.tree.sync()
        LOGGER.info("Synchronized %d application commands", len(synced))

    def _admin_cog(self) -> commands.Cog:
        from .cogs.admin import AdminCog

        return AdminCog(self)

    def _monitor_cog(self) -> commands.Cog:
        from .cogs.monitor import MonitorCog

        return MonitorCog(self)

    def _onboarding_cog(self) -> commands.Cog:
        from .cogs.onboarding import OnboardingCog

        return OnboardingCog(self)

    async def on_ready(self) -> None:
        if self.user:
            LOGGER.info("Connected as %s (%s)", self.user, self.user.id)


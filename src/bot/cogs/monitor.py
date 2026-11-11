"""Periodic scraping and notification dispatch."""

from __future__ import annotations

import asyncio
import logging
from typing import TYPE_CHECKING

import discord
from discord.ext import commands, tasks

from ...database import GuildMonitorConfig
from ...notifier import send_job
from ...scrapers import ACTIVE_SCRAPERS, BaseScraper, Job

if TYPE_CHECKING:
    from ..client import JobSniperBot

LOGGER = logging.getLogger(__name__)


class MonitorCog(commands.Cog):
    def __init__(self, bot: JobSniperBot) -> None:
        self.bot = bot
        self.database = bot.database
        self.scrapers: tuple[BaseScraper, ...] = ACTIVE_SCRAPERS
        self.monitor_loop.change_interval(minutes=bot.settings.check_interval_minutes)
        self.monitor_loop.start()

    def cog_unload(self) -> None:
        self.monitor_loop.cancel()

    @tasks.loop(minutes=5)
    async def monitor_loop(self) -> None:
        configs = await self.database.get_monitor_configs()
        for config in configs:
            await self._process_guild(config)

    @monitor_loop.before_loop
    async def before_monitor_loop(self) -> None:
        await self.bot.wait_until_ready()

    async def _process_guild(self, config: GuildMonitorConfig) -> None:
        if config.channel_id is None:
            return
        channel = self.bot.get_channel(config.channel_id)
        if channel is None:
            try:
                channel = await self.bot.fetch_channel(config.channel_id)
            except (discord.NotFound, discord.Forbidden, discord.HTTPException):
                LOGGER.warning("Configured channel %s is unavailable", config.channel_id)
                return
        if not isinstance(channel, discord.abc.Messageable):
            LOGGER.warning("Configured channel %s cannot receive messages", config.channel_id)
            return

        results = await asyncio.gather(
            *(scraper.fetch_jobs(config.keywords, config.location) for scraper in self.scrapers),
            return_exceptions=True,
        )
        jobs: dict[str, Job] = {}
        for scraper, result in zip(self.scrapers, results):
            if isinstance(result, BaseException):
                LOGGER.error("Scraper %s failed: %s", scraper.source, result)
                continue
            jobs.update({job.id: job for job in result})
        for job in jobs.values():
            if await self.database.mark_notified(config.guild_id, job.id, job.source):
                if not await send_job(channel, job):
                    await self._remove_notification(config.guild_id, job.id)
                else:
                    await asyncio.sleep(1.5)

    async def _remove_notification(self, guild_id: int, job_id: str) -> None:
        await self.database.remove_notified(guild_id, job_id)

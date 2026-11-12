"""Periodic scraping and notification dispatch."""

from __future__ import annotations

import asyncio
import logging
import re
import unicodedata
from typing import TYPE_CHECKING

import discord
from discord.ext import commands, tasks

from ...database import GuildMonitorConfig
from ...notifier import send_job
from ...scrapers import ACTIVE_SCRAPERS, BaseScraper, Job

if TYPE_CHECKING:
    from ..client import JobSniperBot

LOGGER = logging.getLogger(__name__)

KEYWORD_ALIASES: dict[str, tuple[str, ...]] = {
    "ingeniero de software": ("software engineer",),
    "ingeniera de software": ("software engineer",),
    "frontend": ("front-end", "front end"),
    "backend": ("back-end", "back end"),
    "devops": ("dev-ops", "dev ops"),
    "informatica": ("informática",),
}


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

    @staticmethod
    def _normalize(value: str) -> str:
        normalized = unicodedata.normalize("NFKD", value.casefold())
        without_accents = "".join(
            character for character in normalized
            if not unicodedata.combining(character)
        )
        return " ".join(without_accents.split())

    @classmethod
    def _keyword_variants(cls, keyword: str) -> tuple[str, ...]:
        term = cls._normalize(keyword)
        if not term:
            return ()
        aliases = KEYWORD_ALIASES.get(term, ())
        return (term, *aliases)

    @classmethod
    def _contains_term(cls, text: str, term: str) -> bool:
        normalized_text = cls._normalize(text)
        normalized_term = cls._normalize(term)
        return re.search(rf"(?<!\w){re.escape(normalized_term)}(?!\w)", normalized_text) is not None

    @classmethod
    def _matches_keyword(cls, job: Job, keyword: str) -> bool:
        variants = cls._keyword_variants(keyword)
        if not variants:
            return False
        title = job.title
        description = job.description
        if any(cls._contains_term(title, variant) for variant in variants):
            return True

        for variant in variants:
            words = variant.split()
            if len(words) > 1:
                title_words = set(cls._normalize(title).split())
                overlap = sum(word in title_words for word in words) / len(words)
                if overlap >= 0.5:
                    return True
            elif cls._contains_term(description, variant):
                return True
        return False

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
            *(
                scraper.fetch_jobs(config.keywords, config.location, config.max_age_hours)
                for scraper in self.scrapers
            ),
            return_exceptions=True,
        )
        jobs: dict[str, Job] = {}
        for scraper, result in zip(self.scrapers, results):
            if isinstance(result, BaseException):
                LOGGER.error("Scraper %s failed: %s", scraper.source, result)
                continue
            jobs.update({job.id: job for job in result})
        for job in jobs.values():
            if not any(self._matches_keyword(job, keyword) for keyword in config.keywords):
                LOGGER.info(
                    "Skipping non-matching job %s for guild %s",
                    job.title,
                    config.guild_id,
                )
                continue
            if any(
                blocked.casefold() in job.company.casefold()
                for blocked in config.blacklisted_companies
            ):
                LOGGER.info(
                    "Skipping blacklisted company %s for guild %s",
                    job.company,
                    config.guild_id,
                )
                continue
            if await self.database.mark_notified(config.guild_id, job.id, job.source):
                if not await send_job(channel, job):
                    await self._remove_notification(config.guild_id, job.id)
                else:
                    await asyncio.sleep(1.5)

    async def _remove_notification(self, guild_id: int, job_id: str) -> None:
        await self.database.remove_notified(guild_id, job_id)

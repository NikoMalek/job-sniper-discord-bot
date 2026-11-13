"""Periodic scraping and notification dispatch."""

from __future__ import annotations

import asyncio
import logging
import re
import time
import unicodedata
from typing import TYPE_CHECKING

import discord
from discord.ext import commands, tasks

from ...database import NOTIFIED_RETENTION_HOURS, GuildMonitorConfig
from ...dedupe import group_jobs
from ...notifier import send_jobs
from ...scrapers import SCRAPERS, Job, get_scraper
from lingua import Language, LanguageDetectorBuilder

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
LANGUAGE_MAP = {
    "spanish": Language.SPANISH,
    "english": Language.ENGLISH,
    "portuguese": Language.PORTUGUESE,
    "french": Language.FRENCH,
    "german": Language.GERMAN,
}
LANGUAGE_DETECTOR = LanguageDetectorBuilder.from_languages(*LANGUAGE_MAP.values()).build()


class MonitorCog(commands.Cog):
    def __init__(self, bot: JobSniperBot) -> None:
        self.bot = bot
        self.database = bot.database
        self.scrapers = SCRAPERS
        self.monitor_loop.change_interval(minutes=bot.settings.check_interval_minutes)
        self.monitor_loop.start()

    def cog_unload(self) -> None:
        self.monitor_loop.cancel()

    @tasks.loop(minutes=5)
    async def monitor_loop(self) -> None:
        started = time.monotonic()
        for scraper in self.scrapers.values():
            scraper.begin_cycle()
        configs = await self.database.get_monitor_configs()
        LOGGER.info(
            "Cycle start: guilds=%d available_portals=%s",
            len(configs),
            ", ".join(self.scrapers) or "none",
        )
        for config in configs:
            try:
                await self._process_guild(config)
            except Exception:
                LOGGER.exception("Guild %s failed during this cycle", config.guild_id)
        removed = await self.database.cleanup_notified_jobs()
        if removed:
            LOGGER.info(
                "Pruned %d notification records older than %.0f hours",
                removed,
                NOTIFIED_RETENTION_HOURS,
            )
        LOGGER.info("Cycle finished in %.1fs", time.monotonic() - started)

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

    @classmethod
    def _matches_language(cls, job: Job, languages: list[str]) -> bool:
        if not languages:
            return True
        allowed = {LANGUAGE_MAP[language] for language in languages if language in LANGUAGE_MAP}
        if not allowed:
            return True

        title = job.title.strip()
        if len(title) >= 20:
            detected_title = LANGUAGE_DETECTOR.detect_language_of(title)
            if detected_title in allowed:
                return True

        fragments = re.split(r"(?<=[.!?])\s+|\n{1,}", job.description)
        detected_fragments = 0
        for fragment in fragments:
            clean_fragment = " ".join(fragment.split()).strip()
            if len(clean_fragment) < 30:
                continue
            detected = LANGUAGE_DETECTOR.detect_language_of(clean_fragment)
            if detected is None:
                continue
            detected_fragments += 1
            if detected in allowed:
                return True

        return detected_fragments == 0

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

        portals = []
        for slug in config.portals:
            scraper = get_scraper(slug)
            if scraper is None:
                LOGGER.warning(
                    "Guild %s has unknown portal %r; skipping it",
                    config.guild_id,
                    slug,
                )
                continue
            portals.append(scraper)
        if not portals:
            LOGGER.warning(
                "Guild %s has no available portals; skipping cycle",
                config.guild_id,
            )
            return

        notified_keys = await self.database.get_notified_keys(config.guild_id)
        known_by_source: dict[str, set[str]] = {}
        for source, job_id in notified_keys:
            known_by_source.setdefault(source, set()).add(job_id)

        LOGGER.info(
            "Guild %s: checking portals=%s keywords=%d max_age=%.1fh",
            config.guild_id,
            ", ".join(scraper.source for scraper in portals),
            len(config.keywords),
            config.max_age_hours,
        )
        results = await asyncio.gather(
            *(
                scraper.fetch_jobs(
                    config.keywords,
                    config.location,
                    config.max_age_hours,
                    known_job_ids=known_by_source.get(scraper.source, set()),
                )
                for scraper in portals
            ),
            return_exceptions=True,
        )

        jobs: dict[tuple[str, str], Job] = {}
        scraped_by_source: dict[str, int] = {}
        for scraper, result in zip(portals, results):
            if isinstance(result, BaseException):
                LOGGER.error(
                    "Portal %s failed for guild %s: %s",
                    scraper.source,
                    config.guild_id,
                    result,
                )
                continue
            scraped_by_source[scraper.source] = len(result)
            LOGGER.info(
                "Portal %s returned %d jobs for guild %s",
                scraper.source,
                len(result),
                config.guild_id,
            )
            for job in result:
                jobs[(job.source, job.id)] = job

        groups = group_jobs(list(jobs.values()))
        counters = {
            "sent_messages": 0,
            "sent_variants": 0,
            "duplicate_groups": 0,
            "grouped_jobs": 0,
            "already_seen": 0,
            "language_skips": 0,
            "keyword_skips": 0,
            "blacklist_skips": 0,
            "send_failures": 0,
        }
        sent_by_source: dict[str, int] = {}
        for group in groups:
            group_label = ", ".join(f"{job.source}:{job.id}" for job in group)
            if len(group) > 1:
                counters["duplicate_groups"] += 1
                counters["grouped_jobs"] += len(group)
                LOGGER.info(
                    "Duplicate group detected for guild %s: %s",
                    config.guild_id,
                    group_label,
                )

            blocked_job = next(
                (
                    job
                    for job in group
                    if any(
                        blocked.casefold() in job.company.casefold()
                        for blocked in config.blacklisted_companies
                    )
                ),
                None,
            )
            if blocked_job is not None:
                counters["blacklist_skips"] += len(group)
                LOGGER.info(
                    "Skipping blacklisted company %s for job group [%s] in guild %s",
                    blocked_job.company,
                    group_label,
                    config.guild_id,
                )
                continue
            if not any(
                self._matches_language(job, config.languages) for job in group
            ):
                counters["language_skips"] += len(group)
                LOGGER.info(
                    "Skipping language-mismatched job group [%s] (%r) for guild %s",
                    group_label,
                    group[0].title,
                    config.guild_id,
                )
                continue
            if not any(
                self._matches_keyword(job, keyword)
                for job in group
                for keyword in config.keywords
            ):
                counters["keyword_skips"] += len(group)
                LOGGER.info(
                    "Skipping non-matching job group [%s] (%r) for guild %s",
                    group_label,
                    group[0].title,
                    config.guild_id,
                )
                continue

            fresh: list[Job] = []
            for job in group:
                if await self.database.mark_notified(
                    config.guild_id, job.id, job.source
                ):
                    fresh.append(job)
                else:
                    counters["already_seen"] += 1
            if not fresh:
                continue

            if await send_jobs(channel, fresh):
                counters["sent_messages"] += 1
                counters["sent_variants"] += len(fresh)
                for job in fresh:
                    sent_by_source[job.source] = (
                        sent_by_source.get(job.source, 0) + 1
                    )
                LOGGER.info(
                    "Sent job group [%s] (%r) at %r to guild %s",
                    ", ".join(f"{job.source}:{job.id}" for job in fresh),
                    fresh[0].title,
                    fresh[0].company,
                    config.guild_id,
                )
                await asyncio.sleep(1.5)
            else:
                counters["send_failures"] += len(fresh)
                for job in fresh:
                    await self._remove_notification(
                        config.guild_id, job.id, job.source
                    )

        LOGGER.info(
            "Guild %s finished: portals_scraped=%s sent_by_source=%s unique=%d "
            "sent_messages=%d sent_variants=%d duplicate_groups=%d grouped_jobs=%d "
            "already_seen=%d language_skips=%d keyword_skips=%d blacklist_skips=%d "
            "send_failures=%d",
            config.guild_id,
            ", ".join(
                f"{source}:{count}" for source, count in scraped_by_source.items()
            )
            or "none",
            ", ".join(
                f"{source}:{count}" for source, count in sent_by_source.items()
            )
            or "none",
            len(jobs),
            counters["sent_messages"],
            counters["sent_variants"],
            counters["duplicate_groups"],
            counters["grouped_jobs"],
            counters["already_seen"],
            counters["language_skips"],
            counters["keyword_skips"],
            counters["blacklist_skips"],
            counters["send_failures"],
        )

    async def _remove_notification(
        self, guild_id: int, job_id: str, source: str
    ) -> None:
        await self.database.remove_notified(guild_id, job_id, source)

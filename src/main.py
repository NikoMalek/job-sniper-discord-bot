"""Process entry point for the Railway worker."""

from __future__ import annotations

import asyncio
import logging
import os
import sys

from .bot.client import JobSniperBot
from .config import Settings
from .database import Database

LOGGER = logging.getLogger(__name__)


class _BelowWarningFilter(logging.Filter):
    """Keep warning/error records out of stdout so Railway does not paint them red."""

    def filter(self, record: logging.LogRecord) -> bool:
        return record.levelno < logging.WARNING


def _configure_logging() -> None:
    raw_level = os.getenv("LOG_LEVEL", "INFO").strip().upper()
    level = getattr(logging, raw_level, logging.INFO)
    formatter = logging.Formatter(
        "%(asctime)s %(levelname)-8s %(name)s: %(message)s",
        datefmt="%Y-%m-%d %H:%M:%S",
    )
    stdout_handler = logging.StreamHandler(sys.stdout)
    stdout_handler.setLevel(logging.DEBUG)
    stdout_handler.addFilter(_BelowWarningFilter())
    stderr_handler = logging.StreamHandler(sys.stderr)
    stderr_handler.setLevel(logging.WARNING)
    for handler in (stdout_handler, stderr_handler):
        handler.setFormatter(formatter)
    logging.basicConfig(level=level, handlers=[stdout_handler, stderr_handler], force=True)


async def main() -> None:
    settings = Settings.from_env()
    LOGGER.info(
        "Starting Job Sniper: database=%s interval=%g min",
        settings.db_path,
        settings.check_interval_minutes,
    )
    database = Database(settings.db_path)
    await database.initialize()
    bot = JobSniperBot(settings, database)
    try:
        await bot.start(settings.discord_bot_token)
    finally:
        await bot.close()


if __name__ == "__main__":
    _configure_logging()
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        LOGGER.info("Shutdown requested.")

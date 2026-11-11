"""Process entry point for the Railway worker."""

from __future__ import annotations

import asyncio
import logging

from .bot.client import JobSniperBot
from .config import Settings
from .database import Database

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(name)s: %(message)s",
)


async def main() -> None:
    settings = Settings.from_env()
    database = Database(settings.db_path)
    await database.initialize()
    bot = JobSniperBot(settings, database)
    try:
        await bot.start(settings.discord_bot_token)
    finally:
        await bot.close()


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        logging.getLogger(__name__).info("Shutdown requested.")


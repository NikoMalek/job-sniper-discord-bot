"""Asynchronous SQLite persistence for the bot."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Any

import aiosqlite


@dataclass(frozen=True, slots=True)
class GuildMonitorConfig:
    guild_id: int
    channel_id: int | None
    location: str
    keywords: list[str]
    blacklisted_companies: list[str]
    max_age_hours: float


class Database:
    """Small repository around the SQLite schema."""

    def __init__(self, path: Path) -> None:
        self.path = path

    async def initialize(self) -> None:
        self.path.parent.mkdir(parents=True, exist_ok=True)
        async with aiosqlite.connect(self.path) as connection:
            await connection.executescript(
                """
                PRAGMA foreign_keys = ON;

                CREATE TABLE IF NOT EXISTS guild_configs (
                    guild_id INTEGER PRIMARY KEY,
                    channel_id INTEGER,
                    location TEXT NOT NULL DEFAULT 'Worldwide',
                    max_age_hours REAL NOT NULL DEFAULT 24.0
                );

                CREATE TABLE IF NOT EXISTS guild_keywords (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    guild_id INTEGER NOT NULL,
                    keyword TEXT NOT NULL,
                    UNIQUE (guild_id, keyword),
                    FOREIGN KEY (guild_id) REFERENCES guild_configs(guild_id)
                        ON DELETE CASCADE
                );

                CREATE TABLE IF NOT EXISTS guild_blacklisted_companies (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    guild_id INTEGER NOT NULL,
                    company TEXT NOT NULL,
                    UNIQUE (guild_id, company),
                    FOREIGN KEY (guild_id) REFERENCES guild_configs(guild_id)
                        ON DELETE CASCADE
                );

                CREATE TABLE IF NOT EXISTS notified_jobs (
                    guild_id INTEGER NOT NULL,
                    job_id TEXT NOT NULL,
                    source TEXT NOT NULL,
                    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
                    PRIMARY KEY (guild_id, job_id),
                    FOREIGN KEY (guild_id) REFERENCES guild_configs(guild_id)
                        ON DELETE CASCADE
                );
                """
            )
            columns_cursor = await connection.execute("PRAGMA table_info(guild_configs)")
            columns = {row[1] for row in await columns_cursor.fetchall()}
            if "max_age_hours" not in columns:
                await connection.execute(
                    "ALTER TABLE guild_configs ADD COLUMN max_age_hours REAL NOT NULL DEFAULT 24.0"
                )
            await connection.commit()

    async def _ensure_guild(self, guild_id: int) -> None:
        async with aiosqlite.connect(self.path) as connection:
            await connection.execute(
                "INSERT OR IGNORE INTO guild_configs (guild_id) VALUES (?)",
                (guild_id,),
            )
            await connection.commit()

    async def set_channel(self, guild_id: int, channel_id: int) -> None:
        await self._ensure_guild(guild_id)
        async with aiosqlite.connect(self.path) as connection:
            await connection.execute(
                "UPDATE guild_configs SET channel_id = ? WHERE guild_id = ?",
                (channel_id, guild_id),
            )
            await connection.commit()

    async def set_location(self, guild_id: int, location: str) -> None:
        await self._ensure_guild(guild_id)
        async with aiosqlite.connect(self.path) as connection:
            await connection.execute(
                "UPDATE guild_configs SET location = ? WHERE guild_id = ?",
                (location, guild_id),
            )
            await connection.commit()

    async def set_max_age_hours(self, guild_id: int, max_age_hours: float) -> None:
        await self._ensure_guild(guild_id)
        async with aiosqlite.connect(self.path) as connection:
            await connection.execute(
                "UPDATE guild_configs SET max_age_hours = ? WHERE guild_id = ?",
                (max_age_hours, guild_id),
            )
            await connection.commit()

    async def add_keyword(self, guild_id: int, keyword: str) -> bool:
        await self._ensure_guild(guild_id)
        async with aiosqlite.connect(self.path) as connection:
            cursor = await connection.execute(
                "INSERT OR IGNORE INTO guild_keywords (guild_id, keyword) VALUES (?, ?)",
                (guild_id, keyword),
            )
            await connection.commit()
            return cursor.rowcount == 1

    async def remove_keyword(self, guild_id: int, keyword: str) -> bool:
        async with aiosqlite.connect(self.path) as connection:
            cursor = await connection.execute(
                "DELETE FROM guild_keywords WHERE guild_id = ? AND keyword = ?",
                (guild_id, keyword),
            )
            await connection.commit()
            return cursor.rowcount == 1

    async def add_blacklisted_company(self, guild_id: int, company: str) -> bool:
        await self._ensure_guild(guild_id)
        async with aiosqlite.connect(self.path) as connection:
            cursor = await connection.execute(
                """
                SELECT 1 FROM guild_blacklisted_companies
                WHERE guild_id = ? AND lower(company) = lower(?)
                """,
                (guild_id, company),
            )
            if await cursor.fetchone():
                return False
            await connection.execute(
                """
                INSERT INTO guild_blacklisted_companies (guild_id, company)
                VALUES (?, ?)
                """,
                (guild_id, company),
            )
            await connection.commit()
        return True

    async def remove_blacklisted_company(self, guild_id: int, company: str) -> bool:
        async with aiosqlite.connect(self.path) as connection:
            cursor = await connection.execute(
                """
                DELETE FROM guild_blacklisted_companies
                WHERE guild_id = ? AND lower(company) = lower(?)
                """,
                (guild_id, company),
            )
            await connection.commit()
            return cursor.rowcount == 1

    async def get_guild_config(self, guild_id: int) -> GuildMonitorConfig:
        await self._ensure_guild(guild_id)
        async with aiosqlite.connect(self.path) as connection:
            connection.row_factory = aiosqlite.Row
            config_cursor = await connection.execute(
                """
                SELECT guild_id, channel_id, location, max_age_hours
                FROM guild_configs
                WHERE guild_id = ?
                """,
                (guild_id,),
            )
            config = await config_cursor.fetchone()
            keyword_cursor = await connection.execute(
                "SELECT keyword FROM guild_keywords WHERE guild_id = ? ORDER BY keyword",
                (guild_id,),
            )
            keywords = [row["keyword"] for row in await keyword_cursor.fetchall()]
            blacklist_cursor = await connection.execute(
                """
                SELECT company FROM guild_blacklisted_companies
                WHERE guild_id = ? ORDER BY company
                """,
                (guild_id,),
            )
            blacklisted_companies = [
                row["company"] for row in await blacklist_cursor.fetchall()
            ]
        if config is None:
            raise RuntimeError(f"Guild configuration was not created: {guild_id}")
        return GuildMonitorConfig(
            guild_id=config["guild_id"],
            channel_id=config["channel_id"],
            location=config["location"],
            keywords=keywords,
            blacklisted_companies=blacklisted_companies,
            max_age_hours=float(config["max_age_hours"]),
        )

    async def get_monitor_configs(self) -> list[GuildMonitorConfig]:
        async with aiosqlite.connect(self.path) as connection:
            connection.row_factory = aiosqlite.Row
            cursor = await connection.execute(
                """
                SELECT guild_id, channel_id, location, max_age_hours
                FROM guild_configs
                WHERE channel_id IS NOT NULL
                ORDER BY guild_id
                """
            )
            configs = await cursor.fetchall()
            result: list[GuildMonitorConfig] = []
            for config in configs:
                keyword_cursor = await connection.execute(
                    "SELECT keyword FROM guild_keywords WHERE guild_id = ? ORDER BY keyword",
                    (config["guild_id"],),
                )
                keywords = [row["keyword"] for row in await keyword_cursor.fetchall()]
                blacklist_cursor = await connection.execute(
                    """
                    SELECT company FROM guild_blacklisted_companies
                    WHERE guild_id = ? ORDER BY company
                    """,
                    (config["guild_id"],),
                )
                blacklisted_companies = [
                    row["company"] for row in await blacklist_cursor.fetchall()
                ]
                if keywords:
                    result.append(
                        GuildMonitorConfig(
                            guild_id=config["guild_id"],
                            channel_id=config["channel_id"],
                            location=config["location"],
                            keywords=keywords,
                            blacklisted_companies=blacklisted_companies,
                            max_age_hours=float(config["max_age_hours"]),
                        )
                    )
        return result

    async def mark_notified(self, guild_id: int, job_id: str, source: str) -> bool:
        async with aiosqlite.connect(self.path) as connection:
            cursor = await connection.execute(
                """
                INSERT OR IGNORE INTO notified_jobs (guild_id, job_id, source)
                VALUES (?, ?, ?)
                """,
                (guild_id, job_id, source),
            )
            await connection.commit()
            return cursor.rowcount == 1

    async def remove_notified(self, guild_id: int, job_id: str) -> None:
        async with aiosqlite.connect(self.path) as connection:
            await connection.execute(
                "DELETE FROM notified_jobs WHERE guild_id = ? AND job_id = ?",
                (guild_id, job_id),
            )
            await connection.commit()

    async def count_notified(self, guild_id: int) -> int:
        async with aiosqlite.connect(self.path) as connection:
            cursor = await connection.execute(
                "SELECT COUNT(*) FROM notified_jobs WHERE guild_id = ?",
                (guild_id,),
            )
            row: Any = await cursor.fetchone()
        return int(row[0])

"""Asynchronous SQLite persistence for the bot."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Any

import aiosqlite

MIN_MAX_AGE_HOURS = 10 / 60
MAX_MAX_AGE_HOURS = 24.0
NOTIFIED_RETENTION_HOURS = MAX_MAX_AGE_HOURS + 1.0


@dataclass(frozen=True, slots=True)
class GuildMonitorConfig:
    guild_id: int
    channel_id: int | None
    location: str
    keywords: list[str]
    blacklisted_companies: list[str]
    languages: list[str]
    max_age_hours: float
    portals: tuple[str, ...]


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
                    max_age_hours REAL NOT NULL DEFAULT 24.0,
                    setup_message_sent INTEGER NOT NULL DEFAULT 0
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

                CREATE TABLE IF NOT EXISTS guild_languages (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    guild_id INTEGER NOT NULL,
                    language TEXT NOT NULL,
                    UNIQUE (guild_id, language),
                    FOREIGN KEY (guild_id) REFERENCES guild_configs(guild_id)
                        ON DELETE CASCADE
                );

                CREATE TABLE IF NOT EXISTS guild_portals (
                    guild_id INTEGER NOT NULL,
                    source TEXT NOT NULL,
                    UNIQUE (guild_id, source),
                    FOREIGN KEY (guild_id) REFERENCES guild_configs(guild_id)
                        ON DELETE CASCADE
                );

                CREATE TABLE IF NOT EXISTS notified_jobs (
                    guild_id INTEGER NOT NULL,
                    job_id TEXT NOT NULL,
                    source TEXT NOT NULL,
                    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
                    PRIMARY KEY (guild_id, source, job_id),
                    FOREIGN KEY (guild_id) REFERENCES guild_configs(guild_id)
                        ON DELETE CASCADE
                );

                CREATE INDEX IF NOT EXISTS idx_notified_jobs_created_at
                    ON notified_jobs(created_at);
                """
            )
            columns_cursor = await connection.execute("PRAGMA table_info(guild_configs)")
            columns = {row[1] for row in await columns_cursor.fetchall()}
            if "max_age_hours" not in columns:
                await connection.execute(
                    "ALTER TABLE guild_configs ADD COLUMN max_age_hours REAL NOT NULL DEFAULT 24.0"
                )
            if "setup_message_sent" not in columns:
                await connection.execute(
                    "ALTER TABLE guild_configs ADD COLUMN setup_message_sent INTEGER NOT NULL DEFAULT 0"
                )
            await connection.execute(
                "UPDATE guild_configs SET max_age_hours = ? WHERE max_age_hours > ?",
                (MAX_MAX_AGE_HOURS, MAX_MAX_AGE_HOURS),
            )
            await connection.execute(
                "UPDATE guild_configs SET max_age_hours = ? WHERE max_age_hours < ?",
                (MIN_MAX_AGE_HOURS, MIN_MAX_AGE_HOURS),
            )

            # Old databases keyed notified jobs by (guild_id, job_id), which can
            # collide when two portals reuse the same id. Rebuild the table with
            # source in the primary key, preserving existing rows.
            primary_key = await self._primary_key(connection, "notified_jobs")
            if primary_key and primary_key != ["guild_id", "source", "job_id"]:
                await connection.executescript(
                    """
                    ALTER TABLE notified_jobs RENAME TO notified_jobs_legacy;
                    CREATE TABLE notified_jobs (
                        guild_id INTEGER NOT NULL,
                        job_id TEXT NOT NULL,
                        source TEXT NOT NULL,
                        created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
                        PRIMARY KEY (guild_id, source, job_id),
                        FOREIGN KEY (guild_id) REFERENCES guild_configs(guild_id)
                            ON DELETE CASCADE
                    );
                    INSERT OR IGNORE INTO notified_jobs
                        (guild_id, job_id, source, created_at)
                    SELECT guild_id, job_id, source, created_at
                    FROM notified_jobs_legacy;
                    DROP TABLE notified_jobs_legacy;
                    CREATE INDEX IF NOT EXISTS idx_notified_jobs_created_at
                        ON notified_jobs(created_at);
                    """
                )

            # Every existing guild keeps LinkedIn enabled unless it already has
            # a portal selection.
            await connection.execute(
                """
                INSERT OR IGNORE INTO guild_portals (guild_id, source)
                SELECT guild_id, 'linkedin' FROM guild_configs
                WHERE guild_id NOT IN (SELECT guild_id FROM guild_portals)
                """
            )
            await connection.commit()

    @staticmethod
    async def _primary_key(connection: aiosqlite.Connection, table: str) -> list[str]:
        cursor = await connection.execute(f"PRAGMA index_list({table})")
        for row in await cursor.fetchall():
            name, unique, origin = row[1], row[2], row[3]
            if origin != "pk" and not (unique and str(name).startswith("sqlite_autoindex_")):
                continue
            index_cursor = await connection.execute(f"PRAGMA index_info({name})")
            return [column[2] for column in await index_cursor.fetchall()]
        return []

    async def _ensure_guild(self, guild_id: int) -> None:
        async with aiosqlite.connect(self.path) as connection:
            await connection.execute(
                "INSERT OR IGNORE INTO guild_configs (guild_id) VALUES (?)",
                (guild_id,),
            )
            await connection.execute(
                """
                INSERT OR IGNORE INTO guild_portals (guild_id, source)
                SELECT ?, 'linkedin'
                WHERE NOT EXISTS (
                    SELECT 1 FROM guild_portals WHERE guild_id = ?
                )
                """,
                (guild_id, guild_id),
            )
            await connection.commit()

    async def has_setup_message(self, guild_id: int) -> bool:
        await self._ensure_guild(guild_id)
        async with aiosqlite.connect(self.path) as connection:
            cursor = await connection.execute(
                "SELECT setup_message_sent FROM guild_configs WHERE guild_id = ?",
                (guild_id,),
            )
            row = await cursor.fetchone()
        return bool(row and row[0])

    async def mark_setup_message_sent(self, guild_id: int) -> None:
        await self._ensure_guild(guild_id)
        async with aiosqlite.connect(self.path) as connection:
            await connection.execute(
                "UPDATE guild_configs SET setup_message_sent = 1 WHERE guild_id = ?",
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

    async def add_language(self, guild_id: int, language: str) -> bool:
        await self._ensure_guild(guild_id)
        async with aiosqlite.connect(self.path) as connection:
            cursor = await connection.execute(
                """
                INSERT OR IGNORE INTO guild_languages (guild_id, language)
                VALUES (?, ?)
                """,
                (guild_id, language),
            )
            await connection.commit()
            return cursor.rowcount == 1

    async def remove_language(self, guild_id: int, language: str) -> bool:
        async with aiosqlite.connect(self.path) as connection:
            cursor = await connection.execute(
                "DELETE FROM guild_languages WHERE guild_id = ? AND language = ?",
                (guild_id, language),
            )
            await connection.commit()
            return cursor.rowcount == 1

    async def get_portals(self, guild_id: int) -> list[str]:
        async with aiosqlite.connect(self.path) as connection:
            cursor = await connection.execute(
                "SELECT source FROM guild_portals WHERE guild_id = ? ORDER BY source",
                (guild_id,),
            )
            return [row[0] for row in await cursor.fetchall()]

    async def add_portal(self, guild_id: int, source: str) -> bool:
        await self._ensure_guild(guild_id)
        async with aiosqlite.connect(self.path) as connection:
            cursor = await connection.execute(
                "INSERT OR IGNORE INTO guild_portals (guild_id, source) VALUES (?, ?)",
                (guild_id, source),
            )
            await connection.commit()
            return cursor.rowcount == 1

    async def remove_portal(self, guild_id: int, source: str) -> bool:
        async with aiosqlite.connect(self.path) as connection:
            cursor = await connection.execute(
                "DELETE FROM guild_portals WHERE guild_id = ? AND source = ?",
                (guild_id, source),
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
            language_cursor = await connection.execute(
                "SELECT language FROM guild_languages WHERE guild_id = ? ORDER BY language",
                (guild_id,),
            )
            languages = [row["language"] for row in await language_cursor.fetchall()]
            portal_cursor = await connection.execute(
                "SELECT source FROM guild_portals WHERE guild_id = ? ORDER BY source",
                (guild_id,),
            )
            portals = tuple(row["source"] for row in await portal_cursor.fetchall())
        if config is None:
            raise RuntimeError(f"Guild configuration was not created: {guild_id}")
        return GuildMonitorConfig(
            guild_id=config["guild_id"],
            channel_id=config["channel_id"],
            location=config["location"],
            keywords=keywords,
            blacklisted_companies=blacklisted_companies,
            languages=languages,
            max_age_hours=float(config["max_age_hours"]),
            portals=portals,
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
                language_cursor = await connection.execute(
                    "SELECT language FROM guild_languages WHERE guild_id = ? ORDER BY language",
                    (config["guild_id"],),
                )
                languages = [row["language"] for row in await language_cursor.fetchall()]
                portal_cursor = await connection.execute(
                    "SELECT source FROM guild_portals WHERE guild_id = ? ORDER BY source",
                    (config["guild_id"],),
                )
                portals = tuple(row["source"] for row in await portal_cursor.fetchall())
                if keywords:
                    result.append(
                        GuildMonitorConfig(
                            guild_id=config["guild_id"],
                            channel_id=config["channel_id"],
                            location=config["location"],
                            keywords=keywords,
                            blacklisted_companies=blacklisted_companies,
                            languages=languages,
                            max_age_hours=float(config["max_age_hours"]),
                            portals=portals,
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

    async def remove_notified(self, guild_id: int, job_id: str, source: str) -> None:
        async with aiosqlite.connect(self.path) as connection:
            await connection.execute(
                """
                DELETE FROM notified_jobs
                WHERE guild_id = ? AND job_id = ? AND source = ?
                """,
                (guild_id, job_id, source),
            )
            await connection.commit()

    async def get_notified_keys(self, guild_id: int) -> set[tuple[str, str]]:
        """Return ``(source, job_id)`` pairs already notified for a guild."""
        async with aiosqlite.connect(self.path) as connection:
            cursor = await connection.execute(
                "SELECT source, job_id FROM notified_jobs WHERE guild_id = ?",
                (guild_id,),
            )
            return {(row[0], row[1]) for row in await cursor.fetchall()}

    async def cleanup_notified_jobs(
        self, hours: float = NOTIFIED_RETENTION_HOURS
    ) -> int:
        """Delete notification history older than ``hours`` and return how many."""
        async with aiosqlite.connect(self.path) as connection:
            cursor = await connection.execute(
                "DELETE FROM notified_jobs WHERE created_at < datetime('now', ?)",
                (f"-{float(hours):g} hours",),
            )
            await connection.commit()
            return cursor.rowcount

    async def count_notified(self, guild_id: int) -> int:
        async with aiosqlite.connect(self.path) as connection:
            cursor = await connection.execute(
                "SELECT COUNT(*) FROM notified_jobs WHERE guild_id = ?",
                (guild_id,),
            )
            row: Any = await cursor.fetchone()
        return int(row[0])

"""Asynchronous SQLite persistence for the bot."""

from __future__ import annotations

import secrets
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

import aiosqlite

MIN_MAX_AGE_HOURS = 10 / 60
MAX_MAX_AGE_HOURS = 24.0
NOTIFIED_RETENTION_HOURS = MAX_MAX_AGE_HOURS + 1.0
DEFAULT_LOCATION = "Worldwide"


@dataclass(frozen=True, slots=True)
class GuildMonitorConfig:
    guild_id: int
    channel_id: int | None
    locations: tuple[str, ...]
    keywords: list[str]
    blacklisted_companies: list[str]
    languages: list[str]
    max_age_hours: float
    portals: tuple[str, ...]

    @property
    def location(self) -> str:
        """First configured location, or the implicit default."""
        return self.locations[0] if self.locations else DEFAULT_LOCATION


@dataclass(frozen=True, slots=True)
class TrackedJob:
    """Metadata needed when a tracked notification button is pressed."""

    token: str
    guild_id: int
    job_id: str
    source: str
    job_url: str
    job_title: str
    company: str


@dataclass(frozen=True, slots=True)
class JobInteraction:
    """A click on a job offer plus its optional application answer."""

    id: int
    guild_id: int
    user_id: int
    job_url: str
    job_title: str
    company: str
    source: str
    clicked_at: str
    applied: bool | None
    notes: str | None = None


@dataclass(frozen=True, slots=True)
class ApplicationRecord:
    """Recent application row shown by `/applications`."""

    job_title: str
    company: str
    job_url: str
    source: str
    clicked_at: str
    applied: bool | None
    notes: str | None = None


@dataclass(frozen=True, slots=True)
class ApplicationSummary:
    """Overall counters shown at the top of `/applications`."""

    clicked: int
    applied: int
    skipped: int
    viewed: int


@dataclass(frozen=True, slots=True)
class DebugReport:
    """A guild report with an optional note left in the tracking modal."""

    user_id: int
    job_title: str
    job_url: str
    source: str
    clicked_at: str
    applied: bool | None
    notes: str


@dataclass(frozen=True, slots=True)
class SniperRanking:
    """Per-user totals used by `/snipers`."""

    user_id: int
    applications: int
    viewed: int


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
                    location TEXT NOT NULL DEFAULT '',
                    max_age_hours REAL NOT NULL DEFAULT 24.0,
                    setup_message_sent INTEGER NOT NULL DEFAULT 0,
                    experimental_enabled INTEGER NOT NULL DEFAULT 0
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

                CREATE TABLE IF NOT EXISTS guild_locations (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    guild_id INTEGER NOT NULL,
                    position INTEGER NOT NULL,
                    location TEXT NOT NULL,
                    UNIQUE (guild_id, location),
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

                CREATE TABLE IF NOT EXISTS tracked_jobs (
                    token TEXT PRIMARY KEY,
                    guild_id INTEGER NOT NULL,
                    job_id TEXT NOT NULL,
                    source TEXT NOT NULL,
                    job_url TEXT NOT NULL,
                    job_title TEXT NOT NULL,
                    company TEXT NOT NULL,
                    created_at TEXT NOT NULL,
                    FOREIGN KEY (guild_id) REFERENCES guild_configs(guild_id)
                        ON DELETE CASCADE
                );

                CREATE TABLE IF NOT EXISTS job_interactions (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    guild_id INTEGER NOT NULL,
                    user_id INTEGER NOT NULL,
                    job_url TEXT NOT NULL,
                    job_title TEXT NOT NULL,
                    company TEXT NOT NULL,
                    source TEXT NOT NULL,
                    clicked_at TEXT NOT NULL,
                    applied INTEGER CHECK (applied IS NULL OR applied IN (0, 1)),
                    notes TEXT,
                    UNIQUE (guild_id, user_id, job_url),
                    FOREIGN KEY (guild_id) REFERENCES guild_configs(guild_id)
                        ON DELETE CASCADE
                );

                CREATE INDEX IF NOT EXISTS idx_job_interactions_guild_clicked
                    ON job_interactions(guild_id, clicked_at DESC);
                CREATE INDEX IF NOT EXISTS idx_job_interactions_user_clicked
                    ON job_interactions(guild_id, user_id, clicked_at DESC);
                CREATE INDEX IF NOT EXISTS idx_tracked_jobs_created_at
                    ON tracked_jobs(created_at);
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
            if "experimental_enabled" not in columns:
                await connection.execute(
                    "ALTER TABLE guild_configs ADD COLUMN experimental_enabled INTEGER NOT NULL DEFAULT 0"
                )
            interaction_columns_cursor = await connection.execute(
                "PRAGMA table_info(job_interactions)"
            )
            interaction_columns = {
                row[1] for row in await interaction_columns_cursor.fetchall()
            }
            if "notes" not in interaction_columns:
                if "observation" in interaction_columns:
                    # Earlier local builds called the same field "observation".
                    await connection.execute(
                        "ALTER TABLE job_interactions RENAME COLUMN observation TO notes"
                    )
                else:
                    await connection.execute(
                        "ALTER TABLE job_interactions ADD COLUMN notes TEXT"
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
            # Migrate the single ``location`` column into the new list table.
            await connection.execute(
                """
                INSERT OR IGNORE INTO guild_locations (guild_id, position, location)
                SELECT guild_id, 0, location FROM guild_configs
                WHERE guild_id NOT IN (SELECT guild_id FROM guild_locations)
                  AND location <> ''
                """
            )
            # Canonical label for "any modality, any country" is "Worldwide";
            # normalize the temporary Spanish label if it was ever stored.
            await connection.execute(
                "UPDATE OR IGNORE guild_configs SET location = 'Worldwide' "
                "WHERE location = 'Todo el mundo'"
            )
            await connection.execute(
                "UPDATE OR IGNORE guild_locations SET location = 'Worldwide' "
                "WHERE location = 'Todo el mundo'"
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

    async def is_experimental_enabled(self, guild_id: int) -> bool:
        await self._ensure_guild(guild_id)
        async with aiosqlite.connect(self.path) as connection:
            cursor = await connection.execute(
                "SELECT experimental_enabled FROM guild_configs WHERE guild_id = ?",
                (guild_id,),
            )
            row = await cursor.fetchone()
        return bool(row and row[0])

    async def set_experimental_enabled(self, guild_id: int, enabled: bool) -> None:
        await self._ensure_guild(guild_id)
        async with aiosqlite.connect(self.path) as connection:
            await connection.execute(
                "UPDATE guild_configs SET experimental_enabled = ? WHERE guild_id = ?",
                (int(enabled), guild_id),
            )
            await connection.commit()

    async def get_experimental_enabled_guild_ids(self) -> list[int]:
        async with aiosqlite.connect(self.path) as connection:
            cursor = await connection.execute(
                """
                SELECT guild_id FROM guild_configs
                WHERE experimental_enabled = 1
                ORDER BY guild_id
                """
            )
            return [int(row[0]) for row in await cursor.fetchall()]

    async def set_channel(self, guild_id: int, channel_id: int) -> None:
        await self._ensure_guild(guild_id)
        async with aiosqlite.connect(self.path) as connection:
            await connection.execute(
                "UPDATE guild_configs SET channel_id = ? WHERE guild_id = ?",
                (channel_id, guild_id),
            )
            await connection.commit()

    async def set_location(self, guild_id: int, location: str) -> None:
        """Replace every configured location with a single one."""
        await self._ensure_guild(guild_id)
        async with aiosqlite.connect(self.path) as connection:
            await connection.execute(
                "UPDATE guild_configs SET location = ? WHERE guild_id = ?",
                (location, guild_id),
            )
            await connection.execute(
                "DELETE FROM guild_locations WHERE guild_id = ?", (guild_id,)
            )
            await connection.execute(
                """
                INSERT INTO guild_locations (guild_id, position, location)
                VALUES (?, 0, ?)
                """,
                (guild_id, location),
            )
            await connection.commit()

    async def get_locations(self, guild_id: int) -> list[str]:
        await self._ensure_guild(guild_id)
        async with aiosqlite.connect(self.path) as connection:
            cursor = await connection.execute(
                """
                SELECT location FROM guild_locations
                WHERE guild_id = ? ORDER BY position, id
                """,
                (guild_id,),
            )
            return [row[0] for row in await cursor.fetchall()]

    async def add_location(self, guild_id: int, location: str) -> bool:
        await self._ensure_guild(guild_id)
        async with aiosqlite.connect(self.path) as connection:
            cursor = await connection.execute(
                """
                SELECT COALESCE(MAX(position), -1) + 1
                FROM guild_locations WHERE guild_id = ?
                """,
                (guild_id,),
            )
            row = await cursor.fetchone()
            position = int(row[0]) if row else 0
            cursor = await connection.execute(
                """
                INSERT OR IGNORE INTO guild_locations
                    (guild_id, position, location)
                VALUES (?, ?, ?)
                """,
                (guild_id, position, location),
            )
            await connection.commit()
            return cursor.rowcount == 1

    async def remove_location(self, guild_id: int, location: str) -> bool:
        async with aiosqlite.connect(self.path) as connection:
            cursor = await connection.execute(
                """
                DELETE FROM guild_locations
                WHERE guild_id = ? AND location = ?
                """,
                (guild_id, location),
            )
            removed = cursor.rowcount == 1
            if removed:
                rows = await (
                    await connection.execute(
                        """
                        SELECT id FROM guild_locations
                        WHERE guild_id = ? ORDER BY position, id
                        """,
                        (guild_id,),
                    )
                ).fetchall()
                for index, row in enumerate(rows):
                    await connection.execute(
                        "UPDATE guild_locations SET position = ? WHERE id = ?",
                        (index, row[0]),
                    )
                first = await (
                    await connection.execute(
                        """
                        SELECT location FROM guild_locations
                        WHERE guild_id = ? ORDER BY position, id LIMIT 1
                        """,
                        (guild_id,),
                    )
                ).fetchone()
                # An empty list is valid: the monitor falls back to
                # DEFAULT_LOCATION without counting it as a saved location.
                await connection.execute(
                    "UPDATE guild_configs SET location = ? WHERE guild_id = ?",
                    (first[0] if first is not None else "", guild_id),
                )
            await connection.commit()
            return removed

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
            location_cursor = await connection.execute(
                """
                SELECT location FROM guild_locations
                WHERE guild_id = ? ORDER BY position, id
                """,
                (guild_id,),
            )
            locations = tuple(row["location"] for row in await location_cursor.fetchall())
        if config is None:
            raise RuntimeError(f"Guild configuration was not created: {guild_id}")
        return GuildMonitorConfig(
            guild_id=config["guild_id"],
            channel_id=config["channel_id"],
            locations=locations,
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
                location_cursor = await connection.execute(
                    """
                    SELECT location FROM guild_locations
                    WHERE guild_id = ? ORDER BY position, id
                    """,
                    (config["guild_id"],),
                )
                locations = tuple(
                    row["location"] for row in await location_cursor.fetchall()
                )
                if keywords:
                    result.append(
                        GuildMonitorConfig(
                            guild_id=config["guild_id"],
                            channel_id=config["channel_id"],
                            locations=locations,
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

    @staticmethod
    def _utc_now() -> str:
        return datetime.now(timezone.utc).isoformat(timespec="seconds")

    async def store_tracked_job(
        self,
        guild_id: int,
        job_id: str,
        source: str,
        job_url: str,
        job_title: str,
        company: str,
    ) -> str:
        """Store the metadata behind a short-lived tracking button token."""
        token = secrets.token_hex(8)
        await self._ensure_guild(guild_id)
        async with aiosqlite.connect(self.path) as connection:
            await connection.execute(
                """
                INSERT INTO tracked_jobs
                    (token, guild_id, job_id, source, job_url, job_title,
                     company, created_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    token,
                    guild_id,
                    job_id,
                    source,
                    job_url,
                    job_title,
                    company,
                    self._utc_now(),
                ),
            )
            await connection.commit()
        return token

    async def get_tracked_job(self, token: str) -> TrackedJob | None:
        async with aiosqlite.connect(self.path) as connection:
            connection.row_factory = aiosqlite.Row
            cursor = await connection.execute(
                "SELECT * FROM tracked_jobs WHERE token = ?", (token,)
            )
            row = await cursor.fetchone()
        if row is None:
            return None
        return TrackedJob(
            token=row["token"],
            guild_id=int(row["guild_id"]),
            job_id=row["job_id"],
            source=row["source"],
            job_url=row["job_url"],
            job_title=row["job_title"],
            company=row["company"],
        )

    async def record_job_click(
        self,
        guild_id: int,
        user_id: int,
        job_url: str,
        job_title: str,
        company: str,
        source: str,
    ) -> int:
        """Register a click and return the interaction row id.

        Repeated clicks on the same offer refresh ``clicked_at`` but keep the
        answer already given by the user.
        """
        await self._ensure_guild(guild_id)
        async with aiosqlite.connect(self.path) as connection:
            await connection.execute(
                """
                INSERT INTO job_interactions
                    (guild_id, user_id, job_url, job_title, company, source,
                     clicked_at)
                VALUES (?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT (guild_id, user_id, job_url) DO UPDATE SET
                    clicked_at = excluded.clicked_at,
                    job_title = excluded.job_title,
                    company = excluded.company,
                    source = excluded.source
                """,
                (
                    guild_id,
                    user_id,
                    job_url,
                    job_title,
                    company,
                    source,
                    self._utc_now(),
                ),
            )
            cursor = await connection.execute(
                """
                SELECT id FROM job_interactions
                WHERE guild_id = ? AND user_id = ? AND job_url = ?
                """,
                (guild_id, user_id, job_url),
            )
            row = await cursor.fetchone()
            await connection.commit()
        if row is None:  # pragma: no cover - the upsert always creates the row
            raise RuntimeError("Job interaction was not created")
        return int(row[0])

    @staticmethod
    def _interaction_from_row(row: aiosqlite.Row) -> JobInteraction:
        applied = row["applied"]
        return JobInteraction(
            id=int(row["id"]),
            guild_id=int(row["guild_id"]),
            user_id=int(row["user_id"]),
            job_url=row["job_url"],
            job_title=row["job_title"],
            company=row["company"],
            source=row["source"],
            clicked_at=row["clicked_at"],
            applied=None if applied is None else bool(applied),
            notes=row["notes"] or None,
        )

    async def get_job_interaction(self, record_id: int) -> JobInteraction | None:
        async with aiosqlite.connect(self.path) as connection:
            connection.row_factory = aiosqlite.Row
            cursor = await connection.execute(
                "SELECT * FROM job_interactions WHERE id = ?", (record_id,)
            )
            row = await cursor.fetchone()
        return None if row is None else self._interaction_from_row(row)

    async def update_application_status(
        self,
        record_id: int,
        applied: bool | None,
        notes: str | None = None,
    ) -> bool:
        """Save the Yes/No answer and the optional user notes."""
        async with aiosqlite.connect(self.path) as connection:
            cursor = await connection.execute(
                "UPDATE job_interactions SET applied = ?, notes = ? WHERE id = ?",
                (None if applied is None else int(applied), notes, record_id),
            )
            await connection.commit()
            return cursor.rowcount == 1

    async def get_guild_snipers_ranking(
        self, guild_id: int, limit: int = 10
    ) -> list[SniperRanking]:
        async with aiosqlite.connect(self.path) as connection:
            connection.row_factory = aiosqlite.Row
            cursor = await connection.execute(
                """
                SELECT user_id,
                       SUM(CASE WHEN applied = 1 THEN 1 ELSE 0 END) AS applications,
                       COUNT(*) AS viewed
                FROM job_interactions
                WHERE guild_id = ?
                GROUP BY user_id
                ORDER BY applications DESC, viewed DESC, user_id
                LIMIT ?
                """,
                (guild_id, max(1, int(limit))),
            )
            rows = await cursor.fetchall()
        return [
            SniperRanking(
                user_id=int(row["user_id"]),
                applications=int(row["applications"]),
                viewed=int(row["viewed"]),
            )
            for row in rows
        ]

    async def get_guild_click_ranking(
        self, guild_id: int, limit: int = 10
    ) -> list[SniperRanking]:
        """Top users by offers opened, independently of confirmed applications."""
        async with aiosqlite.connect(self.path) as connection:
            connection.row_factory = aiosqlite.Row
            cursor = await connection.execute(
                """
                SELECT user_id,
                       SUM(CASE WHEN applied = 1 THEN 1 ELSE 0 END) AS applications,
                       COUNT(*) AS viewed
                FROM job_interactions
                WHERE guild_id = ?
                GROUP BY user_id
                ORDER BY viewed DESC, applications DESC, user_id
                LIMIT ?
                """,
                (guild_id, max(1, int(limit))),
            )
            rows = await cursor.fetchall()
        return [
            SniperRanking(
                user_id=int(row["user_id"]),
                applications=int(row["applications"]),
                viewed=int(row["viewed"]),
            )
            for row in rows
        ]

    async def get_user_application_summary(
        self, guild_id: int, user_id: int
    ) -> ApplicationSummary:
        async with aiosqlite.connect(self.path) as connection:
            cursor = await connection.execute(
                """
                SELECT COUNT(*) AS clicked,
                       COALESCE(SUM(CASE WHEN applied = 1 THEN 1 ELSE 0 END), 0),
                       COALESCE(SUM(CASE WHEN applied = 0 THEN 1 ELSE 0 END), 0),
                       COALESCE(SUM(CASE WHEN applied IS NULL THEN 1 ELSE 0 END), 0)
                FROM job_interactions
                WHERE guild_id = ? AND user_id = ?
                """,
                (guild_id, user_id),
            )
            row = await cursor.fetchone()
        assert row is not None
        return ApplicationSummary(
            clicked=int(row[0]),
            applied=int(row[1]),
            skipped=int(row[2]),
            viewed=int(row[3]),
        )

    async def get_user_applications(
        self, guild_id: int, user_id: int, limit: int = 15, offset: int = 0
    ) -> list[ApplicationRecord]:
        async with aiosqlite.connect(self.path) as connection:
            connection.row_factory = aiosqlite.Row
            cursor = await connection.execute(
                """
                SELECT job_title, company, job_url, source, clicked_at, applied,
                       notes
                FROM job_interactions
                WHERE guild_id = ? AND user_id = ?
                ORDER BY clicked_at DESC, id DESC
                LIMIT ? OFFSET ?
                """,
                (guild_id, user_id, max(1, int(limit)), max(0, int(offset))),
            )
            rows = await cursor.fetchall()
        return [
            ApplicationRecord(
                job_title=row["job_title"],
                company=row["company"],
                job_url=row["job_url"],
                source=row["source"] or "",
                clicked_at=row["clicked_at"],
                applied=(
                    None
                    if row["applied"] is None
                    else bool(row["applied"])
                ),
                notes=row["notes"] or None,
            )
            for row in rows
        ]

    async def get_debug_report_count(
        self, guild_id: int, source: str | None = None
    ) -> int:
        """Count recent reports with a non-empty note, optionally by portal."""
        query = """
            SELECT COUNT(*) FROM job_interactions
            WHERE guild_id = ?
              AND notes IS NOT NULL
              AND TRIM(notes) != ''
        """
        params: list[object] = [guild_id]
        if source:
            query += " AND LOWER(source) = LOWER(?)"
            params.append(source.strip())
        async with aiosqlite.connect(self.path) as connection:
            cursor = await connection.execute(query, params)
            row = await cursor.fetchone()
        return int(row[0]) if row else 0

    async def get_debug_reports(
        self,
        guild_id: int,
        source: str | None = None,
        limit: int = 5,
        offset: int = 0,
    ) -> list[DebugReport]:
        """Return the newest notes left in the tracking modal."""
        query = """
            SELECT user_id, job_title, job_url, source, clicked_at, applied, notes
            FROM job_interactions
            WHERE guild_id = ?
              AND notes IS NOT NULL
              AND TRIM(notes) != ''
        """
        params: list[object] = [guild_id]
        if source:
            query += " AND LOWER(source) = LOWER(?)"
            params.append(source.strip())
        query += " ORDER BY clicked_at DESC, id DESC LIMIT ? OFFSET ?"
        params.extend((max(1, int(limit)), max(0, int(offset))))
        async with aiosqlite.connect(self.path) as connection:
            connection.row_factory = aiosqlite.Row
            cursor = await connection.execute(query, params)
            rows = await cursor.fetchall()
        return [
            DebugReport(
                user_id=int(row["user_id"]),
                job_title=row["job_title"],
                job_url=row["job_url"],
                source=row["source"] or "",
                clicked_at=row["clicked_at"],
                applied=(
                    None
                    if row["applied"] is None
                    else bool(row["applied"])
                ),
                notes=row["notes"].strip(),
            )
            for row in rows
        ]

    async def cleanup_tracked_jobs(
        self, hours: float = NOTIFIED_RETENTION_HOURS
    ) -> int:
        """Drop tracking tokens older than the notification retention window."""
        cutoff = (
            datetime.now(timezone.utc) - timedelta(hours=float(hours))
        ).isoformat(timespec="seconds")
        async with aiosqlite.connect(self.path) as connection:
            cursor = await connection.execute(
                "DELETE FROM tracked_jobs WHERE created_at < ?", (cutoff,)
            )
            await connection.commit()
            return cursor.rowcount

"""Seed demo interactions for the Experimental Zone test guild/users."""

from __future__ import annotations

import asyncio
import os
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

import aiosqlite

ROOT = Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from src.database import Database  # noqa: E402

# The bot is only a member of this guild. Syncing commands into a guild it
# cannot access raises HTTP 403 (Missing Access) on startup.
GUILD_IDS = (851228769159217163,)
USERS: dict[int, list[tuple[str, str, str, bool | None]]] = {
    303354731215192066: [
        ("Staff Python Engineer", "Acme", "LinkedIn", True),
        ("Backend Developer", "Falabella", "FirstJob", True),
        ("Software Engineer", "Mercado Libre", "Get On Board", True),
        ("Desarrollador Full Stack", "Globant", "Computrabajo", True),
        ("Data Engineer", "Bancolombia", "LinkedIn", True),
        ("Frontend React Developer", "Rappi", "FirstJob", True),
        ("DevOps Engineer", "Nubank", "LinkedIn", False),
        ("Python Automation Engineer", "Salesforce", "Get On Board", False),
        ("Technical Lead", "EPAM", "Computrabajo", False),
        ("Machine Learning Engineer", "Datawiki", "LinkedIn", None),
        ("Platform Engineer", "Kuepa", "FirstJob", None),
        ("QA Automation Engineer", "Sopignt", "Computrabajo", None),
    ],
    1094833149965516801: [
        ("Senior Backend Engineer", "Netflix", "LinkedIn", True),
        ("Python Developer", "Aguakate", "FirstJob", True),
        ("Software Architect", "Bancolombia", "Get On Board", True),
        ("Cloud Engineer", "AWS", "Computrabajo", True),
        ("Frontend Engineer", "Despegar", "LinkedIn", False),
        ("Product Engineer", "Mercado Libre", "FirstJob", False),
        ("Site Reliability Engineer", "Globant", "LinkedIn", None),
        ("Mobile Developer", "Rappi", "Get On Board", None),
        ("Engineering Manager", "Nubank", "Computrabajo", None),
    ],
}


async def main() -> None:
    path = Path(os.getenv("DB_PATH", "data/jobs.db"))
    database = Database(path)
    await database.initialize()
    now = datetime.now(timezone.utc)

    for guild_id in GUILD_IDS:
        await database.set_experimental_enabled(guild_id, True)
        await database.add_language(guild_id, "spanish")

    async with aiosqlite.connect(path) as connection:
        for guild_id in GUILD_IDS:
            await connection.execute(
                "DELETE FROM job_interactions WHERE guild_id = ? AND user_id IN (?, ?)",
                (guild_id, *USERS.keys()),
            )
            for user_id, jobs in USERS.items():
                for index, (title, company, source, applied) in enumerate(jobs):
                    clicked_at = (
                        now - timedelta(days=index * 2, hours=index % 5)
                    ).isoformat(timespec="seconds")
                    await connection.execute(
                        """
                        INSERT INTO job_interactions
                            (guild_id, user_id, job_url, job_title, company,
                             source, clicked_at, applied)
                        VALUES (?, ?, ?, ?, ?, ?, ?, ?)
                        """,
                        (
                            guild_id,
                            user_id,
                            f"https://example.com/jobs/{user_id}/{index + 1}",
                            title,
                            company,
                            source,
                            clicked_at,
                            None if applied is None else int(applied),
                        ),
                    )
        await connection.commit()

    for guild_id in GUILD_IDS:
        print(f"guild {guild_id}: experimental enabled")
        for user_id in USERS:
            summary = await database.get_user_application_summary(guild_id, user_id)
            print(
                f"  user {user_id}: {summary.clicked} clicks, "
                f"{summary.applied} applied, {summary.skipped} skipped, "
                f"{summary.viewed} unanswered"
            )


if __name__ == "__main__":
    asyncio.run(main())

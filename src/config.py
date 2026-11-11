"""Application configuration loaded from environment variables."""

from __future__ import annotations

import os
from dataclasses import dataclass
from pathlib import Path

from dotenv import load_dotenv

load_dotenv()


@dataclass(frozen=True, slots=True)
class Settings:
    discord_bot_token: str
    db_path: Path
    check_interval_minutes: float

    @classmethod
    def from_env(cls) -> Settings:
        token = os.getenv("DISCORD_BOT_TOKEN", "").strip()
        if not token:
            raise ValueError("DISCORD_BOT_TOKEN is required.")

        interval_value = os.getenv("CHECK_INTERVAL_MINUTES", "5").strip()
        try:
            interval = float(interval_value)
        except ValueError as exc:
            raise ValueError("CHECK_INTERVAL_MINUTES must be a positive number.") from exc
        if interval <= 0:
            raise ValueError("CHECK_INTERVAL_MINUTES must be greater than zero.")

        db_path = Path(os.getenv("DB_PATH", "data/jobs.db")).expanduser()
        return cls(
            discord_bot_token=token,
            db_path=db_path,
            check_interval_minutes=interval,
        )


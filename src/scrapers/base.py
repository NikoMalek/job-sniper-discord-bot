"""Common scraper contract and job model."""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass


@dataclass(frozen=True, slots=True)
class Job:
    id: str
    title: str
    company: str
    location: str
    url: str
    source: str
    published_at: str
    company_logo_url: str = ""
    description: str = ""


class BaseScraper(ABC):
    source: str

    @abstractmethod
    async def fetch_jobs(
        self, keywords: list[str], location: str, max_age_hours: float = 24.0
    ) -> list[Job]:
        """Return recent jobs matching the supplied search."""

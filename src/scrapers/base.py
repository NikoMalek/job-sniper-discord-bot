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
    slug: str
    source: str

    def begin_cycle(self) -> None:
        """Reset per-cycle state. Called once before processing every guild."""

    @abstractmethod
    async def fetch_jobs(
        self,
        keywords: list[str],
        location: str,
        max_age_hours: float = 24.0,
        known_job_ids: set[str] | None = None,
    ) -> list[Job]:
        """Return recent jobs matching the supplied search.

        ``known_job_ids`` contains jobs already notified for the guild being
        processed, so scrapers can avoid downloading their details again.

        Implementations must guarantee: ``url`` is absolute, ``description``
        may be empty (``""``) when the portal has no text, and ``published_at``
        is human-readable text (it is displayed verbatim).
        """

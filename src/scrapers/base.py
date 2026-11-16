"""Common scraper contract and job model."""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass
from datetime import datetime

from ..locations import LocationIntent


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
    published_at_datetime: datetime | None = None


@dataclass(frozen=True, slots=True)
class PortalPlan:
    """How a portal will search a given location intent."""

    supported: bool
    search_location: str = ""
    note: str = ""
    reason: str = ""


class BaseScraper(ABC):
    slug: str
    source: str

    #: ISO alpha-2 codes the portal can search. ``None`` means any country.
    COVERED_COUNTRIES: frozenset[str] | None = None

    def covers(self, intent: LocationIntent) -> bool:
        """Return whether the portal can search this intent."""
        if self.COVERED_COUNTRIES is None:
            return True
        if intent.country_code is None:
            # "Worldwide" / "Remoto" alone: each portal contributes what
            # it can.
            return True
        return intent.country_code in self.COVERED_COUNTRIES

    def pause_reason(self, intent: LocationIntent) -> str:
        if self.COVERED_COUNTRIES == frozenset({"CL"}):
            covered = "Chile"
        else:
            covered = "esos países"
        return (
            f"solo cubre {covered} y la ubicación actual es {intent.label}. "
            "Cambia la ubicación o agrega una compatible con `/add-location`."
        )

    def plan(self, intent: LocationIntent) -> PortalPlan:
        """Translate a location intent into the portal's own vocabulary."""
        if not self.covers(intent):
            return PortalPlan(False, reason=self.pause_reason(intent))
        return PortalPlan(True, search_location=intent.label)

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
        is human-readable text (it is displayed verbatim). When the portal knows
        the exact publication moment, it can also set
        ``published_at_datetime`` so the notification can show a relative time.
        """

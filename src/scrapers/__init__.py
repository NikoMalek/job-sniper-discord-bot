"""Registry of job portal scrapers."""

from __future__ import annotations

from .base import BaseScraper, Job, PortalPlan
from .chiletrabajos import ChileTrabajosScraper
from .computrabajo import ComputrabajoScraper
from .http import HttpScraper
from .linkedin import LinkedInScraper

#: Slug -> singleton scraper. Instances are reused across cycles so in-memory
#: caches (descriptions, per-cycle search results) keep working.
SCRAPERS: dict[str, BaseScraper] = {
    scraper.slug: scraper
    for scraper in (
        LinkedInScraper(),
        ChileTrabajosScraper(),
        ComputrabajoScraper(),
    )
}

DEFAULT_PORTALS: tuple[str, ...] = ("linkedin",)


def get_scraper(slug: str) -> BaseScraper | None:
    return SCRAPERS.get(slug)


def portal_names(slugs: tuple[str, ...] | list[str]) -> list[str]:
    return [SCRAPERS[slug].source if slug in SCRAPERS else slug for slug in slugs]


__all__ = [
    "DEFAULT_PORTALS",
    "SCRAPERS",
    "BaseScraper",
    "ChileTrabajosScraper",
    "ComputrabajoScraper",
    "HttpScraper",
    "Job",
    "LinkedInScraper",
    "PortalPlan",
    "get_scraper",
    "portal_names",
]

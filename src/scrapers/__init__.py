"""Active job portal scrapers."""

from .base import BaseScraper, Job
from .linkedin import LinkedInScraper

ACTIVE_SCRAPERS: tuple[BaseScraper, ...] = (LinkedInScraper(),)

__all__ = ["ACTIVE_SCRAPERS", "BaseScraper", "Job", "LinkedInScraper"]


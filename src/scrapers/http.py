"""Shared HTTP behaviour for portal scrapers."""

from __future__ import annotations

import asyncio
import logging
import random

import httpx

from .base import BaseScraper

LOGGER = logging.getLogger(__name__)


class HttpScraper(BaseScraper):
    """Base class with retry/backoff helpers shared by every scraper.

    Subclasses only need to implement :meth:`fetch_jobs`; they may override the
    retry knobs below to match the portal's rate limits.
    """

    SEARCH_RETRIES = 2
    DETAIL_RETRIES = 2
    RETRY_BASE_SECONDS = 3.0
    RETRY_AFTER_CAP = 15.0

    def _retry_delay(self, response: httpx.Response | None, attempt: int) -> float:
        if response is not None:
            retry_after = response.headers.get("Retry-After", "")
            try:
                return min(max(float(retry_after), 1.0), self.RETRY_AFTER_CAP)
            except (TypeError, ValueError):
                pass
        delay = self.RETRY_BASE_SECONDS * (2**attempt)
        return delay + random.uniform(0.0, 1.5)

    @staticmethod
    def _is_authwall(response: httpx.Response) -> bool:
        final_url = str(response.url).lower()
        return "authwall" in final_url or "/login" in final_url

    async def _request_with_retry(
        self,
        client: httpx.AsyncClient,
        url: str,
        *,
        params: dict[str, str | int] | None = None,
        headers: dict[str, str] | None = None,
        retries: int | None = None,
        label: str = "request",
    ) -> httpx.Response | None:
        """Fetch ``url`` retrying rate limits, server errors and network errors."""
        attempts = self.SEARCH_RETRIES if retries is None else retries
        for attempt in range(attempts + 1):
            try:
                response = await client.get(url, params=params, headers=headers)
            except httpx.HTTPError as exc:
                if attempt >= attempts:
                    LOGGER.warning(
                        "%s %s failed after %d attempts: %s",
                        self.source,
                        label,
                        attempt + 1,
                        exc,
                    )
                    return None
                delay = self._retry_delay(None, attempt)
                LOGGER.warning(
                    "%s %s error: %s; retrying in %.1fs (%d/%d)",
                    self.source,
                    label,
                    exc,
                    delay,
                    attempt + 1,
                    attempts,
                )
                await asyncio.sleep(delay)
                continue

            if response.status_code == 429:
                if attempt >= attempts:
                    LOGGER.warning(
                        "%s %s hit 429 after %d attempts",
                        self.source,
                        label,
                        attempt + 1,
                    )
                    return None
                delay = self._retry_delay(response, attempt)
                LOGGER.warning(
                    "%s %s hit 429; retrying in %.1fs (%d/%d)",
                    self.source,
                    label,
                    delay,
                    attempt + 1,
                    attempts,
                )
                await asyncio.sleep(delay)
                continue

            if response.status_code >= 500:
                if attempt >= attempts:
                    LOGGER.warning(
                        "%s %s returned %s after %d attempts",
                        self.source,
                        label,
                        response.status_code,
                        attempt + 1,
                    )
                    return None
                delay = self._retry_delay(response, attempt)
                LOGGER.warning(
                    "%s %s returned %s; retrying in %.1fs (%d/%d)",
                    self.source,
                    label,
                    response.status_code,
                    delay,
                    attempt + 1,
                    attempts,
                )
                await asyncio.sleep(delay)
                continue

            return response

        return None

"""LinkedIn guest jobs scraper."""

from __future__ import annotations

import asyncio
import logging
import math
import random
import time
from dataclasses import replace
from urllib.parse import parse_qsl, urlencode, urljoin, urlsplit, urlunsplit

import httpx
from bs4 import BeautifulSoup

from .base import Job
from .http import HttpScraper

LOGGER = logging.getLogger(__name__)


class LinkedInScraper(HttpScraper):
    slug = "linkedin"
    source = "LinkedIn"
    endpoint = "https://www.linkedin.com/jobs-guest/jobs/api/seeMoreJobPostings/search"
    user_agents = (
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
        "(KHTML, like Gecko) Chrome/131.0 Safari/537.36",
        "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 "
        "(KHTML, like Gecko) Version/18.1 Safari/605.1.15",
        "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
        "(KHTML, like Gecko) Chrome/131.0 Safari/537.36",
    )

    SEARCH_RETRIES = 2
    DETAIL_RETRIES = 2
    MAX_SEARCH_PAGES = 10
    DETAIL_DELAY_RANGE = (0.8, 1.4)
    SEARCH_PAGE_DELAY_RANGE = (1.0, 2.0)
    DESCRIPTION_CACHE_TTL_SECONDS = 30 * 60
    DESCRIPTION_CACHE_LIMIT = 500

    def __init__(self) -> None:
        self._description_cache: dict[str, tuple[float, str]] = {}
        self._search_cache: dict[
            tuple[tuple[str, ...], str, float],
            tuple[dict[str, Job], int, int],
        ] = {}

    def begin_cycle(self) -> None:
        self._search_cache.clear()

    @staticmethod
    def _clean_url(url: str) -> str:
        parts = urlsplit(url)
        query = [
            (key, value)
            for key, value in parse_qsl(parts.query, keep_blank_values=True)
            if not key.lower().startswith(("trk", "tracking", "refid"))
        ]
        return urlunsplit((parts.scheme, parts.netloc, parts.path, urlencode(query), ""))

    @staticmethod
    def _text(card: BeautifulSoup, selector: str) -> str:
        element = card.select_one(selector)
        return element.get_text(" ", strip=True) if element else ""

    @staticmethod
    def _logo_url(card: BeautifulSoup) -> str:
        image = card.select_one("img")
        if image is None:
            return ""
        for attribute in ("src", "data-src", "data-delayed-url", "data-ghost-url"):
            value = image.get(attribute)
            if isinstance(value, str) and value.strip():
                candidate = urljoin("https://www.linkedin.com", value.strip())
                if urlsplit(candidate).scheme in {"http", "https"}:
                    return candidate
        srcset = image.get("srcset")
        if isinstance(srcset, str):
            first_candidate = srcset.split(",")[0].strip().split(" ")[0]
            if first_candidate:
                candidate = urljoin("https://www.linkedin.com", first_candidate)
                if urlsplit(candidate).scheme in {"http", "https"}:
                    return candidate
        return ""

    def _parse_jobs(self, html: str) -> list[Job]:
        soup = BeautifulSoup(html, "html.parser")
        jobs: list[Job] = []
        seen_ids: set[str] = set()
        for card in soup.select("li"):
            link = card.select_one("a.base-card__full-link, a.base-card-card__full-link")
            if link is None:
                link = card.select_one("a[href*='/jobs/view/']")
            if link is None or not link.get("href"):
                continue
            url = self._clean_url(str(link["href"]))
            path_parts = [part for part in urlsplit(url).path.split("/") if part]
            job_id = path_parts[-1] if path_parts else ""
            if not job_id or job_id in seen_ids:
                continue
            title = self._text(card, "h3.base-search-card__title") or self._text(card, "h3")
            company = self._text(card, "h4.base-search-card__subtitle") or self._text(card, "h4")
            location = self._text(card, "span.job-search-card__location") or self._text(card, "span")
            published = self._text(card, "time")
            logo_url = self._logo_url(card)
            description = self._text(
                card,
                ".base-search-card__snippet, .job-search-card__snippet, "
                ".base-card__snippet, p.description",
            )
            if not title:
                continue
            seen_ids.add(job_id)
            jobs.append(
                Job(
                    id=job_id,
                    title=title,
                    company=company or "No especificada",
                    location=location or "No especificada",
                    url=url,
                    source=self.source,
                    published_at=published,
                    company_logo_url=logo_url,
                    description=description,
                )
            )
        return jobs

    @staticmethod
    def _parse_description(html: str) -> str:
        soup = BeautifulSoup(html, "html.parser")
        element = soup.select_one(
            ".show-more-less-html__markup, .description__text, "
            ".jobs-description__content, [class*='description']"
        )
        if element:
            return element.get_text(" ", strip=True)
        meta = soup.select_one("meta[name='description']")
        return str(meta.get("content", "")).strip() if meta else ""

    def _cached_description(self, job_id: str) -> str | None:
        entry = self._description_cache.get(job_id)
        if entry is None:
            return None
        expires_at, description = entry
        if expires_at <= time.monotonic():
            self._description_cache.pop(job_id, None)
            return None
        return description

    def _store_description(self, job_id: str, description: str) -> None:
        now = time.monotonic()
        if len(self._description_cache) >= self.DESCRIPTION_CACHE_LIMIT:
            expired = [
                key
                for key, (expires_at, _) in self._description_cache.items()
                if expires_at <= now
            ]
            for key in expired:
                self._description_cache.pop(key, None)
            while len(self._description_cache) >= self.DESCRIPTION_CACHE_LIMIT:
                self._description_cache.pop(next(iter(self._description_cache)))
        self._description_cache[job_id] = (
            now + self.DESCRIPTION_CACHE_TTL_SECONDS,
            description,
        )

    async def _fetch_description(
        self,
        client: httpx.AsyncClient,
        job: Job,
        headers: dict[str, str],
    ) -> str | None:
        """Return the description, or ``None`` if the page could not be read.

        An empty string means the page loaded but has no parseable description;
        those jobs are still returned so they can be notified without a
        language check.
        """
        response = await self._request_with_retry(
            client,
            job.url,
            headers=headers,
            retries=self.DETAIL_RETRIES,
            label=f"job {job.id}",
        )
        if response is None:
            LOGGER.warning(
                "LinkedIn description unavailable for job %s; deferring to next cycle",
                job.id,
            )
            return None
        if not response.is_success:
            LOGGER.warning(
                "LinkedIn returned %s for job %s; deferring to next cycle",
                response.status_code,
                job.id,
            )
            return None
        if self._is_authwall(response):
            LOGGER.warning(
                "LinkedIn authwall for job %s; deferring to next cycle", job.id
            )
            return None
        return self._parse_description(response.text)

    async def fetch_jobs(
        self,
        keywords: list[str],
        location: str,
        max_age_hours: float = 24.0,
        known_job_ids: set[str] | None = None,
    ) -> list[Job]:
        age_seconds = math.ceil(max_age_hours * 60 * 60)
        params: dict[str, str | int] = {
            "keywords": " OR ".join(
                keyword.strip() for keyword in keywords if keyword.strip()
            ),
            "location": location,
            "f_TPR": f"r{age_seconds}",
            "sortBy": "DD",
        }
        headers = {
            "User-Agent": random.choice(self.user_agents),
            "Accept-Language": "en-US,en;q=0.9",
        }
        known = known_job_ids or set()
        cache_key = (
            tuple(
                sorted(
                    keyword.strip()
                    for keyword in keywords
                    if keyword.strip()
                )
            ),
            location,
            max_age_hours,
        )
        search_cache_hit = False

        try:
            async with httpx.AsyncClient(
                timeout=httpx.Timeout(20.0), follow_redirects=True
            ) as client:
                cached_search = self._search_cache.get(cache_key)
                if cached_search is not None:
                    search_cache_hit = True
                    cards, pages_fetched, found_count = cached_search
                else:
                    cards: dict[str, Job] = {}
                    pages_fetched = 0
                    found_count = 0

                    for page in range(self.MAX_SEARCH_PAGES):
                        params["start"] = page * 10
                        response = await self._request_with_retry(
                            client,
                            self.endpoint,
                            params=params,
                            headers=headers,
                            retries=self.SEARCH_RETRIES,
                            label=f"search page {page + 1}",
                        )
                        if response is None:
                            LOGGER.warning(
                                "LinkedIn search stopped at page %d; keeping %d jobs "
                                "from previous pages",
                                page + 1,
                                len(cards),
                            )
                            break
                        if self._is_authwall(response):
                            LOGGER.warning(
                                "LinkedIn search authwall on page %d; stopping", page + 1
                            )
                            break
                        if not response.is_success:
                            LOGGER.warning(
                                "LinkedIn search returned %s on page %d; stopping",
                                response.status_code,
                                page + 1,
                            )
                            break

                        page_jobs = self._parse_jobs(response.text)
                        pages_fetched += 1
                        found_count += len(page_jobs)
                        if not page_jobs:
                            break

                        new_jobs = 0
                        for job in page_jobs:
                            if job.id not in cards:
                                cards[job.id] = job
                                new_jobs += 1

                        if new_jobs == 0:
                            break
                        if page + 1 < self.MAX_SEARCH_PAGES:
                            await asyncio.sleep(
                                random.uniform(*self.SEARCH_PAGE_DELAY_RANGE)
                            )

                    self._search_cache[cache_key] = (
                        cards,
                        pages_fetched,
                        found_count,
                    )

                job_cards = list(cards.values())
                enriched: list[Job] = []
                known_count = 0
                cached_count = 0
                fetched_count = 0
                without_description = 0
                deferred_count = 0

                for job in job_cards:
                    if job.id in known:
                        known_count += 1
                        continue

                    cached_description = self._cached_description(job.id)
                    if cached_description is not None:
                        cached_count += 1
                        if cached_description:
                            enriched.append(replace(job, description=cached_description))
                        else:
                            enriched.append(job)
                        continue

                    description = await self._fetch_description(
                        client, job, headers
                    )
                    if description is None:
                        deferred_count += 1
                        continue

                    fetched_count += 1
                    self._store_description(job.id, description)
                    if description:
                        enriched.append(replace(job, description=description))
                    else:
                        without_description += 1
                        enriched.append(job)
                    await asyncio.sleep(random.uniform(*self.DETAIL_DELAY_RANGE))
        except httpx.HTTPError:
            LOGGER.exception("LinkedIn request failed")
            return []

        LOGGER.info(
            "LinkedIn cycle: search=%s pages=%d found=%d unique=%d known=%d "
            "cached=%d fetched=%d without_description=%d deferred=%d",
            "cache" if search_cache_hit else "live",
            pages_fetched,
            found_count,
            len(job_cards),
            known_count,
            cached_count,
            fetched_count,
            without_description,
            deferred_count,
        )
        return enriched

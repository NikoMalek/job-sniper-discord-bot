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

from .base import BaseScraper, Job

LOGGER = logging.getLogger(__name__)


class LinkedInScraper(BaseScraper):
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

    DETAIL_RETRIES = 2
    RETRY_BASE_SECONDS = 3.0
    RETRY_AFTER_CAP = 15.0
    DETAIL_DELAY_RANGE = (0.8, 1.4)
    DESCRIPTION_CACHE_TTL_SECONDS = 30 * 60
    DESCRIPTION_CACHE_LIMIT = 500
    DETAIL_COOLDOWN_SECONDS = 4 * 60

    def __init__(self) -> None:
        self._description_cache: dict[str, tuple[float, str]] = {}
        self._rate_limited_jobs: dict[str, float] = {}
        self._detail_cooldown_until = 0.0

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

    def _retry_delay(self, response: httpx.Response, attempt: int) -> float:
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

    async def _fetch_description(
        self,
        client: httpx.AsyncClient,
        job: Job,
        headers: dict[str, str],
    ) -> tuple[str | None, bool]:
        """Return ``(description, abort_batch)``.

        ``description`` is ``None`` when the page could not be read. An empty
        string means the page loaded but has no parseable description.
        ``abort_batch`` is ``True`` when LinkedIn is rate limiting us and the
        remaining jobs should be deferred to the next cycle.
        """
        for attempt in range(self.DETAIL_RETRIES + 1):
            try:
                response = await client.get(job.url, headers=headers)
            except httpx.HTTPError:
                LOGGER.warning("Could not fetch description for LinkedIn job %s", job.id)
                return None, False
            if response.status_code == 429:
                if attempt < self.DETAIL_RETRIES:
                    delay = self._retry_delay(response, attempt)
                    LOGGER.warning(
                        "LinkedIn 429 for job %s; retrying in %.1fs (%d/%d)",
                        job.id,
                        delay,
                        attempt + 1,
                        self.DETAIL_RETRIES,
                    )
                    await asyncio.sleep(delay)
                    continue
                LOGGER.warning(
                    "LinkedIn 429 persists for job %s; deferring remaining jobs",
                    job.id,
                )
                return None, True
            if not response.is_success:
                LOGGER.warning(
                    "LinkedIn returned %s for job %s", response.status_code, job.id
                )
                return None, False
            if self._is_authwall(response):
                LOGGER.warning("LinkedIn authwall for job %s; deferring", job.id)
                return None, True
            return self._parse_description(response.text), False
        return None, True

    async def fetch_jobs(
        self,
        keywords: list[str],
        location: str,
        max_age_hours: float = 24.0,
        known_job_ids: set[str] | None = None,
    ) -> list[Job]:
        age_seconds = math.ceil(max_age_hours * 60 * 60)
        params = {
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

        now = time.monotonic()
        if len(self._rate_limited_jobs) > self.DESCRIPTION_CACHE_LIMIT:
            self._rate_limited_jobs = {
                job_id: expires_at
                for job_id, expires_at in self._rate_limited_jobs.items()
                if expires_at > now
            }

        try:
            async with httpx.AsyncClient(
                timeout=httpx.Timeout(20.0), follow_redirects=True
            ) as client:
                response = await client.get(self.endpoint, params=params, headers=headers)
                if response.status_code == 429:
                    LOGGER.warning("LinkedIn rate limit reached; skipping this cycle")
                    return []
                response.raise_for_status()
                cards = self._parse_jobs(response.text)

                enriched: list[Job] = []
                known_count = 0
                cached_count = 0
                fetched_count = 0
                without_description = 0
                deferred_count = 0

                for job in cards:
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

                    now = time.monotonic()
                    if self._rate_limited_jobs.get(job.id, 0.0) > now:
                        deferred_count += 1
                        continue
                    if self._detail_cooldown_until > now:
                        deferred_count += 1
                        continue

                    description, abort_batch = await self._fetch_description(
                        client, job, headers
                    )
                    if description is None:
                        deferred_count += 1
                        self._rate_limited_jobs[job.id] = (
                            time.monotonic() + self.DETAIL_COOLDOWN_SECONDS
                        )
                        if abort_batch:
                            self._detail_cooldown_until = (
                                time.monotonic() + self.DETAIL_COOLDOWN_SECONDS
                            )
                            break
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
            "LinkedIn cycle: found=%d known=%d cached=%d fetched=%d "
            "without_description=%d deferred=%d",
            len(cards),
            known_count,
            cached_count,
            fetched_count,
            without_description,
            deferred_count,
        )
        return enriched

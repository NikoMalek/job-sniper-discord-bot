"""LinkedIn guest jobs scraper."""

from __future__ import annotations

import asyncio
import logging
import math
import random
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

    async def fetch_jobs(
        self, keywords: list[str], location: str, max_age_hours: float = 24.0
    ) -> list[Job]:
        age_seconds = math.ceil(max_age_hours * 60 * 60)
        params = {
            "keywords": " OR ".join(keyword.strip() for keyword in keywords if keyword.strip()),
            "location": location,
            "f_TPR": f"r{age_seconds}",
            "sortBy": "DD",
        }
        headers = {"User-Agent": random.choice(self.user_agents), "Accept-Language": "en-US,en;q=0.9"}
        try:
            async with httpx.AsyncClient(timeout=httpx.Timeout(20.0), follow_redirects=True) as client:
                response = await client.get(self.endpoint, params=params, headers=headers)
                if response.status_code == 429:
                    LOGGER.warning("LinkedIn rate limit reached; skipping this cycle")
                    return []
                response.raise_for_status()
                jobs = self._parse_jobs(response.text)
                enriched_jobs: list[Job] = []
                for job in jobs:
                    if job.description:
                        enriched_jobs.append(job)
                        continue
                    try:
                        detail_response = await client.get(job.url, headers=headers)
                    except httpx.HTTPError:
                        LOGGER.warning("Could not fetch description for LinkedIn job %s", job.id)
                        enriched_jobs.append(job)
                        continue
                    if detail_response.status_code == 429:
                        LOGGER.warning("LinkedIn rate limit reached while fetching job details")
                        enriched_jobs.extend(jobs[len(enriched_jobs):])
                        break
                    if detail_response.is_success:
                        description = self._parse_description(detail_response.text)
                        enriched_jobs.append(replace(job, description=description))
                    else:
                        enriched_jobs.append(job)
                    await asyncio.sleep(0.5)
        except httpx.HTTPError:
            LOGGER.exception("LinkedIn request failed")
            return []
        return enriched_jobs

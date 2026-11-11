"""LinkedIn guest jobs scraper."""

from __future__ import annotations

import logging
import random
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit

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
                )
            )
        return jobs

    async def fetch_jobs(self, keywords: list[str], location: str) -> list[Job]:
        params = {
            "keywords": " OR ".join(keyword.strip() for keyword in keywords if keyword.strip()),
            "location": location,
            "f_TPR": "r86400",
            "sortBy": "DD",
        }
        headers = {"User-Agent": random.choice(self.user_agents), "Accept-Language": "en-US,en;q=0.9"}
        try:
            async with httpx.AsyncClient(timeout=httpx.Timeout(20.0), follow_redirects=True) as client:
                response = await client.get(self.endpoint, params=params, headers=headers)
        except httpx.HTTPError:
            LOGGER.exception("LinkedIn request failed")
            return []
        if response.status_code == 429:
            LOGGER.warning("LinkedIn rate limit reached; skipping this cycle")
            return []
        try:
            response.raise_for_status()
        except httpx.HTTPStatusError:
            LOGGER.exception("LinkedIn returned HTTP %s", response.status_code)
            return []
        return self._parse_jobs(response.text)


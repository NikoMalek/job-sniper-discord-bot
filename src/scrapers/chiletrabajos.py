"""ChileTrabajos scraper."""

from __future__ import annotations

import asyncio
import html as html_module
import json
import logging
import math
import random
import re
import time
import unicodedata
from dataclasses import replace
from datetime import datetime, timedelta, timezone
from urllib.parse import urljoin, urlsplit
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

import httpx
from bs4 import BeautifulSoup

from .base import Job
from .http import HttpScraper

LOGGER = logging.getLogger(__name__)

BASE_URL = "https://www.chiletrabajos.cl"
SEARCH_PATH = "/encuentra-un-empleo"

try:
    CHILE_TIMEZONE = ZoneInfo("America/Santiago")
except ZoneInfoNotFoundError:  # pragma: no cover - depends on host tz database
    CHILE_TIMEZONE = timezone(timedelta(hours=-4), "America/Santiago")
    LOGGER.warning(
        "tzdata is not installed; ChileTrabajos dates will use a fixed UTC-4 offset"
    )

_MONTH_ABBREVIATIONS = (
    "ene",
    "feb",
    "mar",
    "abr",
    "may",
    "jun",
    "jul",
    "ago",
    "sep",
    "oct",
    "nov",
    "dic",
)

# Fallback used when the search form cannot be downloaded. The scraper
# refreshes the full list from the site every few hours.
_STATIC_CITIES = {
    "antofagasta": "1004",
    "arica": "1000",
    "chillan": "1036",
    "concepcion": "1035",
    "copiapo": "1007",
    "curico": "1032",
    "iquique": "1002",
    "la serena": "1010",
    "los angeles": "1037",
    "puerto montt": "1043",
    "punta arenas": "1051",
    "rancagua": "1028",
    "santiago": "1022",
    "talca": "1031",
    "temuco": "1039",
    "valdivia": "1041",
    "valparaiso": "1014",
}

_REMOTE_PREFIXES = ("anywhere", "remote", "remoto", "todo chile", "worldwide")


class ChileTrabajosScraper(HttpScraper):
    slug = "chiletrabajos"
    source = "ChileTrabajos"
    user_agents = (
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
        "(KHTML, like Gecko) Chrome/131.0 Safari/537.36",
        "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 "
        "(KHTML, like Gecko) Version/18.1 Safari/605.1.15",
        "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
        "(KHTML, like Gecko) Chrome/131.0 Safari/537.36",
    )

    PAGE_SIZE = 30
    MAX_PAGES_PER_KEYWORD = 3
    MAX_DETAILS_PER_CYCLE = 25
    DETAIL_DELAY_RANGE = (0.4, 0.9)
    SEARCH_PAGE_DELAY_RANGE = (0.5, 1.0)
    CITY_CACHE_TTL_SECONDS = 6 * 60 * 60
    DETAIL_CACHE_TTL_SECONDS = 6 * 60 * 60
    DETAIL_CACHE_LIMIT = 500
    DEFERRED_TTL_SECONDS = 3 * 60 * 60
    DEFERRED_LIMIT = 200
    DEFERRED_CONTEXT_LIMIT = 50

    def __init__(self) -> None:
        self._city_cache: tuple[float, dict[str, str]] | None = None
        self._detail_cache: dict[str, tuple[float, Job, datetime]] = {}
        self._deferred: dict[
            tuple[tuple[str, ...], str, int],
            dict[str, tuple[float, Job]],
        ] = {}

    @staticmethod
    def _normalize(value: str) -> str:
        normalized = unicodedata.normalize("NFKD", value.casefold())
        without_accents = "".join(
            character for character in normalized
            if not unicodedata.combining(character)
        )
        return " ".join(without_accents.split())

    @staticmethod
    def _text(element: object) -> str:
        if element is None or not hasattr(element, "get_text"):
            return ""
        return " ".join(element.get_text(" ", strip=True).split())

    @staticmethod
    def _unescape(value: object) -> str:
        if not isinstance(value, str):
            return ""
        return html_module.unescape(value).strip()

    @staticmethod
    def _fecha_bucket(hours: float) -> int:
        if hours <= 24:
            return 2  # desde ayer
        if hours <= 72:
            return 3  # menos de 3 días
        if hours <= 144:
            return 4  # menos de 6 días
        if hours <= 168:
            return 5  # última semana
        return 6

    @staticmethod
    def _parse_posted_date(value: object) -> datetime | None:
        if not isinstance(value, str) or not value.strip():
            return None
        text = value.strip().replace("Z", "+00:00")
        try:
            parsed = datetime.fromisoformat(text)
        except ValueError:
            return None
        if parsed.tzinfo is None:
            parsed = parsed.replace(tzinfo=CHILE_TIMEZONE)
        return parsed

    @staticmethod
    def _format_published(value: datetime) -> str:
        return (
            f"{value.day} {_MONTH_ABBREVIATIONS[value.month - 1]} "
            f"{value.year}, {value:%H:%M}"
        )

    @staticmethod
    def _image_url(element: object) -> str:
        if element is None or not hasattr(element, "get"):
            return ""
        for attribute in ("src", "data-src", "data-original", "data-lazy-src"):
            value = element.get(attribute)
            if isinstance(value, str) and value.strip():
                candidate = urljoin(BASE_URL, value.strip())
                if urlsplit(candidate).scheme in {"http", "https"}:
                    return candidate
        srcset = element.get("srcset")
        if isinstance(srcset, str) and srcset.strip():
            first = srcset.split(",")[0].strip().split(" ")[0]
            candidate = urljoin(BASE_URL, first)
            if urlsplit(candidate).scheme in {"http", "https"}:
                return candidate
        return ""

    @staticmethod
    def _background_url(element: object) -> str:
        if element is None or not hasattr(element, "get"):
            return ""
        style = element.get("style")
        if not isinstance(style, str):
            return ""
        match = re.search(r"url\(\s*['\"]?([^'\")]+)", style, re.IGNORECASE)
        if match is None:
            return ""
        candidate = urljoin(BASE_URL, match.group(1).strip())
        return candidate if urlsplit(candidate).scheme in {"http", "https"} else ""

    @classmethod
    def _logo_url(cls, card: BeautifulSoup) -> str:
        url = cls._image_url(card.select_one("img.image-oferta"))
        if url:
            return url

        thumb = card.select_one("div.thumb")
        if thumb is None:
            return ""
        return cls._image_url(thumb.select_one("img")) or cls._background_url(thumb)

    @classmethod
    def _parse_cards(cls, html: str) -> list[Job]:
        soup = BeautifulSoup(html, "html.parser")
        for featured in soup.select(".featured"):
            featured.decompose()

        jobs: list[Job] = []
        seen_ids: set[str] = set()
        for card in soup.select("div.job-item"):
            link = card.select_one("h2.title a")
            if link is None or not link.get("href"):
                continue
            url = urljoin(BASE_URL, str(link["href"]).strip())
            match = re.search(r"-(\d+)/?$", urlsplit(url).path)
            if match is None:
                continue
            job_id = match.group(1)
            if job_id in seen_ids:
                continue

            title = cls._text(link)
            if not title:
                continue

            metas = card.select("h3.meta")
            company = ""
            location = ""
            if metas:
                city = metas[0].select_one("a[href*='/ciudad/']")
                location = cls._text(city)
                company_text = cls._text(metas[0])
                company = (
                    re.sub(
                        rf"\s*{re.escape(location)}\s*$",
                        "",
                        company_text,
                    )
                    if location
                    else company_text
                ).strip(" ,")

            published = ""
            for meta in metas:
                if meta.select_one("i.fa-calendar"):
                    published = cls._text(meta)
                    break

            description = ""
            description_element = card.select_one("p.description")
            if description_element is not None:
                for extra in description_element.select(".ver-mas-btn"):
                    extra.decompose()
                description = cls._text(description_element)

            seen_ids.add(job_id)
            jobs.append(
                Job(
                    id=job_id,
                    title=title,
                    company=company or "No especificada",
                    location=location or "No especificada",
                    url=url,
                    source=cls.source,
                    published_at=published,
                    company_logo_url=cls._logo_url(card),
                    description=description,
                )
            )
        return jobs

    @staticmethod
    def _parse_detail(html: str) -> dict | None:
        soup = BeautifulSoup(html, "html.parser")
        for script in soup.select("script[type='application/ld+json']"):
            raw = script.string or script.get_text()
            if not raw or not raw.strip():
                continue
            try:
                data = json.loads(raw)
            except json.JSONDecodeError:
                continue
            for item in data if isinstance(data, list) else [data]:
                if isinstance(item, dict) and item.get("@type") == "JobPosting":
                    return item
        return None

    @classmethod
    def _detail_logo_url(cls, html: str) -> str:
        soup = BeautifulSoup(html, "html.parser")
        url = cls._image_url(soup.select_one("img.image-oferta"))
        if url:
            return url
        thumb = soup.select_one("div.job-item.detalle div.thumb")
        if thumb is None:
            return ""
        return cls._image_url(thumb.select_one("img")) or cls._background_url(thumb)

    @classmethod
    def _job_from_detail(
        cls, card: Job, detail: dict, logo_url: str = ""
    ) -> tuple[Job, datetime] | None:
        posted = cls._parse_posted_date(detail.get("datePosted"))
        if posted is None:
            return None

        organization = detail.get("hiringOrganization")
        if not isinstance(organization, dict):
            organization = {}
        job_location = detail.get("jobLocation")
        if not isinstance(job_location, dict):
            job_location = {}
        address = job_location.get("address")
        if not isinstance(address, dict):
            address = {}

        title = cls._unescape(detail.get("title")) or card.title
        company = cls._unescape(organization.get("name")) or card.company
        organization_logo = cls._unescape(organization.get("logo"))
        if organization_logo:
            organization_logo = urljoin(BASE_URL, organization_logo)
            if urlsplit(organization_logo).scheme not in {"http", "https"}:
                organization_logo = ""
        locality = cls._unescape(address.get("addressLocality"))
        location = locality or card.location
        if str(detail.get("jobLocationType", "")).upper() == "TELECOMMUTE":
            location = (
                f"{location} (Remoto)"
                if location and location != "No especificada"
                else "Remoto"
            )

        description = cls._unescape(detail.get("description")) or card.description
        description = description.replace("\r\n", "\n").replace("\r", "\n").strip()

        return (
            replace(
                card,
                title=title,
                company=company or "No especificada",
                location=location or "No especificada",
                company_logo_url=(
                    card.company_logo_url
                    or organization_logo
                    or logo_url
                ),
                description=description,
                published_at=cls._format_published(posted),
                published_at_datetime=posted,
            ),
            posted,
        )

    @classmethod
    def _parse_cities(cls, html: str) -> dict[str, str]:
        soup = BeautifulSoup(html, "html.parser")
        select = soup.select_one("select[name='13']")
        if select is None:
            return {}

        mapping: dict[str, str] = {}
        for option in select.select("option"):
            value = option.get("value")
            if not isinstance(value, str) or not value.strip():
                continue
            name = cls._normalize(cls._text(option))
            if name and name != "--":
                mapping[name] = value.strip()
            seo = option.get("data-seo")
            if isinstance(seo, str) and seo.strip():
                mapping[cls._normalize(seo.replace("-", " "))] = value.strip()
        return mapping

    @classmethod
    def _match_location(cls, location: str, mapping: dict[str, str]) -> str:
        normalized = cls._normalize(location)
        if not normalized:
            return ""
        if normalized == "chile" or normalized.startswith(_REMOTE_PREFIXES):
            return ""
        if normalized in mapping:
            return mapping[normalized]
        for segment in normalized.split(","):
            segment = segment.strip()
            if segment in mapping:
                return mapping[segment]
            if segment == "chile" or segment.startswith(_REMOTE_PREFIXES):
                return ""

        padded = f" {normalized} "
        matches = [name for name in mapping if f" {name} " in padded]
        if not matches:
            return ""
        return mapping[max(matches, key=len)]

    async def _city_mapping(
        self, client: httpx.AsyncClient
    ) -> dict[str, str]:
        now = time.monotonic()
        if self._city_cache is not None and self._city_cache[0] > now:
            return self._city_cache[1]

        mapping = dict(_STATIC_CITIES)
        response = await self._request_with_retry(
            client,
            f"{BASE_URL}{SEARCH_PATH}",
            retries=1,
            label="city list",
        )
        if response is not None and response.is_success:
            mapping.update(self._parse_cities(response.text))
        else:
            LOGGER.warning(
                "ChileTrabajos city list unavailable; using built-in city names"
            )
        self._city_cache = (now + self.CITY_CACHE_TTL_SECONDS, mapping)
        return mapping

    def _cached_detail(self, job_id: str) -> tuple[Job, datetime] | None:
        entry = self._detail_cache.get(job_id)
        if entry is None:
            return None
        expires_at, job, posted = entry
        if expires_at <= time.monotonic():
            self._detail_cache.pop(job_id, None)
            return None
        return job, posted

    def _store_detail(self, job_id: str, job: Job, posted: datetime) -> None:
        now = time.monotonic()
        if len(self._detail_cache) >= self.DETAIL_CACHE_LIMIT:
            expired = [
                key
                for key, (expires_at, _, _) in self._detail_cache.items()
                if expires_at <= now
            ]
            for key in expired:
                self._detail_cache.pop(key, None)
            while len(self._detail_cache) >= self.DETAIL_CACHE_LIMIT:
                self._detail_cache.pop(next(iter(self._detail_cache)))
        self._detail_cache[job_id] = (
            now + self.DETAIL_CACHE_TTL_SECONDS,
            job,
            posted,
        )

    def _prune_deferred(self, now: float) -> None:
        for context, items in list(self._deferred.items()):
            alive = {
                job_id: value
                for job_id, value in items.items()
                if value[0] > now
            }
            if alive:
                self._deferred[context] = alive
            else:
                self._deferred.pop(context, None)

        while len(self._deferred) > self.DEFERRED_CONTEXT_LIMIT:
            oldest = min(
                self._deferred,
                key=lambda key: min(
                    expires_at for expires_at, _ in self._deferred[key].values()
                ),
            )
            self._deferred.pop(oldest, None)

    @staticmethod
    def _is_recent(posted: datetime, cutoff: datetime) -> bool:
        return posted.astimezone(timezone.utc) >= cutoff

    @classmethod
    def _parse_total(cls, html: str) -> int | None:
        soup = BeautifulSoup(html, "html.parser")
        for heading in soup.select("h2"):
            match = re.search(
                r"([\d.]+)\s+Trabajos", cls._text(heading), re.IGNORECASE
            )
            if match is None:
                continue
            try:
                return int(match.group(1).replace(".", ""))
            except ValueError:
                continue
        return None

    async def fetch_jobs(
        self,
        keywords: list[str],
        location: str,
        max_age_hours: float = 24.0,
        known_job_ids: set[str] | None = None,
    ) -> list[Job]:
        terms: list[str] = []
        seen_terms: set[str] = set()
        for keyword in keywords:
            cleaned = keyword.strip()
            normalized = self._normalize(cleaned)
            if not cleaned or normalized in seen_terms:
                continue
            seen_terms.add(normalized)
            terms.append(cleaned)
        if not terms:
            return []

        known = known_job_ids or set()
        cutoff = datetime.now(timezone.utc) - timedelta(hours=max_age_hours)
        fecha = self._fecha_bucket(max_age_hours)
        headers = {
            "User-Agent": random.choice(self.user_agents),
            "Accept-Language": "es-CL,es;q=0.9,en;q=0.8",
        }

        try:
            async with httpx.AsyncClient(
                timeout=httpx.Timeout(20.0),
                follow_redirects=True,
                headers=headers,
            ) as client:
                normalized_location = self._normalize(location)
                if (
                    not normalized_location
                    or normalized_location == "chile"
                    or normalized_location.startswith(_REMOTE_PREFIXES)
                ):
                    location_id = ""
                else:
                    location_id = self._match_location(
                        location, await self._city_mapping(client)
                    )
                candidates: dict[str, Job] = {}
                pages_fetched = 0
                found_count = 0
                known_count = 0

                for keyword in terms:
                    max_pages = self.MAX_PAGES_PER_KEYWORD
                    for page in range(max_pages):
                        url = (
                            f"{BASE_URL}{SEARCH_PATH}"
                            if page == 0
                            else f"{BASE_URL}{SEARCH_PATH}/{page * self.PAGE_SIZE}"
                        )
                        params: dict[str, str | int] = {
                            "2": keyword,
                            "fecha": fecha,
                        }
                        if location_id:
                            params["13"] = location_id

                        response = await self._request_with_retry(
                            client,
                            url,
                            params=params,
                            retries=self.SEARCH_RETRIES,
                            label=f"search {keyword!r} page {page + 1}",
                        )
                        if response is None:
                            LOGGER.warning(
                                "ChileTrabajos search stopped for %r at page %d",
                                keyword,
                                page + 1,
                            )
                            break
                        if not response.is_success:
                            LOGGER.warning(
                                "ChileTrabajos search for %r returned %s on page %d",
                                keyword,
                                response.status_code,
                                page + 1,
                            )
                            break

                        page_jobs = self._parse_cards(response.text)
                        pages_fetched += 1
                        found_count += len(page_jobs)

                        if page == 0:
                            total = self._parse_total(response.text)
                            if total is not None:
                                max_pages = min(
                                    max_pages,
                                    max(1, math.ceil(total / self.PAGE_SIZE)),
                                )

                        if not page_jobs:
                            break

                        new_jobs = 0
                        for job in page_jobs:
                            if job.id in candidates:
                                continue
                            candidates[job.id] = job
                            new_jobs += 1
                            if job.id in known:
                                known_count += 1

                        if page + 1 >= max_pages:
                            break
                        if len(page_jobs) < self.PAGE_SIZE:
                            break
                        if new_jobs == 0:
                            break
                        await asyncio.sleep(
                            random.uniform(*self.SEARCH_PAGE_DELAY_RANGE)
                        )

                now = time.monotonic()
                context = (
                    tuple(sorted(self._normalize(term) for term in terms)),
                    location_id,
                    fecha,
                )
                deferred_items = self._deferred.get(context, {})
                deferred = [
                    (expires_at, job)
                    for job_id, (expires_at, job) in deferred_items.items()
                    if expires_at > now and job_id not in known
                ]
                pending: dict[str, Job] = {
                    job.id: job for _, job in deferred
                }
                for job in candidates.values():
                    if job.id in known or job.id in pending:
                        continue
                    pending[job.id] = job

                enriched: list[Job] = []
                remaining: dict[str, Job] = {}
                cached_count = 0
                fetched_count = 0
                too_old_count = 0
                expired_count = 0
                error_count = 0
                pending_ids = list(pending)

                for index, job_id in enumerate(pending_ids):
                    if fetched_count >= self.MAX_DETAILS_PER_CYCLE:
                        for deferred_id in pending_ids[index:]:
                            remaining[deferred_id] = pending[deferred_id]
                        break

                    job = pending[job_id]
                    cached_detail = self._cached_detail(job_id)
                    if cached_detail is not None:
                        cached_count += 1
                        cached_job, posted = cached_detail
                        if self._is_recent(posted, cutoff):
                            enriched.append(cached_job)
                        else:
                            too_old_count += 1
                        continue

                    response = await self._request_with_retry(
                        client,
                        job.url,
                        retries=self.DETAIL_RETRIES,
                        label=f"job {job_id}",
                    )
                    fetched_count += 1
                    if response is None:
                        error_count += 1
                        remaining[job_id] = job
                        continue
                    if response.status_code == 404:
                        expired_count += 1
                        continue
                    if not response.is_success:
                        error_count += 1
                        remaining[job_id] = job
                        continue

                    detail = self._parse_detail(response.text)
                    if detail is None:
                        LOGGER.warning(
                            "ChileTrabajos job %s has no JobPosting data; skipping",
                            job_id,
                        )
                        error_count += 1
                        continue

                    built = self._job_from_detail(
                        job,
                        detail,
                        self._detail_logo_url(response.text),
                    )
                    if built is None:
                        LOGGER.warning(
                            "ChileTrabajos job %s has no parseable date; skipping",
                            job_id,
                        )
                        error_count += 1
                        continue

                    detailed_job, posted = built
                    self._store_detail(job_id, detailed_job, posted)
                    if self._is_recent(posted, cutoff):
                        enriched.append(detailed_job)
                    else:
                        too_old_count += 1

                    await asyncio.sleep(
                        random.uniform(*self.DETAIL_DELAY_RANGE)
                    )

                now = time.monotonic()
                if remaining:
                    deferred_items = {
                        deferred_id: (
                            now + self.DEFERRED_TTL_SECONDS,
                            deferred_job,
                        )
                        for deferred_id, deferred_job in remaining.items()
                    }
                    if len(deferred_items) > self.DEFERRED_LIMIT:
                        deferred_items = dict(
                            list(deferred_items.items())[: self.DEFERRED_LIMIT]
                        )
                    self._deferred[context] = deferred_items
                else:
                    self._deferred.pop(context, None)
                self._prune_deferred(now)
        except httpx.HTTPError:
            LOGGER.exception("ChileTrabajos request failed")
            return []

        LOGGER.info(
            "ChileTrabajos cycle: keywords=%d pages=%d found=%d unique=%d "
            "known=%d cached=%d fetched=%d deferred=%d too_old=%d "
            "expired=%d errors=%d",
            len(terms),
            pages_fetched,
            found_count,
            len(candidates),
            known_count,
            cached_count,
            fetched_count,
            len(remaining),
            too_old_count,
            expired_count,
            error_count,
        )
        return enriched

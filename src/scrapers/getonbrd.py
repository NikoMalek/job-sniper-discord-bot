"""Get on Board public API scraper."""

from __future__ import annotations

import asyncio
import json
import logging
import math
import random
import time
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from urllib.parse import urljoin, urlsplit

import httpx
from bs4 import BeautifulSoup

from ..countries import Country
from ..locations import LocationIntent, normalize, parse_location_or_none
from .base import Job, PortalPlan
from .http import HttpScraper

LOGGER = logging.getLogger(__name__)

BASE_URL = "https://www.getonbrd.com"
SEARCH_PATH = "/api/v0/search/jobs"
SEARCH_URL = f"{BASE_URL}{SEARCH_PATH}"
# The API expects ``expand`` as a JSON array string (a repeated ``expand[]``
# parameter also works, but the retry helper only accepts scalar values).
EXPAND_PARAM = json.dumps(
    [
        "company",
        "tags",
        "location_cities",
        "location_regions",
        "location_tenants",
    ],
    separators=(",", ":"),
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

_RELATION_KEYS = (
    "description",
    "functions",
    "desirable",
    "benefits",
    "projects",
)


@dataclass(frozen=True, slots=True)
class _BoardJob:
    """A job plus the location fields needed to filter it locally."""

    job: Job
    countries: tuple[str, ...]
    tenants: tuple[str, ...]
    cities: tuple[str, ...]
    remote: bool


class GetOnBoardScraper(HttpScraper):
    slug = "getonbrd"
    source = "Get on Board"
    # The public search endpoint has no country/city parameter, so results are
    # filtered locally. Jobs are posted from many countries, not only Chile.
    COVERED_COUNTRIES = None

    # Try the public API directly first and fall back to the Worker when the
    # direct path is blocked or fails temporarily (429/5xx/network error).
    PROXY_MODE = "fallback"
    PROXY_HOSTS = ("www.getonbrd.com",)

    PAGE_SIZE = 50
    MAX_PAGES_PER_KEYWORD = 3
    SEARCH_PAGE_DELAY_RANGE = (0.4, 0.9)
    SEARCH_CACHE_TTL_SECONDS = 4 * 60
    SEARCH_CACHE_LIMIT = 32
    USER_AGENT = (
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
        "(KHTML, like Gecko) Chrome/131.0 Safari/537.36"
    )

    def __init__(self) -> None:
        self._search_cache: dict[
            tuple[tuple[str, ...], int],
            tuple[float, dict[str, _BoardJob], int, int],
        ] = {}
        self._search_locks: dict[
            tuple[tuple[str, ...], int], asyncio.Lock
        ] = {}

    def plan(self, intent: LocationIntent) -> PortalPlan:
        if intent.kind == "worldwide":
            return PortalPlan(
                True,
                "Worldwide",
                note=(
                    "sin filtro geográfico ni de modalidad: incluye ofertas "
                    "remotas y presenciales."
                ),
            )
        if intent.kind == "remote":
            if intent.country is None:
                return PortalPlan(
                    True,
                    "Remoto",
                    note="solo ofertas remotas de cualquier país.",
                )
            return PortalPlan(
                True,
                f"Remoto, {intent.country.name_es}",
                note=(
                    "solo ofertas remotas disponibles para ese país; la API "
                    "no distingue país en las remotas globales."
                ),
            )
        if intent.kind == "country" and intent.country is not None:
            return PortalPlan(
                True,
                intent.country.name_es,
                note=(
                    "incluye ofertas presenciales del país y remotas "
                    "disponibles para él."
                ),
            )
        if intent.country is None:
            return PortalPlan(
                True,
                intent.city or "Worldwide",
                note="la API no filtra por ubicación; se buscan todas las ofertas.",
            )
        return PortalPlan(
            True,
            intent.label,
            note=(
                f"la API no filtra por ciudad; se muestran ofertas de "
                f"{intent.city or intent.country.name_es}, las que no informan "
                f"ciudad y las remotas disponibles en {intent.country.name_es}."
            ),
        )

    @staticmethod
    def _html_text(value: object) -> str:
        if not isinstance(value, str) or not value.strip():
            return ""
        return " ".join(
            BeautifulSoup(value, "html.parser").get_text(" ", strip=True).split()
        )

    @staticmethod
    def _relation_names(value: object) -> tuple[str, ...]:
        if not isinstance(value, dict):
            return ()
        data = value.get("data")
        if isinstance(data, dict):
            data = [data]
        if not isinstance(data, list):
            return ()
        names: list[str] = []
        for item in data:
            if not isinstance(item, dict):
                continue
            attributes = item.get("attributes")
            if not isinstance(attributes, dict):
                continue
            name = attributes.get("name")
            if isinstance(name, str) and name.strip():
                names.append(" ".join(name.split()))
        return tuple(dict.fromkeys(names))

    @staticmethod
    def _published_at(value: object) -> datetime | None:
        if isinstance(value, bool):
            return None
        try:
            timestamp = int(value)  # type: ignore[arg-type]
        except (TypeError, ValueError):
            return None
        if timestamp <= 0:
            return None
        return datetime.fromtimestamp(timestamp, tz=timezone.utc)

    @staticmethod
    def _format_published(value: datetime) -> str:
        return (
            f"{value.day} {_MONTH_ABBREVIATIONS[value.month - 1]} "
            f"{value.year}, {value:%H:%M}"
        )

    @classmethod
    def _company(cls, attributes: dict) -> tuple[str, str]:
        company = attributes.get("company")
        if not isinstance(company, dict):
            return "No especificada", ""
        data = company.get("data")
        if not isinstance(data, dict):
            return "No especificada", ""
        company_attributes = data.get("attributes")
        if not isinstance(company_attributes, dict):
            return "No especificada", ""
        name = company_attributes.get("name")
        logo = company_attributes.get("logo")
        clean_name = name.strip() if isinstance(name, str) else ""
        clean_logo = logo.strip() if isinstance(logo, str) else ""
        if clean_logo:
            clean_logo = urljoin(BASE_URL, clean_logo)
            if urlsplit(clean_logo).scheme not in {"http", "https"}:
                clean_logo = ""
        return clean_name or "No especificada", clean_logo

    @classmethod
    def _description(cls, attributes: dict) -> str:
        sections: list[str] = []
        for key in _RELATION_KEYS:
            text = cls._html_text(attributes.get(key))
            if text:
                sections.append(text)
        tags = cls._relation_names(attributes.get("tags"))
        if tags:
            sections.append(f"Tecnologías: {', '.join(tags)}")
        return "\n\n".join(sections)

    @staticmethod
    def _location_text(
        remote: bool,
        countries: tuple[str, ...],
        tenants: tuple[str, ...],
        cities: tuple[str, ...],
        regions: tuple[str, ...],
        remote_zone: object,
    ) -> str:
        if remote:
            zone = remote_zone.strip() if isinstance(remote_zone, str) else ""
            detail = zone or ", ".join(tenants or regions) or ", ".join(
                country
                for country in countries
                if normalize(country) not in {"remote", "remoto"}
            )
            return f"Remoto · {detail}" if detail else "Remoto"
        if cities:
            return (
                f"{', '.join(cities)} · {', '.join(countries)}"
                if countries
                else ", ".join(cities)
            )
        if regions:
            return ", ".join(regions)
        if countries:
            return ", ".join(countries)
        return "No especificada"

    @classmethod
    def _job_from_item(cls, item: object) -> _BoardJob | None:
        if not isinstance(item, dict):
            return None
        job_id = item.get("id")
        attributes = item.get("attributes")
        if not isinstance(job_id, str) or not job_id.strip():
            return None
        if not isinstance(attributes, dict):
            return None

        title = attributes.get("title")
        if not isinstance(title, str) or not title.strip():
            return None
        title = " ".join(title.split())

        published = cls._published_at(attributes.get("published_at"))
        if published is None:
            return None

        links = item.get("links")
        url = links.get("public_url") if isinstance(links, dict) else ""
        if not isinstance(url, str) or urlsplit(url).scheme not in {"http", "https"}:
            url = f"{BASE_URL}/jobs/{job_id.strip()}"

        countries = tuple(
            value.strip()
            for value in attributes.get("countries") or ()
            if isinstance(value, str) and value.strip()
        )
        tenants = cls._relation_names(attributes.get("location_tenants"))
        cities = cls._relation_names(attributes.get("location_cities"))
        regions = cls._relation_names(attributes.get("location_regions"))
        remote = bool(attributes.get("remote"))
        company, logo = cls._company(attributes)

        job = Job(
            id=job_id.strip(),
            title=title,
            company=company,
            location=cls._location_text(
                remote,
                countries,
                tenants,
                cities,
                regions,
                attributes.get("remote_zone"),
            ),
            url=url,
            source=cls.source,
            published_at=cls._format_published(published),
            company_logo_url=logo,
            description=cls._description(attributes),
            published_at_datetime=published,
        )
        return _BoardJob(
            job=job,
            countries=countries,
            tenants=tenants,
            cities=cities,
            remote=remote,
        )

    @classmethod
    def _parse_payload(cls, payload: object) -> list[_BoardJob]:
        if not isinstance(payload, dict):
            return []
        data = payload.get("data")
        if not isinstance(data, list):
            return []
        jobs: list[_BoardJob] = []
        for item in data:
            job = cls._job_from_item(item)
            if job is not None:
                jobs.append(job)
        return jobs

    @staticmethod
    def _serves_country(record: _BoardJob, country: Country) -> bool:
        # The API currently answers with English country names but the request
        # asks for Spanish, so accept both spellings.
        needles = {normalize(country.name_en), normalize(country.name_es)}
        if any(normalize(value) in needles for value in record.countries):
            return True
        if any(normalize(tenant) in needles for tenant in record.tenants):
            return True
        # A fully remote posting without geographic restrictions is available
        # from every country.
        return record.remote and not record.tenants

    @classmethod
    def _matches_location(
        cls, record: _BoardJob, intent: LocationIntent | None
    ) -> bool:
        if intent is None or intent.kind == "worldwide":
            return True
        if intent.kind == "remote":
            if not record.remote:
                return False
            if intent.country is None:
                return True
            return cls._serves_country(record, intent.country)

        country = intent.country
        if country is None:
            return True
        if intent.kind == "country":
            return cls._serves_country(record, country)

        city = normalize(intent.city)
        if city and any(normalize(value) == city for value in record.cities):
            return True
        if not record.cities and cls._serves_country(record, country):
            return True
        # City searches still include remote jobs available in that country.
        return record.remote and cls._serves_country(record, country)

    def _prune_search_cache(self, now: float) -> None:
        for cache_key, value in list(self._search_cache.items()):
            if value[0] <= now:
                self._search_cache.pop(cache_key, None)
        while len(self._search_cache) > self.SEARCH_CACHE_LIMIT:
            self._search_cache.pop(next(iter(self._search_cache)))

    async def _search_candidates(
        self,
        client: httpx.AsyncClient,
        terms: list[str],
    ) -> tuple[dict[str, _BoardJob], int, int, bool]:
        """Fetch the search pages for the supplied keywords."""
        candidates: dict[str, _BoardJob] = {}
        pages_fetched = 0
        found_count = 0
        complete = True

        for keyword in terms:
            if self._circuit_is_open():
                LOGGER.warning(
                    "Get on Board search circuit open; keeping %d jobs from "
                    "previous keywords",
                    len(candidates),
                )
                complete = False
                break
            for page in range(self.MAX_PAGES_PER_KEYWORD):
                params: dict[str, str | int] = {
                    "query": keyword,
                    "page": page + 1,
                    "per_page": self.PAGE_SIZE,
                    "lang": "es",
                    "expand": EXPAND_PARAM,
                }

                response = await self._request_with_retry(
                    client,
                    SEARCH_URL,
                    params=params,
                    retries=self.SEARCH_RETRIES,
                    label=f"search {keyword!r} page {page + 1}",
                )
                if response is None:
                    LOGGER.warning(
                        "Get on Board search stopped for %r at page %d",
                        keyword,
                        page + 1,
                    )
                    complete = False
                    break
                if not response.is_success:
                    LOGGER.warning(
                        "Get on Board search for %r returned %s on page %d",
                        keyword,
                        response.status_code,
                        page + 1,
                    )
                    complete = False
                    break

                try:
                    payload = response.json()
                except json.JSONDecodeError:
                    LOGGER.warning(
                        "Get on Board returned invalid JSON for %r on page %d",
                        keyword,
                        page + 1,
                    )
                    complete = False
                    break

                page_jobs = self._parse_payload(payload)
                data = payload.get("data") if isinstance(payload, dict) else None
                pages_fetched += 1
                found_count += len(data) if isinstance(data, list) else 0

                new_jobs = 0
                for record in page_jobs:
                    if record.job.id in candidates:
                        continue
                    candidates[record.job.id] = record
                    new_jobs += 1

                if not isinstance(data, list) or len(data) < self.PAGE_SIZE:
                    break
                if new_jobs == 0:
                    break
                if page + 1 < self.MAX_PAGES_PER_KEYWORD:
                    await asyncio.sleep(
                        random.uniform(*self.SEARCH_PAGE_DELAY_RANGE)
                    )

        return candidates, pages_fetched, found_count, complete

    async def _cached_candidates(
        self,
        client: httpx.AsyncClient,
        terms: list[str],
        context: tuple[tuple[str, ...], int],
    ) -> tuple[dict[str, _BoardJob], int, int]:
        """Share one search across guilds with the same query parameters."""
        now = time.monotonic()
        cached = self._search_cache.get(context)
        if cached is not None and cached[0] > now:
            return dict(cached[1]), cached[2], cached[3]

        lock = self._search_locks.setdefault(context, asyncio.Lock())
        async with lock:
            now = time.monotonic()
            cached = self._search_cache.get(context)
            if cached is not None and cached[0] > now:
                return dict(cached[1]), cached[2], cached[3]

            candidates, pages_fetched, found_count, complete = (
                await self._search_candidates(client, terms)
            )
            # Never cache an aborted search: an empty entry would hide jobs
            # from other guilds until the TTL expires.
            if complete:
                self._prune_search_cache(now)
                self._search_cache[context] = (
                    time.monotonic() + self.SEARCH_CACHE_TTL_SECONDS,
                    candidates,
                    pages_fetched,
                    found_count,
                )
            return dict(candidates), pages_fetched, found_count

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
            normalized = normalize(cleaned)
            if not cleaned or normalized in seen_terms:
                continue
            seen_terms.add(normalized)
            terms.append(cleaned)
        if not terms:
            return []

        intent = parse_location_or_none(location)
        known = known_job_ids or set()
        cutoff = datetime.now(timezone.utc) - timedelta(hours=max_age_hours)
        # Cache searches by whole hours so guilds with similar windows share
        # them; each guild still applies its exact cutoff afterwards.
        age_bucket = max(1, math.ceil(max_age_hours))
        context = (
            tuple(sorted(normalize(term) for term in terms)),
            age_bucket,
        )
        headers = {
            "User-Agent": self.USER_AGENT,
            "Accept": "application/json",
            "Accept-Language": "es-CL,es;q=0.9,en;q=0.8",
        }
        self._reset_transport_health()
        LOGGER.info("Get on Board transport: direct")

        try:
            async with httpx.AsyncClient(
                timeout=self._request_timeout(),
                follow_redirects=True,
                headers=headers,
            ) as client:
                records, pages_fetched, found_count = await self._cached_candidates(
                    client,
                    terms,
                    context,
                )
        except httpx.HTTPError:
            LOGGER.exception("Get on Board request failed")
            return []

        results: list[Job] = []
        known_count = 0
        too_old_count = 0
        location_skips = 0
        for record in records.values():
            job = record.job
            if job.id in known:
                known_count += 1
                continue
            published = job.published_at_datetime
            if published is None or published < cutoff:
                too_old_count += 1
                continue
            if not self._matches_location(record, intent):
                location_skips += 1
                continue
            results.append(job)

        LOGGER.info(
            "Get on Board cycle: keywords=%d pages=%d found=%d unique=%d "
            "known=%d too_old=%d location_skips=%d returned=%d",
            len(terms),
            pages_fetched,
            found_count,
            len(records),
            known_count,
            too_old_count,
            location_skips,
            len(results),
        )
        return results

"""Computrabajo scraper (multi-country).

Computrabajo publishes one subdomain per country (``cl.computrabajo.com``,
``ar.computrabajo.com``, ...). Search URLs follow the pattern
``/trabajo-de-<keyword>`` with an optional ``-en-<location-slug>`` suffix and
the ``?by=publicationtime`` ordering.

The location slug is never guessed from free text: every country has its own
exact region/city slugs, loaded from ``data/computrabajo_locations.json``. For
instance Chile needs ``-en-rmetropolitana-en-santiago-centro`` while México
uses the single-level ``-en-guadalajara``. Cards expose the relative
publication date, while the job detail page ships schema.org ``JobPosting``
data (description, company, logo and ``datePosted``).
"""

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
from dataclasses import dataclass, replace
from datetime import date, datetime, timedelta, timezone
from pathlib import Path
from urllib.parse import urljoin, urlsplit

import httpx
from bs4 import BeautifulSoup

from ..locations import LocationIntent
from .base import Job, PortalPlan
from .http import HttpScraper

LOGGER = logging.getLogger(__name__)

BASE_HOST = "computrabajo.com"
DATA_FILE = (
    Path(__file__).resolve().parent.parent / "data" / "computrabajo_locations.json"
)


@dataclass(frozen=True, slots=True)
class ComputrabajoLocation:
    """One exact slug accepted by Computrabajo as a location filter."""

    slug: str
    label: str
    region: str
    city: str
    aliases: tuple[str, ...]


@dataclass(frozen=True, slots=True)
class ComputrabajoCountry:
    """A Computrabajo subdomain and its location catalog."""

    code: str
    label: str
    subdomain: str
    aliases: tuple[str, ...]
    locations: tuple[ComputrabajoLocation, ...]


@dataclass(frozen=True, slots=True)
class ResolvedSearch:
    country: ComputrabajoCountry
    slug: str
    matched: bool

    @property
    def subdomain(self) -> str:
        return self.country.subdomain


def _normalize_catalog_text(value: object) -> str:
    normalized = unicodedata.normalize("NFKD", str(value).casefold())
    without_accents = "".join(
        character for character in normalized if not unicodedata.combining(character)
    )
    return " ".join(without_accents.split())


def _load_catalog() -> tuple[tuple[ComputrabajoCountry, ...], str]:
    """Load and validate the editable location catalog."""
    payload = json.loads(DATA_FILE.read_text(encoding="utf-8"))
    countries: list[ComputrabajoCountry] = []
    seen_codes: set[str] = set()
    seen_subdomains: set[str] = set()

    for raw_code, raw in payload["countries"].items():
        code = str(raw_code).upper()
        subdomain = str(raw["subdomain"]).strip().casefold()
        if code in seen_codes:
            raise ValueError(f"{DATA_FILE}: país duplicado: {code}")
        if not subdomain or "." in subdomain or subdomain in seen_subdomains:
            raise ValueError(
                f"{DATA_FILE}: subdominio inválido o duplicado: {subdomain!r}"
            )
        seen_codes.add(code)
        seen_subdomains.add(subdomain)

        locations: list[ComputrabajoLocation] = []
        seen_slugs: set[str] = set()
        for item in raw.get("locations", []):
            slug = str(item["slug"]).strip()
            if not slug or slug in seen_slugs:
                raise ValueError(
                    f"{DATA_FILE}: slug de ubicación inválido o duplicado "
                    f"en {code}: {slug!r}"
                )
            seen_slugs.add(slug)
            locations.append(
                ComputrabajoLocation(
                    slug=slug,
                    label=str(item["label"]),
                    region=str(item.get("region", "")),
                    city=str(item.get("city", "")),
                    aliases=tuple(
                        str(alias) for alias in item.get("aliases", []) if str(alias)
                    ),
                )
            )
        countries.append(
            ComputrabajoCountry(
                code=code,
                label=str(raw["label"]),
                subdomain=subdomain,
                aliases=tuple(
                    str(alias) for alias in raw.get("aliases", []) if str(alias)
                ),
                locations=tuple(locations),
            )
        )

    default = str(payload.get("default_country", "")).upper()
    if default not in seen_codes:
        raise ValueError(
            f"{DATA_FILE}: default_country {default!r} no está en el catálogo"
        )
    return tuple(countries), default


CATALOG, DEFAULT_COUNTRY = _load_catalog()
COUNTRIES_BY_CODE: dict[str, ComputrabajoCountry] = {
    country.code: country for country in CATALOG
}
COUNTRY_LABELS: dict[str, str] = {
    code: country.label for code, country in COUNTRIES_BY_CODE.items()
}
COUNTRY_CODES = frozenset(COUNTRIES_BY_CODE)

_COUNTRY_LOOKUP: dict[str, str] = {}
for _country in CATALOG:
    for _name in (_country.label, *_country.aliases):
        _COUNTRY_LOOKUP[_normalize_catalog_text(_name)] = _country.code
_COUNTRY_LOOKUP_KEYS = tuple(sorted(_COUNTRY_LOOKUP, key=len, reverse=True))

_LOCATION_LOOKUP: dict[tuple[str, str], ComputrabajoLocation] = {}
for _country in CATALOG:
    for _location in _country.locations:
        for _name in (_location.city, _location.label, *_location.aliases):
            if not _name:
                continue
            # City entries come first in the catalog; keep that priority so a
            # name such as "Lima" resolves to the city, not to the region.
            _LOCATION_LOOKUP.setdefault(
                (_country.code, _normalize_catalog_text(_name)), _location
            )

_MONTHS = {
    "ene": 1,
    "enero": 1,
    "feb": 2,
    "febrero": 2,
    "mar": 3,
    "marzo": 3,
    "abr": 4,
    "abril": 4,
    "may": 5,
    "mayo": 5,
    "jun": 6,
    "junio": 6,
    "jul": 7,
    "julio": 7,
    "ago": 8,
    "agosto": 8,
    "sep": 9,
    "septiembre": 9,
    "setiembre": 9,
    "oct": 10,
    "octubre": 10,
    "nov": 11,
    "noviembre": 11,
    "dic": 12,
    "diciembre": 12,
}

_BLOCK_MARKERS = (
    "captcha",
    "no soy un robot",
    "verifica que eres humano",
    "acceso denegado",
    "request blocked",
    "unusual traffic",
)

_REMOTE_WORDS = ("remoto", "remote", "teletrabajo", "home office", "hibrido")

#: Published by the portal itself when the card exposes a remote modality.
_REMOTE_SUFFIX = " (Remoto)"


class ComputrabajoScraper(HttpScraper):
    slug = "computrabajo"
    source = "Computrabajo"
    COVERED_COUNTRIES = COUNTRY_CODES
    user_agents = (
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
        "(KHTML, like Gecko) Chrome/131.0 Safari/537.36",
        "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 "
        "(KHTML, like Gecko) Version/18.1 Safari/605.1.15",
        "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
        "(KHTML, like Gecko) Chrome/131.0 Safari/537.36",
    )

    PAGE_SIZE = 20
    # Search results are ordered by publication time but still mix featured
    # cards, so a fixed number of pages is read and filtered by card date.
    MAX_PAGES_PER_KEYWORD = 3
    MAX_DETAILS_PER_CYCLE = 25
    DETAIL_DELAY_RANGE = (0.4, 1.0)
    SEARCH_PAGE_DELAY_RANGE = (0.6, 1.2)
    DETAIL_CACHE_TTL_SECONDS = 6 * 60 * 60
    DETAIL_CACHE_LIMIT = 500
    SEARCH_CACHE_TTL_SECONDS = 4 * 60
    DEFERRED_TTL_SECONDS = 3 * 60 * 60
    DEFERRED_LIMIT = 200
    DEFERRED_CONTEXT_LIMIT = 50
    PROXY_MODE = "fallback"
    PROXY_HOSTS = tuple(
        f"{country.subdomain}.{BASE_HOST}"
        for country in sorted(CATALOG, key=lambda item: item.code)
    )

    def __init__(self) -> None:
        self._detail_cache: dict[str, tuple[float, Job, datetime]] = {}
        self._search_cache: dict[
            tuple[tuple[str, ...], str, str],
            tuple[float, dict[str, Job], int, int],
        ] = {}
        self._search_locks: dict[
            tuple[tuple[str, ...], str, str], asyncio.Lock
        ] = {}
        self._deferred: dict[
            tuple[tuple[str, ...], str, str],
            dict[str, tuple[float, Job]],
        ] = {}

    def plan(self, intent: LocationIntent) -> PortalPlan:
        """Translate a location intent into an exact Computrabajo catalog slug."""
        if not self.covers(intent):
            return PortalPlan(False, reason=self.pause_reason(intent))

        country = COUNTRIES_BY_CODE[intent.country_code or DEFAULT_COUNTRY]
        note = ""
        if intent.country_code is None:
            note = (
                "Computrabajo filtra por ubicación geográfica, no por modalidad: "
                f"se buscará en {country.label} (`{country.subdomain}.{BASE_HOST}`)."
            )

        if intent.kind == "city":
            location = self._find_location(country, intent.city)
            if location is not None:
                return PortalPlan(
                    True,
                    f"{location.label}, {country.label}",
                    note=note,
                )
            note = (
                f"{note} No reconoció «{intent.city}» entre las ciudades "
                f"registradas de {country.label}; se buscará en todo el país."
            )

        return PortalPlan(True, country.label, note=note)

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

    @classmethod
    def _slug(cls, value: str) -> str:
        """Turn a keyword into its path segment (locations never use this)."""
        return re.sub(r"[^a-z0-9]+", "-", cls._normalize(value)).strip("-")

    @classmethod
    def _search_path(cls, keyword: str, slug: str) -> str:
        """Build a search path; ``slug`` is used verbatim from the catalog."""
        path = f"/trabajo-de-{cls._slug(keyword)}"
        return f"{path}-en-{slug}" if slug else path

    @staticmethod
    def _find_location(
        country: ComputrabajoCountry, value: str
    ) -> ComputrabajoLocation | None:
        """Find a catalog entry by label or alias, scoped to one country."""
        return _LOCATION_LOOKUP.get(
            (country.code, _normalize_catalog_text(value))
        )

    @staticmethod
    def _find_country(value: str) -> tuple[ComputrabajoCountry, str]:
        """Split a canonical label into ``(country, remaining location text)``."""
        normalized = _normalize_catalog_text(value)
        for key in _COUNTRY_LOOKUP_KEYS:
            if normalized == key:
                return COUNTRIES_BY_CODE[_COUNTRY_LOOKUP[key]], ""
            suffix = f", {key}"
            if normalized.endswith(suffix):
                rest = normalized[: -len(suffix)].strip()
                return COUNTRIES_BY_CODE[_COUNTRY_LOOKUP[key]], rest
        return COUNTRIES_BY_CODE[DEFAULT_COUNTRY], normalized

    @classmethod
    def resolve_search_location(cls, location: str) -> ResolvedSearch:
        """Resolve ``PortalPlan.search_location`` to an exact catalog slug."""
        country, rest = cls._find_country(location)
        if not rest:
            return ResolvedSearch(country, "", True)
        found = cls._find_location(country, rest)
        if found is not None:
            return ResolvedSearch(country, found.slug, True)
        return ResolvedSearch(country, "", False)

    @classmethod
    def _parse_relative_date(cls, text: str, now: datetime) -> datetime | None:
        normalized = cls._normalize(text)
        if not normalized:
            return None

        if "minuto" in normalized:
            match = re.search(r"(\d+)", normalized)
            return now - timedelta(minutes=int(match.group(1)) if match else 0)
        if "hora" in normalized:
            match = re.search(r"(\d+)", normalized)
            return now - timedelta(hours=int(match.group(1)) if match else 0)
        if "ayer" in normalized:
            return now - timedelta(days=1)
        if "hoy" in normalized:
            return now
        if "dia" in normalized:
            match = re.search(r"(\d+)", normalized)
            if match:
                return now - timedelta(days=int(match.group(1)))
            return None

        match = re.search(r"(\d{1,2})\s+de\s+([a-z]+)", normalized)
        if match is None:
            return None
        day = int(match.group(1))
        month = _MONTHS.get(match.group(2))
        if month is None:
            return None
        candidate = datetime(now.year, month, day, tzinfo=timezone.utc)
        if candidate > now:
            candidate = candidate.replace(year=now.year - 1)
        return candidate

    @classmethod
    def _parse_date_posted(cls, value: object) -> datetime | None:
        if not isinstance(value, str) or not value.strip():
            return None
        text = value.strip().replace("Z", "+00:00")
        parsed: datetime | None = None
        try:
            parsed = datetime.fromisoformat(text)
        except ValueError:
            try:
                parsed_date = date.fromisoformat(text[:10])
            except ValueError:
                return None
            parsed = datetime(
                parsed_date.year, parsed_date.month, parsed_date.day
            )
        if parsed is None:
            return None
        if parsed.tzinfo is None:
            parsed = parsed.replace(tzinfo=timezone.utc)
        return parsed

    @classmethod
    def _parse_cards(cls, html: str, base_url: str) -> list[Job]:
        soup = BeautifulSoup(html, "html.parser")
        now = datetime.now(timezone.utc)
        jobs: list[Job] = []
        seen_ids: set[str] = set()
        for card in soup.select("article.box_offer"):
            job_id = str(card.get("data-id") or "").strip()
            link = card.select_one("h2 a.js-o-link") or card.select_one("h2 a")
            if not job_id or job_id in seen_ids:
                continue
            if link is None or not link.get("href"):
                continue

            title = cls._text(link)
            if not title:
                continue

            url = urljoin(base_url, str(link["href"]).split("#")[0].strip())
            if urlsplit(url).scheme not in {"http", "https"}:
                continue

            company = cls._text(card.select_one("p.dFlex a"))
            location = cls._text(
                card.select_one("p.fs16.fc_base.mt5:not(.dFlex)")
            )
            modality = cls._text(card.select_one("div.fs13.mt15"))
            if "remoto" in modality.casefold() or "teletrabajo" in modality.casefold():
                location = (
                    f"{location}{_REMOTE_SUFFIX}"
                    if location
                    else "Remoto"
                )

            published = cls._text(card.select_one("p.fs13.fc_aux.mt15"))
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
                    published_at_datetime=cls._parse_relative_date(
                        published, now
                    ),
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
            items = (
                data.get("@graph")
                if isinstance(data, dict) and "@graph" in data
                else data
            )
            if isinstance(items, dict):
                items = [items]
            if not isinstance(items, list):
                continue
            for item in items:
                if not isinstance(item, dict):
                    continue
                item_type = item.get("@type")
                if item_type == "JobPosting" or (
                    isinstance(item_type, list) and "JobPosting" in item_type
                ):
                    return item
        return None

    @classmethod
    def _description_text(cls, value: object) -> str:
        if not isinstance(value, str) or not value.strip():
            return ""
        fragment = BeautifulSoup(cls._unescape(value), "html.parser")
        lines = [
            " ".join(line.split())
            for line in fragment.get_text("\n", strip=True).splitlines()
        ]
        return "\n".join(line for line in lines if line)

    @classmethod
    def _fallback_detail(cls, html: str) -> dict:
        """Extract what is available when the page has no ``JobPosting``.

        Computrabajo drops the structured data for some offers (for example
        ones the visitor already applied to). The visible page still exposes
        the description and a ``Company - Location`` header.
        """
        soup = BeautifulSoup(html, "html.parser")
        description = ""
        block = soup.select_one("div.box_detail div.mb40.pb40.bb1")
        if block is not None:
            description = "\n".join(
                text
                for text in (cls._text(paragraph) for paragraph in block.select("p"))
                if text
            )

        company = ""
        location = ""
        header = soup.select_one("main.detail_fs div.container p.fs16")
        if header is not None:
            parts = [
                part.strip()
                for part in cls._text(header).split(" - ")
                if part.strip()
            ]
            if len(parts) >= 2:
                company = parts[0]
                location = " - ".join(parts[1:])
        return {
            "description": description,
            "company": company,
            "location": location,
        }

    @classmethod
    def _job_from_detail(cls, card: Job, detail: dict) -> Job | None:
        posted = card.published_at_datetime or cls._parse_date_posted(
            detail.get("datePosted")
        )
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
        logo_url = cls._unescape(organization.get("logo"))
        if logo_url:
            logo_url = urljoin(f"https://{DEFAULT_COUNTRY}.{BASE_HOST}", logo_url)
            if urlsplit(logo_url).scheme not in {"http", "https"}:
                logo_url = ""

        locality = cls._unescape(address.get("addressLocality"))
        region = cls._unescape(address.get("addressRegion"))
        detail_location = ", ".join(
            value for value in (locality, region) if value
        )
        location = card.location
        if detail_location and "no especificada" in location.casefold():
            location = detail_location
        if str(detail.get("jobLocationType", "")).upper() == "TELECOMMUTE":
            if _REMOTE_SUFFIX.strip() not in location:
                location = (
                    f"{location}{_REMOTE_SUFFIX}"
                    if location and location != "No especificada"
                    else "Remoto"
                )

        description = cls._description_text(
            detail.get("description")
        ) or card.description

        return replace(
            card,
            title=title,
            company=company or "No especificada",
            location=location or "No especificada",
            company_logo_url=card.company_logo_url or logo_url,
            description=description,
            published_at=card.published_at,
            published_at_datetime=posted,
        )

    @staticmethod
    def _looks_blocked(html: str) -> bool:
        sample = html[:4000].casefold()
        return any(marker in sample for marker in _BLOCK_MARKERS)

    @classmethod
    def _parse_total(cls, html: str) -> int | None:
        match = re.search(r"Consulta las ([\d.]+) ofertas", html, re.IGNORECASE)
        if match is None:
            return None
        try:
            return int(match.group(1).replace(".", ""))
        except ValueError:
            return None

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

    def _prune_search_cache(self, now: float) -> None:
        for cache_key, value in list(self._search_cache.items()):
            if value[0] <= now:
                self._search_cache.pop(cache_key, None)

    @staticmethod
    def _is_recent(posted: datetime, cutoff: datetime) -> bool:
        return posted.astimezone(timezone.utc) >= cutoff

    async def _search_candidates(
        self,
        client: httpx.AsyncClient,
        terms: list[str],
        subdomain: str,
        slug: str,
    ) -> tuple[dict[str, Job], int, int, bool]:
        """Fetch the search pages for one keyword/subdomain/slug combination."""
        base_url = f"https://{subdomain}.{BASE_HOST}"
        candidates: dict[str, Job] = {}
        pages_fetched = 0
        found_count = 0
        complete = True

        for keyword in terms:
            if not self._slug(keyword):
                continue
            path = self._search_path(keyword, slug)
            max_pages = self.MAX_PAGES_PER_KEYWORD
            for page in range(1, max_pages + 1):
                if self._circuit_is_open():
                    LOGGER.warning(
                        "Computrabajo search circuit open; keeping %d jobs "
                        "from previous keywords",
                        len(candidates),
                    )
                    complete = False
                    break

                params: dict[str, str | int] = {"by": "publicationtime"}
                if page > 1:
                    params["p"] = page
                response = await self._request_with_retry(
                    client,
                    f"{base_url}{path}",
                    params=params,
                    retries=self.SEARCH_RETRIES,
                    label=f"search {keyword!r} page {page}",
                )
                if response is None:
                    LOGGER.warning(
                        "Computrabajo search stopped for %r at page %d",
                        keyword,
                        page,
                    )
                    complete = False
                    break
                if not response.is_success:
                    LOGGER.warning(
                        "Computrabajo search for %r returned %s on page %d",
                        keyword,
                        response.status_code,
                        page,
                    )
                    complete = False
                    break
                if self._looks_blocked(response.text):
                    LOGGER.warning(
                        "Computrabajo returned a challenge page for %r on "
                        "page %d",
                        keyword,
                        page,
                    )
                    complete = False
                    break

                page_jobs = self._parse_cards(response.text, base_url)
                pages_fetched += 1
                found_count += len(page_jobs)

                if page == 1:
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

                if page >= max_pages:
                    break
                if len(page_jobs) < self.PAGE_SIZE:
                    break
                if new_jobs == 0:
                    break
                await asyncio.sleep(
                    random.uniform(*self.SEARCH_PAGE_DELAY_RANGE)
                )

        return candidates, pages_fetched, found_count, complete

    async def _cached_candidates(
        self,
        client: httpx.AsyncClient,
        terms: list[str],
        subdomain: str,
        slug: str,
        context: tuple[tuple[str, ...], str, str],
    ) -> tuple[dict[str, Job], int, int]:
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
                await self._search_candidates(client, terms, subdomain, slug)
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
            normalized = self._normalize(cleaned)
            if not cleaned or normalized in seen_terms:
                continue
            seen_terms.add(normalized)
            terms.append(cleaned)
        if not terms:
            return []

        known = known_job_ids or set()
        now = datetime.now(timezone.utc)
        cutoff = now - timedelta(hours=max_age_hours)
        resolved = self.resolve_search_location(location)
        subdomain, slug = resolved.subdomain, resolved.slug
        if not resolved.matched:
            LOGGER.warning(
                "Computrabajo: location %r is not in the location catalog; "
                "searching all of %s",
                location,
                resolved.country.label,
            )
        headers = {
            "User-Agent": random.choice(self.user_agents),
            "Accept-Language": "es-LATAM,es;q=0.9,en;q=0.8",
        }
        self._reset_transport_health()
        proxy_mode = self._proxy_mode()
        LOGGER.info(
            "Computrabajo transport: %s (country=%s slug=%s)",
            f"proxy {self._proxy_base()} ({proxy_mode})"
            if proxy_mode != "off"
            else "direct",
            subdomain,
            slug or "-",
        )

        try:
            async with httpx.AsyncClient(
                timeout=self._request_timeout(),
                follow_redirects=True,
                headers=headers,
            ) as client:
                context = (
                    tuple(sorted(self._normalize(term) for term in terms)),
                    subdomain,
                    slug,
                )
                candidates, pages_fetched, found_count = (
                    await self._cached_candidates(
                        client, terms, subdomain, slug, context
                    )
                )
                known_count = sum(1 for job_id in candidates if job_id in known)

                deferred_items = self._deferred.get(context, {})
                deferred = [
                    (expires_at, job)
                    for job_id, (expires_at, job) in deferred_items.items()
                    if expires_at > time.monotonic()
                    and job_id not in known
                    and (
                        job.published_at_datetime is None
                        or self._is_recent(job.published_at_datetime, cutoff)
                    )
                ]
                pending: dict[str, Job] = {
                    job.id: job for _, job in deferred
                }
                too_old_count = 0
                for job in candidates.values():
                    if job.id in known or job.id in pending:
                        continue
                    posted = job.published_at_datetime
                    if posted is not None and not self._is_recent(posted, cutoff):
                        too_old_count += 1
                        continue
                    pending[job.id] = job

                enriched: list[Job] = []
                remaining: dict[str, Job] = {}
                cached_count = 0
                fetched_count = 0
                without_detail = 0
                expired_count = 0
                error_count = 0
                pending_ids = list(pending)

                for index, job_id in enumerate(pending_ids):
                    if self._circuit_is_open():
                        for deferred_id in pending_ids[index:]:
                            remaining[deferred_id] = pending[deferred_id]
                        break
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
                            "Computrabajo job %s has no JobPosting data; "
                            "using the visible page as fallback",
                            job_id,
                        )
                        without_detail += 1
                        fallback = self._fallback_detail(response.text)
                        enriched.append(
                            replace(
                                job,
                                description=(
                                    fallback["description"] or job.description
                                ),
                                company=fallback["company"] or job.company,
                                location=fallback["location"] or job.location,
                            )
                        )
                        continue

                    detailed_job = self._job_from_detail(job, detail)
                    if detailed_job is None:
                        LOGGER.warning(
                            "Computrabajo job %s has no parseable date; skipping",
                            job_id,
                        )
                        error_count += 1
                        continue

                    posted = detailed_job.published_at_datetime
                    if posted is None:
                        error_count += 1
                        continue
                    self._store_detail(job_id, detailed_job, posted)
                    if self._is_recent(posted, cutoff):
                        enriched.append(detailed_job)
                    else:
                        too_old_count += 1

                    await asyncio.sleep(
                        random.uniform(*self.DETAIL_DELAY_RANGE)
                    )

                now_monotonic = time.monotonic()
                if remaining:
                    deferred_items = {
                        deferred_id: (
                            now_monotonic + self.DEFERRED_TTL_SECONDS,
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
                self._prune_deferred(now_monotonic)
        except httpx.HTTPError:
            LOGGER.exception("Computrabajo request failed")
            return []

        LOGGER.info(
            "Computrabajo cycle: keywords=%d pages=%d found=%d unique=%d "
            "known=%d cached=%d fetched=%d without_detail=%d deferred=%d "
            "too_old=%d expired=%d errors=%d",
            len(terms),
            pages_fetched,
            found_count,
            len(candidates),
            known_count,
            cached_count,
            fetched_count,
            without_detail,
            len(remaining),
            too_old_count,
            expired_count,
            error_count,
        )
        return enriched

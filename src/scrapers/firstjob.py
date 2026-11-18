"""FirstJob scraper (multi-country, region-based).

FirstJob (``firstjob.me``) exposes a GET form at ``/ofertas`` that filters by
``country[]`` and ``region[]`` ids.  It has no cities or communes: every
country is filtered at country level and, where available, by region.

The exact ids live in ``data/firstjob_locations.json`` so they can be edited
without touching Python code.  "Centroamérica" is a single ``country[]=cam``
option with no regions; the ISO codes of the countries it covers are listed
in the same JSON and are mapped to that option.

FirstJob does not publish a publication date.  Duplicate control therefore
relies on ``known_job_ids`` plus a limited number of result pages, and
``published_at``/``published_at_datetime`` are left empty.
"""

from __future__ import annotations

import asyncio
import json
import logging
import random
import re
import time
import unicodedata
from dataclasses import dataclass, replace
from pathlib import Path
from urllib.parse import urljoin, urlsplit

import httpx
from bs4 import BeautifulSoup

from ..countries import COUNTRIES_BY_CODE
from ..locations import LocationIntent
from .base import Job, PortalPlan
from .http import HttpScraper

LOGGER = logging.getLogger(__name__)

BASE_HOST = "firstjob.me"
BASE_URL = f"https://{BASE_HOST}"
DATA_FILE = (
    Path(__file__).resolve().parent.parent / "data" / "firstjob_locations.json"
)


@dataclass(frozen=True, slots=True)
class FirstJobRegion:
    """One region id accepted by FirstJob's ``region[]`` filter."""

    id: str
    label: str
    aliases: tuple[str, ...]


@dataclass(frozen=True, slots=True)
class FirstJobCountry:
    """A country id and its region catalog."""

    code: str
    label: str
    country_id: str
    aliases: tuple[str, ...]
    regions: tuple[FirstJobRegion, ...]
    countries_covered: tuple[str, ...]
    note: str


@dataclass(frozen=True, slots=True)
class ResolvedSearch:
    country: FirstJobCountry
    region_id: str
    matched: bool


def _normalize(value: object) -> str:
    normalized = unicodedata.normalize("NFKD", str(value).casefold())
    without_accents = "".join(
        character for character in normalized if not unicodedata.combining(character)
    )
    return " ".join(without_accents.split())


def _load_catalog() -> tuple[
    tuple[FirstJobCountry, ...], str, dict[str, tuple[str, ...]]
]:
    """Load and validate the editable location catalog."""
    payload = json.loads(DATA_FILE.read_text(encoding="utf-8"))
    countries: list[FirstJobCountry] = []
    grouped_codes: dict[str, tuple[str, ...]] = {}
    seen_codes: set[str] = set()
    seen_ids: set[str] = set()

    for raw_code, raw in payload["countries"].items():
        code = str(raw_code).upper()
        grouped_codes[code] = tuple(
            str(value).upper() for value in raw.get("country_codes_covered", [])
        )
        country_id = str(raw["country_id"]).strip()
        if code in seen_codes:
            raise ValueError(f"{DATA_FILE}: país duplicado: {code}")
        if not country_id or country_id in seen_ids:
            raise ValueError(
                f"{DATA_FILE}: country_id inválido o duplicado: {country_id!r}"
            )
        seen_codes.add(code)
        seen_ids.add(country_id)

        regions: list[FirstJobRegion] = []
        seen_region_ids: set[str] = set()
        for item in raw.get("regions", []):
            region_id = str(item["id"]).strip()
            if not region_id or region_id in seen_region_ids:
                raise ValueError(
                    f"{DATA_FILE}: id de región inválido o duplicado en "
                    f"{code}: {region_id!r}"
                )
            seen_region_ids.add(region_id)
            regions.append(
                FirstJobRegion(
                    id=region_id,
                    label=str(item["label"]),
                    aliases=tuple(
                        str(alias) for alias in item.get("aliases", []) if str(alias)
                    ),
                )
            )
        countries.append(
            FirstJobCountry(
                code=code,
                label=str(raw["label"]),
                country_id=country_id,
                aliases=tuple(
                    str(alias) for alias in raw.get("aliases", []) if str(alias)
                ),
                regions=tuple(regions),
                countries_covered=tuple(
                    str(name) for name in raw.get("countries_covered", [])
                ),
                note=str(raw.get("note", "")),
            )
        )

    default = str(payload.get("default_country", "")).upper()
    if default not in seen_codes:
        raise ValueError(
            f"{DATA_FILE}: default_country {default!r} no está en el catálogo"
        )
    return tuple(countries), default, grouped_codes


CATALOG, DEFAULT_COUNTRY, _GROUPED_CODES = _load_catalog()
COUNTRIES_BY_CODE_CATALOG: dict[str, FirstJobCountry] = {
    country.code: country for country in CATALOG
}

# ISO codes covered directly plus those grouped into a single catalog entry
# (currently the seven Central American countries mapped to CAM).
_NAME_TO_CODE: dict[str, str] = {
    _normalize(country.name_es): country.code
    for country in COUNTRIES_BY_CODE.values()
}
_COUNTRY_TO_CATALOG: dict[str, str] = {}
for _country in CATALOG:
    _COUNTRY_TO_CATALOG[_country.code] = _country.code
    # Explicit ISO codes (editable in the JSON) take precedence over names.
    for _code in _GROUPED_CODES.get(_country.code, ()):
        if _code in COUNTRIES_BY_CODE:
            _COUNTRY_TO_CATALOG[_code] = _country.code
    for _name in _country.countries_covered:
        _code = _NAME_TO_CODE.get(_normalize(_name))
        if _code and _code not in _COUNTRY_TO_CATALOG:
            _COUNTRY_TO_CATALOG[_code] = _country.code

COUNTRY_CODES = frozenset(_COUNTRY_TO_CATALOG)

_COUNTRY_LOOKUP: dict[str, str] = {}
for _country in CATALOG:
    for _name in (_country.label, *_country.aliases):
        _COUNTRY_LOOKUP[_normalize(_name)] = _country.code
_COUNTRY_LOOKUP_KEYS = tuple(sorted(_COUNTRY_LOOKUP, key=len, reverse=True))

_REGION_LOOKUP: dict[tuple[str, str], FirstJobRegion] = {}
for _country in CATALOG:
    for _region in _country.regions:
        for _name in (_region.label, *_region.aliases):
            _REGION_LOOKUP.setdefault(
                (_country.code, _normalize(_name)), _region
            )

_BLOCK_MARKERS = (
    "captcha",
    "no soy un robot",
    "verifica que eres humano",
    "acceso denegado",
    "request blocked",
    "unusual traffic",
)

_REMOTE_SUFFIX = " (Remoto)"

#: Surfaced as ``PortalPlan.note`` so ``/list-portals`` (and the other plan
#: listings) explain why FirstJob ignores ``max_age_hours``. FirstJob exposes
#: no ``datePublished``/``created_at``/feed/Last-Modified value anywhere.
_NO_DATES_NOTE = (
    "FirstJob no publica fechas de publicación: no se aplica `max_age` y "
    "los duplicados se controlan por ID de aviso."
)


class FirstJobScraper(HttpScraper):
    slug = "firstjob"
    source = "FirstJob"
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
    # FirstJob does not expose dates: read a few pages and de-duplicate by id.
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
    PROXY_HOSTS = (BASE_HOST, f"www.{BASE_HOST}")

    def __init__(self) -> None:
        self._detail_cache: dict[str, tuple[float, Job]] = {}
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
        """Translate a location intent into FirstJob's country/region ids."""
        if not self.covers(intent):
            return PortalPlan(False, reason=self.pause_reason(intent))

        source_code = intent.country_code or DEFAULT_COUNTRY
        catalog_code = _COUNTRY_TO_CATALOG.get(source_code, DEFAULT_COUNTRY)
        country = COUNTRIES_BY_CODE_CATALOG[catalog_code]
        grouped = catalog_code != source_code
        note = ""
        if intent.country_code is None:
            note = (
                "FirstJob no filtra por modalidad: se buscará en "
                f"{country.label} por defecto."
            )
        elif grouped:
            source = COUNTRIES_BY_CODE.get(source_code)
            source_label = source.name_es if source is not None else source_code
            covered = ", ".join(country.countries_covered) or country.label
            note = (
                f"FirstJob agrupa {source_label} dentro de {country.label} y no "
                f"tiene regiones para esa opción; se buscará en toda la región "
                f"({covered})."
            )
        elif country.note:
            note = country.note

        if intent.kind == "city":
            region = self._find_region(country, intent.city)
            if region is not None:
                return PortalPlan(
                    True,
                    f"{region.label}, {country.label}",
                    note=self._with_no_dates_note(note),
                )
            if not grouped:
                if country.regions:
                    extra = (
                        f"No reconoció «{intent.city}» entre las regiones de "
                        f"{country.label}; se buscará en todo el país."
                    )
                else:
                    extra = (
                        f"FirstJob no ofrece regiones para {country.label}; "
                        "se buscará en todo el país."
                    )
                note = f"{note} {extra}".strip()

        return PortalPlan(True, country.label, note=self._with_no_dates_note(note))

    @staticmethod
    def _with_no_dates_note(note: str) -> str:
        """Keep the location note and append the portal-wide date warning."""
        return f"{note} {_NO_DATES_NOTE}".strip() if note else _NO_DATES_NOTE

    @staticmethod
    def _text(element: object) -> str:
        if element is None or not hasattr(element, "get_text"):
            return ""
        return " ".join(element.get_text(" ", strip=True).split())

    @staticmethod
    def _absolute_url(element: object, base_url: str) -> str:
        if element is None or not hasattr(element, "get"):
            return ""
        value = str(element.get("src") or element.get("data-src") or "").strip()
        if not value:
            return ""
        url = urljoin(base_url, value)
        return url if urlsplit(url).scheme in {"http", "https"} else ""

    @staticmethod
    def _find_region(
        country: FirstJobCountry, value: str
    ) -> FirstJobRegion | None:
        return _REGION_LOOKUP.get((country.code, _normalize(value)))

    @staticmethod
    def _find_country(value: str) -> tuple[FirstJobCountry, str]:
        normalized = _normalize(value)
        for key in _COUNTRY_LOOKUP_KEYS:
            if normalized == key:
                return COUNTRIES_BY_CODE_CATALOG[_COUNTRY_LOOKUP[key]], ""
            suffix = f", {key}"
            if normalized.endswith(suffix):
                rest = normalized[: -len(suffix)].strip()
                return COUNTRIES_BY_CODE_CATALOG[_COUNTRY_LOOKUP[key]], rest
        return COUNTRIES_BY_CODE_CATALOG[DEFAULT_COUNTRY], normalized

    @classmethod
    def resolve_search_location(cls, location: str) -> ResolvedSearch:
        """Resolve ``PortalPlan.search_location`` to an exact catalog id."""
        country, rest = cls._find_country(location)
        if not rest:
            return ResolvedSearch(country, "", True)
        region = cls._find_region(country, rest)
        if region is not None:
            return ResolvedSearch(country, region.id, True)
        return ResolvedSearch(country, "", False)

    @staticmethod
    def _search_params(
        keyword: str, country: FirstJobCountry, region_id: str, page: int
    ) -> dict[str, str | int]:
        """FirstJob expects ``country[]``/``region[]`` array-style keys."""
        params: dict[str, str | int] = {
            "title": keyword,
            "country[]": country.country_id,
        }
        if region_id:
            params["region[]"] = region_id
        if page > 1:
            params["page"] = page
        return params

    @classmethod
    def _parse_cards(cls, html: str, base_url: str) -> list[Job]:
        soup = BeautifulSoup(html, "html.parser")
        jobs: list[Job] = []
        seen_ids: set[str] = set()
        for card in soup.select(".card-job"):
            link = card.select_one("a[href*='/oferta/']")
            if link is None or not link.get("href"):
                continue
            match = re.search(r"/oferta/(\d+)", str(link["href"]))
            if match is None:
                continue
            job_id = match.group(1)
            if job_id in seen_ids:
                continue

            title = cls._text(card.select_one(".card-job-top--info-heading"))
            if not title:
                continue
            url = urljoin(base_url, str(link["href"]).split("#")[0].strip())
            if urlsplit(url).scheme not in {"http", "https"}:
                continue

            company = cls._text(card.select_one(".card-job-top--company"))
            location = cls._text(card.select_one(".card-job-top--location"))
            modality = cls._text(card.select_one(".card-job-bottom"))
            if "remoto" in modality.casefold() or "home office" in modality.casefold():
                if _REMOTE_SUFFIX.strip() not in location.casefold():
                    location = (
                        f"{location}{_REMOTE_SUFFIX}" if location else "Remoto"
                    )
            seen_ids.add(job_id)
            jobs.append(
                Job(
                    id=job_id,
                    title=title,
                    company=company or "No especificada",
                    location=location or "No especificada",
                    url=url,
                    source=cls.source,
                    published_at="",
                    company_logo_url=cls._absolute_url(
                        card.select_one(".card-job-top--image img"), base_url
                    ),
                    description=cls._text(
                        card.select_one(".card-job-description")
                    ),
                )
            )
        return jobs

    @classmethod
    def _description_text(cls, block: object) -> str:
        if block is None or not hasattr(block, "get_text"):
            return ""
        lines = [
            " ".join(line.split())
            for line in block.get_text("\n", strip=True).splitlines()
        ]
        return "\n".join(line for line in lines if line)

    @classmethod
    def _parse_detail(cls, html: str, card: Job) -> Job:
        """Enrich a card with the visible fields of the job detail page.

        FirstJob publishes neither JSON-LD nor a publication date, so the
        fallback is the only source of detail data.
        """
        soup = BeautifulSoup(html, "html.parser")
        title = cls._text(soup.select_one(".job-single-header h3")) or card.title
        company = cls._text(soup.select_one(".job-meta a.company")) or card.company
        location = (
            cls._text(soup.select_one(".job-meta span.location")) or card.location
        )
        blocks = [
            cls._description_text(block)
            for block in soup.select(".content-single.content-offer")
        ]
        description = "\n\n".join(block for block in blocks if block) or card.description
        logo = cls._absolute_url(
            soup.select_one(".avatar-sidebar img"), BASE_URL
        ) or card.company_logo_url
        return replace(
            card,
            title=title,
            company=company,
            location=location,
            company_logo_url=logo,
            description=description,
        )

    @staticmethod
    def _looks_blocked(html: str) -> bool:
        sample = html[:4000].casefold()
        return any(marker in sample for marker in _BLOCK_MARKERS)

    def _cached_detail(self, job_id: str) -> Job | None:
        entry = self._detail_cache.get(job_id)
        if entry is None:
            return None
        expires_at, job = entry
        if expires_at <= time.monotonic():
            self._detail_cache.pop(job_id, None)
            return None
        return job

    def _store_detail(self, job_id: str, job: Job) -> None:
        now = time.monotonic()
        if len(self._detail_cache) >= self.DETAIL_CACHE_LIMIT:
            expired = [
                key
                for key, (expires_at, _) in self._detail_cache.items()
                if expires_at <= now
            ]
            for key in expired:
                self._detail_cache.pop(key, None)
            while len(self._detail_cache) >= self.DETAIL_CACHE_LIMIT:
                self._detail_cache.pop(next(iter(self._detail_cache)))
        self._detail_cache[job_id] = (
            now + self.DETAIL_CACHE_TTL_SECONDS,
            job,
        )

    def _prune_deferred(self, now: float) -> None:
        for context, items in list(self._deferred.items()):
            alive = {
                job_id: value for job_id, value in items.items() if value[0] > now
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

    async def _search_candidates(
        self,
        client: httpx.AsyncClient,
        terms: list[str],
        country: FirstJobCountry,
        region_id: str,
    ) -> tuple[dict[str, Job], int, int, bool]:
        """Fetch the search pages for one keyword/country/region combination."""
        candidates: dict[str, Job] = {}
        pages_fetched = 0
        found_count = 0
        complete = True

        for keyword in terms:
            max_pages = self.MAX_PAGES_PER_KEYWORD
            for page in range(1, max_pages + 1):
                if self._circuit_is_open():
                    LOGGER.warning(
                        "FirstJob search circuit open; keeping %d jobs "
                        "from previous keywords",
                        len(candidates),
                    )
                    complete = False
                    break
                response = await self._request_with_retry(
                    client,
                    f"{BASE_URL}/ofertas",
                    params=self._search_params(keyword, country, region_id, page),
                    retries=self.SEARCH_RETRIES,
                    label=f"search {keyword!r} page {page}",
                )
                if response is None:
                    LOGGER.warning(
                        "FirstJob search stopped for %r at page %d", keyword, page
                    )
                    complete = False
                    break
                if not response.is_success:
                    LOGGER.warning(
                        "FirstJob search for %r returned %s on page %d",
                        keyword,
                        response.status_code,
                        page,
                    )
                    complete = False
                    break
                if self._looks_blocked(response.text):
                    LOGGER.warning(
                        "FirstJob returned a challenge page for %r on page %d",
                        keyword,
                        page,
                    )
                    complete = False
                    break

                page_jobs = self._parse_cards(response.text, BASE_URL)
                pages_fetched += 1
                found_count += len(page_jobs)
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
                await asyncio.sleep(random.uniform(*self.SEARCH_PAGE_DELAY_RANGE))

        return candidates, pages_fetched, found_count, complete

    async def _cached_candidates(
        self,
        client: httpx.AsyncClient,
        terms: list[str],
        country: FirstJobCountry,
        region_id: str,
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
                await self._search_candidates(client, terms, country, region_id)
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
            normalized = _normalize(cleaned)
            if not cleaned or normalized in seen_terms:
                continue
            seen_terms.add(normalized)
            terms.append(cleaned)
        if not terms:
            return []

        known = known_job_ids or set()
        resolved = self.resolve_search_location(location)
        country, region_id = resolved.country, resolved.region_id
        if not resolved.matched:
            LOGGER.warning(
                "FirstJob: location %r is not in the location catalog; "
                "searching all of %s",
                location,
                country.label,
            )
        headers = {
            "User-Agent": random.choice(self.user_agents),
            "Accept-Language": "es-LATAM,es;q=0.9,en;q=0.8",
        }
        self._reset_transport_health()
        proxy_mode = self._proxy_mode()
        LOGGER.info(
            "FirstJob transport: %s (country=%s region=%s)"
            " — no publication dates, max_age_hours=%s is not applied",
            f"proxy {self._proxy_base()} ({proxy_mode})"
            if proxy_mode != "off"
            else "direct",
            country.label,
            region_id or "-",
            max_age_hours,
        )

        try:
            async with httpx.AsyncClient(
                timeout=self._request_timeout(),
                follow_redirects=True,
                headers=headers,
            ) as client:
                context = (
                    tuple(sorted(_normalize(term) for term in terms)),
                    country.code,
                    region_id,
                )
                candidates, pages_fetched, found_count = (
                    await self._cached_candidates(
                        client, terms, country, region_id, context
                    )
                )
                known_count = sum(1 for job_id in candidates if job_id in known)

                deferred_items = self._deferred.get(context, {})
                deferred = {
                    job_id: job
                    for job_id, (expires_at, job) in deferred_items.items()
                    if expires_at > time.monotonic() and job_id not in known
                }
                pending: dict[str, Job] = {
                    job_id: job
                    for job_id, job in candidates.items()
                    if job_id not in known
                }
                pending.update(deferred)

                enriched: list[Job] = []
                remaining: dict[str, Job] = {}
                cached_count = 0
                fetched_count = 0
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
                        enriched.append(cached_detail)
                        continue

                    response = await self._request_with_retry(
                        client,
                        job.url,
                        retries=self.DETAIL_RETRIES,
                        label=f"job {job_id}",
                    )
                    fetched_count += 1
                    if response is None or not response.is_success:
                        error_count += 1
                        remaining[job_id] = job
                        continue

                    detailed_job = self._parse_detail(response.text, job)
                    self._store_detail(job_id, detailed_job)
                    enriched.append(detailed_job)
                    await asyncio.sleep(random.uniform(*self.DETAIL_DELAY_RANGE))

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
            LOGGER.exception("FirstJob request failed")
            return []

        LOGGER.info(
            "FirstJob cycle: keywords=%d pages=%d found=%d unique=%d "
            "known=%d cached=%d fetched=%d deferred=%d errors=%d",
            len(terms),
            pages_fetched,
            found_count,
            len(candidates),
            known_count,
            cached_count,
            fetched_count,
            len(remaining),
            error_count,
        )
        return enriched


__all__ = [
    "CATALOG",
    "COUNTRY_CODES",
    "DEFAULT_COUNTRY",
    "FirstJobScraper",
    "ResolvedSearch",
]

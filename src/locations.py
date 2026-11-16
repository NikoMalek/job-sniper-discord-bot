"""Location intents shared by every portal.

The bot accepts five formats: ``Worldwide``, ``Remoto``, ``Remoto, país``,
``País`` and ``Ciudad, país``.  This module turns the raw text into a
:class:`LocationIntent` that each scraper can translate to its own capabilities.

Chilean cities and their equivalences live in
``data/chiletrabajos_cities.json`` so they can be extended without touching
Python code.
"""

from __future__ import annotations

import json
import unicodedata
from dataclasses import dataclass
from pathlib import Path

from .countries import COUNTRIES, COUNTRIES_BY_CODE, COUNTRY_ALIASES, Country

MAX_LOCATIONS = 3
CHILE_CITIES_FILE = (
    Path(__file__).resolve().parent / "data" / "chiletrabajos_cities.json"
)

WORLDWIDE_WORDS = (
    "todo el mundo",
    "worldwide",
    "anywhere",
    "cualquier lugar",
    "global",
)

REMOTE_WORDS = (
    "remoto",
    "remote",
    "teletrabajo",
    "home office",
)

_UNKNOWN_REMOTE_WORDS = ("online", "virtual", "home", "remotamente")

LOCATION_FORMAT_HELP = (
    "Formatos válidos:\n"
    "• `Worldwide` — cualquier modalidad, sin filtro geográfico\n"
    "• `Remoto` — solo ofertas remotas en todo el mundo\n"
    "• `Remoto, país` — remoto en un país (ej. `Remoto, Chile`)\n"
    "• `País` — todo el país (ej. `Chile`, `España`)\n"
    "• `Ciudad, país` — ej. `Santiago, Chile`, `Madrid, España`\n"
    "También acepto una `ciudad` sola si es chilena conocida, pero el "
    "formato recomendado es `ciudad, país`."
)


class LocationError(ValueError):
    """Raised when a location string does not match the standard."""


@dataclass(frozen=True, slots=True)
class ChileanCity:
    name: str
    bucket: str
    region: str
    aliases: tuple[str, ...] = ()


#: Cities exposed by ChileTrabajos (option ``13``) plus the capital's communes
#: and the Valparaíso metro communes that the portal publishes under its
#: regional bucket.
def _load_chile_cities() -> tuple[ChileanCity, ...]:
    payload = json.loads(CHILE_CITIES_FILE.read_text(encoding="utf-8"))
    return tuple(
        ChileanCity(
            name=str(item["name"]),
            bucket=str(item["bucket"]),
            region=str(item["region"]),
            aliases=tuple(str(alias) for alias in item.get("aliases", ())),
        )
        for item in payload
    )


CHILE_CITIES: tuple[ChileanCity, ...] = _load_chile_cities()

CHILE = COUNTRIES_BY_CODE["CL"]


def normalize(value: str) -> str:
    """Lowercase, strip accents and collapse whitespace."""
    decomposed = unicodedata.normalize("NFKD", value.casefold())
    without_accents = "".join(
        character for character in decomposed if not unicodedata.combining(character)
    )
    return " ".join(without_accents.split())


def _display_case(value: str) -> str:
    particles = {"de", "del", "la", "las", "los", "y", "da", "do"}
    words = value.split()
    for index, word in enumerate(words):
        if index and word.casefold() in particles:
            words[index] = word.casefold()
        else:
            words[index] = word[:1].upper() + word[1:]
    return " ".join(words)


def _build_country_aliases() -> dict[str, str]:
    aliases: dict[str, str] = {}
    for country in COUNTRIES:
        for candidate in (country.name_es, country.name_en, country.code):
            aliases[normalize(candidate)] = country.code
    for alias, code in COUNTRY_ALIASES.items():
        aliases[normalize(alias)] = code
    return aliases


def _build_city_aliases() -> dict[str, tuple[str, ChileanCity]]:
    aliases: dict[str, tuple[str, ChileanCity]] = {}
    for city in CHILE_CITIES:
        for candidate in (city.name, *city.aliases):
            aliases[normalize(candidate)] = (candidate, city)
    return aliases


_COUNTRY_ALIASES = _build_country_aliases()
_CITY_ALIASES = _build_city_aliases()


def country_for(value: str) -> Country | None:
    code = _COUNTRY_ALIASES.get(normalize(value))
    return COUNTRIES_BY_CODE.get(code) if code else None


@dataclass(frozen=True, slots=True)
class LocationIntent:
    """A parsed location plus how it maps to each portal's vocabulary."""

    raw: str
    kind: str  # "worldwide" | "remote" | "country" | "city"
    country_code: str | None
    city: str = ""
    inferred_country: bool = False
    city_known: bool = False
    city_alias: bool = False
    chile_bucket: str = ""
    chile_bucket_city: str = ""

    @property
    def country(self) -> Country | None:
        if self.country_code is None:
            return None
        return COUNTRIES_BY_CODE.get(self.country_code)

    @property
    def is_chile(self) -> bool:
        return self.country_code == "CL"

    @property
    def label(self) -> str:
        if self.kind == "worldwide":
            return "Worldwide"
        if self.kind == "remote":
            if self.country is None:
                return "Remoto"
            return f"Remoto, {self.country.name_es}"
        if self.kind == "country" and self.country is not None:
            return self.country.name_es
        if self.country is not None:
            country_name = self.country.name_es
            if self.city.casefold().endswith(f"de {country_name}".casefold()):
                return self.city
            return f"{self.city}, {country_name}"
        return self.city


def _city_intent(
    raw: str,
    country: Country,
    city_text: str,
    *,
    inferred_country: bool = False,
) -> LocationIntent:
    if country.code == "CL":
        known = _CITY_ALIASES.get(normalize(city_text))
        if known is not None:
            display, bucket_city = known
            return LocationIntent(
                raw=raw,
                kind="city",
                country_code="CL",
                city=display,
                inferred_country=inferred_country,
                city_known=True,
                city_alias=normalize(display) != normalize(bucket_city.name),
                chile_bucket=bucket_city.bucket,
                chile_bucket_city=bucket_city.name,
            )
    return LocationIntent(
        raw=raw,
        kind="city",
        country_code=country.code,
        city=_display_case(city_text),
        inferred_country=inferred_country,
    )


def parse_location(value: str) -> LocationIntent:
    """Parse a location string or raise :class:`LocationError`."""
    raw = " ".join(value.split()).strip()
    if not raw:
        raise LocationError("Indica una ubicación, por ejemplo `Chile` o `Remoto`.")

    parts = [part.strip() for part in raw.split(",") if part.strip()]
    if not parts:
        raise LocationError("Indica una ubicación, por ejemplo `Chile` o `Remoto`.")

    if len(parts) == 1:
        token = normalize(parts[0])
        if token in WORLDWIDE_WORDS:
            return LocationIntent(raw=raw, kind="worldwide", country_code=None)
        if token in REMOTE_WORDS:
            return LocationIntent(raw=raw, kind="remote", country_code=None)
        if token in _UNKNOWN_REMOTE_WORDS:
            raise LocationError(
                f"No reconocí **{parts[0]}** como modalidad. "
                "Para buscar remoto escribe `Remoto` o `Remoto, país`; "
                "para cualquier modalidad en todo el mundo, `Worldwide`."
            )
        country = country_for(parts[0])
        if country is not None:
            return LocationIntent(
                raw=raw, kind="country", country_code=country.code
            )
        city = _CITY_ALIASES.get(token)
        if city is not None:
            display, bucket_city = city
            return LocationIntent(
                raw=raw,
                kind="city",
                country_code="CL",
                city=display,
                inferred_country=True,
                city_known=True,
                city_alias=normalize(display) != normalize(bucket_city.name),
                chile_bucket=bucket_city.bucket,
                chile_bucket_city=bucket_city.name,
            )
        raise LocationError(
            f"No reconocí **{parts[0]}** como país ni como ciudad chilena.\n"
            "Usa `ciudad, país` (ej. `Río Bueno, Chile`), `país` "
            "(ej. `Chile`), `Remoto` o `Worldwide`.\n"
            + LOCATION_FORMAT_HELP
        )

    first, last = parts[0], parts[-1]
    country = country_for(last)

    if country is None:
        first_country = country_for(first)
        if first_country is not None and len(parts) == 2:
            return _city_intent(raw, first_country, last)
        token = normalize(first)
        if token in REMOTE_WORDS or token in _UNKNOWN_REMOTE_WORDS:
            raise LocationError(
                f"No reconocí **{last}** como país. "
                "Usa `Remoto, país` con un país real (ej. `Remoto, Chile`) "
                "o solo `Remoto`."
            )
        raise LocationError(
            f"No reconocí **{last}** como país.\n"
            "Usa `ciudad, país` (ej. `Madrid, España`) o `país` "
            "(ej. `España`). Si es una ciudad chilena, escribe "
            "`ciudad, Chile`."
        )

    if normalize(first) in WORLDWIDE_WORDS:
        raise LocationError(
            "Para buscar en todo el mundo escribe solo `Worldwide`. "
            "Si quieres limitarlo a un país, usa `País` (ej. `Chile`)."
        )

    if normalize(first) in REMOTE_WORDS:
        return LocationIntent(raw=raw, kind="remote", country_code=country.code)

    return _city_intent(raw, country, first)


def parse_location_or_none(value: str) -> LocationIntent | None:
    try:
        return parse_location(value)
    except LocationError:
        return None


def chile_cities_by_region() -> dict[str, list[ChileanCity]]:
    grouped: dict[str, list[ChileanCity]] = {}
    for city in CHILE_CITIES:
        grouped.setdefault(city.region, []).append(city)
    return grouped


__all__ = [
    "CHILE",
    "CHILE_CITIES",
    "LOCATION_FORMAT_HELP",
    "MAX_LOCATIONS",
    "ChileanCity",
    "LocationError",
    "LocationIntent",
    "chile_cities_by_region",
    "country_for",
    "normalize",
    "parse_location",
    "parse_location_or_none",
]

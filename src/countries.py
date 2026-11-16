"""Country names used to interpret location settings.

The data lives in ``data/countries.json`` so it can be extended without
touching Python code. Edit that file to add countries or aliases.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path

DATA_FILE = Path(__file__).resolve().parent / "data" / "countries.json"


@dataclass(frozen=True, slots=True)
class Country:
    code: str
    name_es: str
    name_en: str


def _load() -> tuple[tuple[Country, ...], dict[str, str]]:
    payload = json.loads(DATA_FILE.read_text(encoding="utf-8"))
    countries = tuple(
        Country(
            code=str(item["code"]).upper(),
            name_es=str(item["name_es"]),
            name_en=str(item["name_en"]),
        )
        for item in payload["countries"]
    )

    codes = [country.code for country in countries]
    duplicates = sorted({code for code in codes if codes.count(code) > 1})
    if duplicates:
        raise ValueError(
            f"{DATA_FILE}: códigos de país duplicados: {', '.join(duplicates)}"
        )

    aliases = {
        str(alias): str(code).upper()
        for alias, code in payload.get("aliases", {}).items()
    }
    unknown = sorted(set(aliases.values()) - set(codes))
    if unknown:
        raise ValueError(
            f"{DATA_FILE}: alias apuntan a países inexistentes: "
            f"{', '.join(unknown)}"
        )
    return countries, aliases


COUNTRIES, COUNTRY_ALIASES = _load()
COUNTRIES_BY_CODE: dict[str, Country] = {
    country.code: country for country in COUNTRIES
}

__all__ = ["COUNTRIES", "COUNTRIES_BY_CODE", "COUNTRY_ALIASES", "Country"]

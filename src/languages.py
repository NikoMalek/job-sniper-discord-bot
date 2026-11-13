"""Language aliases and normalization shared by commands and filters."""

from __future__ import annotations

LANGUAGE_ALIASES: dict[str, str] = {
    "es": "spanish",
    "espanol": "spanish",
    "español": "spanish",
    "spanish": "spanish",
    "en": "english",
    "ingles": "english",
    "inglés": "english",
    "english": "english",
    "pt": "portuguese",
    "portugues": "portuguese",
    "portugués": "portuguese",
    "portuguese": "portuguese",
    "fr": "french",
    "frances": "french",
    "francés": "french",
    "french": "french",
    "de": "german",
    "aleman": "german",
    "alemán": "german",
    "german": "german",
}

SUPPORTED_LANGUAGES: frozenset[str] = frozenset(LANGUAGE_ALIASES.values())

#: Canonical language -> label shown in Discord. Insertion order is the display order.
LANGUAGE_LABELS: dict[str, str] = {
    "spanish": "español",
    "english": "inglés",
    "portuguese": "portugués",
    "french": "francés",
    "german": "alemán",
}


def normalize_language(value: str) -> str:
    """Return the canonical language name, or the cleaned value if unknown."""
    cleaned = " ".join(value.split()).strip().casefold()
    return LANGUAGE_ALIASES.get(cleaned, cleaned)


def parse_languages(raw: str) -> list[str]:
    """Parse a comma separated language list, keeping order and removing duplicates."""
    languages = [
        normalize_language(value) for value in raw.split(",") if value.strip()
    ]
    return list(dict.fromkeys(languages))

"""Within-cycle cross-portal duplicate detection.

The bot scrapes every enabled portal for a guild, then groups jobs that are the
same posting before notifying. Grouping deliberately does not persist across
cycles: if the same job appears on another portal later, it is notified again,
because that portal may be the one the reader cares about.
"""

from __future__ import annotations

import re
import unicodedata
from dataclasses import dataclass
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit

from .scrapers.base import Job

TITLE_JACCARD_THRESHOLD = 0.75
DESCRIPTION_CONTAINMENT_THRESHOLD = 0.60
DESCRIPTION_JACCARD_THRESHOLD = 0.30
STRONG_CONTAINMENT_THRESHOLD = 0.70
STRONG_JACCARD_THRESHOLD = 0.40
COMPANY_JACCARD_THRESHOLD = 0.80

TITLE_NOISE = frozenset(
    """
    remote remoto hibrido hybrid presencial onsite on-site
    urgent urgente hiring vacante oferta job
    """.split()
)

LEGAL_SUFFIXES = frozenset(
    """
    sa spa ltda limitada inc llc corp corporation ltd gmbh srl sas cia co company
    """.split()
)

STOPWORDS = frozenset(
    """
    a al algo algun alguna algunas alguno algunos ante antes aquel aquella
    aquellas aquellos aqui asi aun bajo bien cada como con contra cual cuales
    cualquier cuando de del desde donde dos el ella ellas ellos en entre era
    eramos eran eras eres es esa esas ese eso esos esta estaba estado estan
    estar estas este esto estos estoy fue fueron ha habia han hasta hay la las
    le les lo los mas me mi mis mucho muchos muy nada ni no nos nosotros o os
    otra otras otro otros para pero poco por porque que quien quienes se sea
    sean segun ser si sin sobre solo son su sus tambien tan tanto te tenemos
    tener ti tiene tienen todo todos tu tus un una uno unos usted ustedes y ya
    yo
    about above after again all also and any are because been before being
    below between both but can could did do does doing down during each few
    for from further had has have having here how into its itself just more
    most no nor not now off on once only or other our out over own same she
    should some such than that the their them then there these they this those
    through to too under until up very was we were what when where which while
    who whom why will with would you your
    empresa equipo buscamos requisitos funciones responsabilidades ofrecemos
    beneficios experiencia conocimientos trabajo puesto cargo persona
    postulante candidato candidata aplicar postular descripcion acerca
    company team role responsibilities requirements benefits experience skills
    knowledge working work position candidate apply description
    """.split()
)

TRACKING_PARAMS = frozenset(
    """
    utm_source utm_medium utm_campaign utm_term utm_content
    trk trackingid refid ref position pagenum originalsubdomain
    """.split()
)


@dataclass(frozen=True, slots=True)
class _Fingerprint:
    job: Job
    company_key: str
    title_key: str
    title_tokens: frozenset[str]
    description_tokens: frozenset[str]
    url_key: str


def _normalize(value: str) -> str:
    decomposed = unicodedata.normalize("NFKD", value.casefold())
    without_accents = "".join(
        character for character in decomposed if not unicodedata.combining(character)
    )
    cleaned = re.sub(r"[^a-z0-9+#]+", " ", without_accents)
    return " ".join(cleaned.split())


def _tokens(value: str, noise: frozenset[str] = frozenset()) -> list[str]:
    return [
        token
        for token in _normalize(value).split()
        if len(token) >= 2 and token not in STOPWORDS and token not in noise
    ]


def _company_key(company: str) -> str:
    tokens = _tokens(company)
    while tokens and tokens[-1] in LEGAL_SUFFIXES:
        tokens.pop()
    if tokens and tokens[0] == "the":
        tokens.pop(0)
    return " ".join(tokens)


def _title_key(title: str) -> str:
    return " ".join(_tokens(title, TITLE_NOISE))


def _description_tokens(description: str) -> frozenset[str]:
    return frozenset(_tokens(description))


def _url_key(url: str) -> str:
    cleaned = url.strip()
    try:
        parts = urlsplit(cleaned)
    except ValueError:
        return _normalize(cleaned)
    if not parts.scheme and not parts.netloc:
        return _normalize(cleaned)
    query = [
        (key, value)
        for key, value in parse_qsl(parts.query, keep_blank_values=True)
        if key.casefold() not in TRACKING_PARAMS
        and not key.casefold().startswith("utm_")
    ]
    return urlunsplit(
        (
            parts.scheme.casefold(),
            parts.netloc.casefold(),
            parts.path.rstrip("/") or "/",
            urlencode(query, doseq=True),
            "",
        )
    )


def _fingerprint(job: Job) -> _Fingerprint:
    return _Fingerprint(
        job=job,
        company_key=_company_key(job.company),
        title_key=_title_key(job.title),
        title_tokens=frozenset(_tokens(job.title, TITLE_NOISE)),
        description_tokens=_description_tokens(job.description),
        url_key=_url_key(job.url),
    )


def _jaccard(left: frozenset[str], right: frozenset[str]) -> float:
    if not left or not right:
        return 0.0
    return len(left & right) / len(left | right)


def _containment(left: frozenset[str], right: frozenset[str]) -> float:
    if not left or not right:
        return 0.0
    return len(left & right) / min(len(left), len(right))


def _companies_match(left: str, right: str) -> bool:
    if not left or not right:
        return False
    if left == right:
        return True
    shorter, longer = sorted((left, right), key=len)
    if len(shorter) >= 3 and re.search(
        rf"(?<!\w){re.escape(shorter)}(?!\w)", longer
    ):
        return True
    return _jaccard(frozenset(left.split()), frozenset(right.split())) >= COMPANY_JACCARD_THRESHOLD


def _is_duplicate(left: _Fingerprint, right: _Fingerprint) -> bool:
    if left.url_key and left.url_key == right.url_key:
        return True
    if not _companies_match(left.company_key, right.company_key):
        return False

    title_exact = bool(left.title_key) and left.title_key == right.title_key
    if not title_exact and _jaccard(left.title_tokens, right.title_tokens) < TITLE_JACCARD_THRESHOLD:
        return False

    if not left.description_tokens or not right.description_tokens:
        return title_exact

    containment = _containment(left.description_tokens, right.description_tokens)
    if containment < DESCRIPTION_CONTAINMENT_THRESHOLD:
        return False
    jaccard = _jaccard(left.description_tokens, right.description_tokens)
    if jaccard < DESCRIPTION_JACCARD_THRESHOLD:
        return False
    if title_exact:
        return True
    return (
        containment >= STRONG_CONTAINMENT_THRESHOLD
        and jaccard >= STRONG_JACCARD_THRESHOLD
    )


def group_jobs(jobs: list[Job] | tuple[Job, ...]) -> list[list[Job]]:
    """Group equivalent jobs found in the same guild and scraping cycle.

    Input order is preserved, so the first job of each group is the primary
    variant (normally the first portal configured for the guild).
    """
    items = list(jobs)
    if len(items) < 2:
        return [items] if items else []

    fingerprints = [_fingerprint(job) for job in items]
    parent = list(range(len(items)))

    def find(index: int) -> int:
        while parent[index] != index:
            parent[index] = parent[parent[index]]
            index = parent[index]
        return index

    def union(left: int, right: int) -> None:
        left_root = find(left)
        right_root = find(right)
        if left_root != right_root:
            parent[right_root] = left_root

    for left in range(len(items)):
        for right in range(left + 1, len(items)):
            if _is_duplicate(fingerprints[left], fingerprints[right]):
                union(left, right)

    groups: dict[int, list[Job]] = {}
    for index, job in enumerate(items):
        groups.setdefault(find(index), []).append(job)
    return list(groups.values())

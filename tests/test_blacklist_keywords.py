from src.bot.cogs.monitor import MonitorCog
from src.scrapers.base import Job


def make_job(title: str, description: str = "") -> Job:
    return Job(
        id="1",
        title=title,
        company="Acme",
        location="Santiago, Chile",
        url="https://example.com/jobs/1",
        source="LinkedIn",
        published_at="hace 1 hora",
        description=description,
    )


def test_matches_blacklisted_keyword_in_title_with_accents_and_case():
    job = make_job("Alumno en Práctica Desarrollador Python")
    assert MonitorCog._matches_blacklisted_keyword(job, ["practica"]) == "practica"
    assert MonitorCog._matches_blacklisted_keyword(job, ["PRÁCTICA"]) == "PRÁCTICA"


def test_blacklisted_keyword_ignores_description_only():
    job = make_job(
        "Desarrollador Backend Python",
        description="Trabajarás junto a un docente y apoyarás a un practicante.",
    )
    assert (
        MonitorCog._matches_blacklisted_keyword(job, ["docente", "practicante"])
        is None
    )


def test_blacklisted_keyword_respects_word_boundaries():
    job = make_job("Ingeniero de Software Internals")
    assert MonitorCog._matches_blacklisted_keyword(job, ["intern"]) is None


def test_expand_blacklist_keywords_without_aliases():
    expanded, auto_aliases = MonitorCog.expand_blacklist_keywords(
        ["Práctica", "docente", "práctica"], include_aliases=False
    )
    assert expanded == ["Práctica", "docente"]
    assert auto_aliases == []


def test_expand_blacklist_keywords_with_aliases():
    expanded, auto_aliases = MonitorCog.expand_blacklist_keywords(
        ["Práctica", "docente"], include_aliases=True
    )
    assert expanded[:2] == ["Práctica", "docente"]
    assert "practicante" in auto_aliases
    assert "pasante" in auto_aliases
    assert "intern" in auto_aliases
    assert "profesor" in auto_aliases
    assert "relator" in auto_aliases
    assert "practica" not in auto_aliases
    assert "docente" not in auto_aliases

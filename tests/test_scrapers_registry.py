import pytest

from src.locations import parse_location
from src.scrapers import SCRAPERS

EXPECTED = {"linkedin", "chiletrabajos", "computrabajo", "firstjob", "getonbrd"}


def test_registry_contains_all_portals():
    assert set(SCRAPERS) == EXPECTED


@pytest.mark.parametrize("slug", sorted(EXPECTED))
def test_scraper_identity(slug):
    scraper = SCRAPERS[slug]
    assert scraper.slug == slug
    assert scraper.source


@pytest.mark.parametrize("slug", ["linkedin", "getonbrd"])
def test_global_portals_cover_any_country(slug):
    assert SCRAPERS[slug].covers(parse_location("Chile"))
    assert SCRAPERS[slug].covers(parse_location("España"))


@pytest.mark.parametrize("slug", ["chiletrabajos", "computrabajo", "firstjob"])
def test_regional_portals_cover_chile_but_not_spain(slug):
    assert SCRAPERS[slug].covers(parse_location("Chile"))
    assert not SCRAPERS[slug].covers(parse_location("España"))

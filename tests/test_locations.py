import pytest

from src.locations import LocationError, parse_location, parse_location_or_none


@pytest.mark.parametrize(
    ("value", "kind", "country_code", "label"),
    [
        ("Chile", "country", "CL", "Chile"),
        ("Remoto", "remote", None, "Remoto"),
        ("Remoto, Chile", "remote", "CL", "Remoto, Chile"),
        ("Worldwide", "worldwide", None, "Worldwide"),
        ("Madrid, España", "city", "ES", "Madrid, España"),
        ("Santiago, Chile", "city", "CL", "Santiago, Chile"),
    ],
)
def test_parse_valid_locations(value, kind, country_code, label):
    intent = parse_location(value)
    assert intent.kind == kind
    assert intent.country_code == country_code
    assert intent.label == label


def test_chilean_city_without_country_is_inferred():
    intent = parse_location("Santiago")
    assert intent.kind == "city"
    assert intent.country_code == "CL"
    assert intent.inferred_country is True
    assert intent.city_known is True
    assert intent.is_chile


def test_explicit_country_is_not_marked_as_inferred():
    assert parse_location("Santiago, Chile").inferred_country is False


def test_whitespace_and_case_are_normalized():
    assert parse_location("  santiago  ,  chile ").label == "Santiago, Chile"


def test_country_first_order_is_accepted():
    intent = parse_location("Chile, Santiago")
    assert intent.city == "Santiago"
    assert intent.country_code == "CL"


@pytest.mark.parametrize(
    "value",
    ["", "   ", "Zzzz", "Worldwide, Chile", "Remoto, Narnia"],
)
def test_invalid_locations_raise(value):
    with pytest.raises(LocationError):
        parse_location(value)


def test_parse_location_or_none():
    assert parse_location_or_none("Zzzz") is None
    assert parse_location_or_none("Chile") is not None

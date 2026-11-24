from src.languages import normalize_language, parse_languages, tracking_text


def test_normalize_language_aliases_and_case():
    assert normalize_language("  ENGLISH ") == "english"
    assert normalize_language("Inglés") == "english"
    assert normalize_language("es") == "spanish"


def test_normalize_language_unknown_value_is_cleaned_not_rejected():
    assert normalize_language("Klingon") == "klingon"


def test_parse_languages_removes_duplicates_and_keeps_order():
    assert parse_languages("english, English, spanish,, ") == ["english", "spanish"]


def test_parse_languages_empty():
    assert parse_languages("") == []


def test_tracking_text_per_language_with_spanish_fallback():
    assert tracking_text("view_job", "english") == "🔗 View Job"
    assert tracking_text("view_job") == "🔗 Ver oferta"
    assert tracking_text("view_job", "klingon") == "🔗 Ver oferta"

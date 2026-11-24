from src.dedupe import group_jobs
from src.scrapers.base import Job

DESCRIPTION = (
    "Buscamos desarrollador backend con experiencia en Python, FastAPI y "
    "PostgreSQL para construir servicios de pagos en la nube AWS, con "
    "pruebas automatizadas, integracion continua y despliegues en contenedores."
)


def make_job(
    job_id: str,
    *,
    title: str = "Backend Developer Python",
    company: str = "Acme",
    source: str = "LinkedIn",
    url: str | None = None,
    description: str = "",
) -> Job:
    return Job(
        id=job_id,
        title=title,
        company=company,
        location="Santiago, Chile",
        url=url or f"https://example.com/{source.casefold()}/{job_id}",
        source=source,
        published_at="hace 1 hora",
        description=description,
    )


def test_empty_and_single():
    assert group_jobs([]) == []
    job = make_job("1")
    assert group_jobs([job]) == [[job]]


def test_same_url_ignoring_tracking_params_is_grouped():
    first = make_job("1", url="https://example.com/job/1?utm_source=x&trk=abc")
    second = make_job("2", source="Get on Board", url="https://example.com/job/1/")
    assert group_jobs([first, second]) == [[first, second]]


def test_same_job_on_two_portals_is_grouped():
    first = make_job("1", company="Acme SpA", description=DESCRIPTION)
    second = make_job(
        "2", company="Acme", source="Computrabajo", description=DESCRIPTION
    )
    assert group_jobs([first, second]) == [[first, second]]


def test_same_title_and_company_without_description_is_grouped():
    first = make_job("1")
    second = make_job("2", source="Computrabajo", title="Backend Developer Python (Remoto)")
    assert group_jobs([first, second]) == [[first, second]]


def test_different_companies_are_not_grouped():
    first = make_job("1", company="Acme", description=DESCRIPTION)
    second = make_job("2", company="Globex", source="Computrabajo", description=DESCRIPTION)
    assert group_jobs([first, second]) == [[first], [second]]


def test_different_titles_are_not_grouped():
    first = make_job("1")
    second = make_job("2", title="Diseñadora UX Senior", source="Computrabajo")
    assert group_jobs([first, second]) == [[first], [second]]


def test_input_order_is_preserved_and_first_job_is_primary():
    first = make_job("1")
    other = make_job("2", title="Analista de datos", company="Globex")
    duplicate = make_job("3", source="Computrabajo")
    groups = group_jobs([first, other, duplicate])
    assert groups == [[first, duplicate], [other]]

import pytest

from src.database import Database


@pytest.fixture
async def db(tmp_path):
    """Base de datos SQLite temporal y vacía para cada test."""
    database = Database(tmp_path / "test.db")
    await database.initialize()
    return database

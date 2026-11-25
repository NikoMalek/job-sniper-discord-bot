GUILD = 1
OTHER_GUILD = 2


async def test_keywords_are_unique_per_guild(db):
    assert await db.add_keyword(GUILD, "python") is True
    assert await db.add_keyword(GUILD, "python") is False
    assert await db.add_keyword(OTHER_GUILD, "python") is True
    assert (await db.get_guild_config(GUILD)).keywords == ["python"]


async def test_remove_keyword(db):
    await db.add_keyword(GUILD, "python")
    assert await db.remove_keyword(GUILD, "python") is True
    assert await db.remove_keyword(GUILD, "python") is False
    assert (await db.get_guild_config(GUILD)).keywords == []


async def test_locations_are_unique(db):
    assert await db.add_location(GUILD, "Santiago, Chile") is True
    assert await db.add_location(GUILD, "Santiago, Chile") is False
    assert await db.get_locations(GUILD) == ["Santiago, Chile"]


async def test_blacklisted_companies_and_languages(db):
    assert await db.add_blacklisted_company(GUILD, "Acme") is True
    assert await db.add_blacklisted_company(GUILD, "Acme") is False
    assert await db.add_language(GUILD, "english") is True
    assert await db.add_language(GUILD, "english") is False
    config = await db.get_guild_config(GUILD)
    assert config.blacklisted_companies == ["Acme"]
    assert config.languages == ["english"]


async def test_blacklisted_keywords_case_insensitive_and_monitor_configs(db):
    await db.set_channel(GUILD, 100)
    await db.add_keyword(GUILD, "python")
    assert await db.add_blacklisted_keyword(GUILD, "Práctica") is True
    assert await db.add_blacklisted_keyword(GUILD, "práctica") is False
    assert await db.add_blacklisted_keyword(GUILD, "docente") is True
    assert await db.add_blacklisted_keyword(OTHER_GUILD, "docente") is True

    config = await db.get_guild_config(GUILD)
    assert config.blacklisted_keywords == ["Práctica", "docente"]

    monitor_configs = await db.get_monitor_configs()
    assert len(monitor_configs) == 1
    assert monitor_configs[0].blacklisted_keywords == ["Práctica", "docente"]

    assert await db.remove_blacklisted_keyword(GUILD, "PRÁCTICA") is True
    assert await db.remove_blacklisted_keyword(GUILD, "PRÁCTICA") is False
    assert (await db.get_guild_config(GUILD)).blacklisted_keywords == ["docente"]


async def test_default_portal_and_add_remove(db):
    await db.add_keyword(GUILD, "python")  # create guild config
    assert await db.get_portals(GUILD) == ["linkedin"]
    assert await db.add_portal(GUILD, "getonbrd") is True
    assert await db.add_portal(GUILD, "getonbrd") is False
    assert await db.get_portals(GUILD) == ["getonbrd", "linkedin"]
    assert await db.remove_portal(GUILD, "getonbrd") is True


async def test_channel_and_max_age(db):
    await db.set_channel(GUILD, 99)
    await db.set_max_age_hours(GUILD, 48.0)
    config = await db.get_guild_config(GUILD)
    assert config.channel_id == 99
    assert config.max_age_hours == 48.0


async def test_unconfigured_guild_has_defaults(db):
    config = await db.get_guild_config(GUILD)
    assert config.channel_id is None
    assert config.max_age_hours == 24.0
    assert config.keywords == []


async def test_mark_notified_is_idempotent_and_guild_scoped(db):
    assert await db.mark_notified(GUILD, "j1", "linkedin") is True
    assert await db.mark_notified(GUILD, "j1", "linkedin") is False
    assert await db.get_notified_keys(GUILD) == {("linkedin", "j1")}
    assert await db.count_notified(GUILD) == 1
    assert await db.get_notified_keys(OTHER_GUILD) == set()


async def test_same_job_id_on_different_portals_is_distinct(db):
    assert await db.mark_notified(GUILD, "123", "linkedin") is True
    assert await db.mark_notified(GUILD, "123", "getonbrd") is True
    assert await db.count_notified(GUILD) == 2


async def test_remove_notified(db):
    await db.mark_notified(GUILD, "j1", "linkedin")
    await db.remove_notified(GUILD, "j1", "linkedin")
    assert await db.count_notified(GUILD) == 0


async def test_cleanup_keeps_recent_notifications(db):
    await db.mark_notified(GUILD, "j1", "linkedin")
    await db.cleanup_notified_jobs(25.0)
    assert await db.count_notified(GUILD) == 1

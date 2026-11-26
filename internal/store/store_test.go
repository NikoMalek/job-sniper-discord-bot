package store

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	guild      int64 = 1
	otherGuild int64 = 2
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open() unexpected error: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() unexpected error: %v", err)
		}
	})
	return store
}

func channelValue(config GuildMonitorConfig) *int64 {
	return config.ChannelID
}

func assertChannel(t *testing.T, config GuildMonitorConfig, want *int64) {
	t.Helper()
	if !reflect.DeepEqual(channelValue(config), want) {
		t.Fatalf("ChannelID = %v, want %v", channelValue(config), want)
	}
}

func TestKeywordsAreUniquePerGuild(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if added, err := store.AddKeyword(ctx, guild, "Python"); err != nil || !added {
		t.Fatalf("AddKeyword(Python) = %v, %v, want true, nil", added, err)
	}
	if added, err := store.AddKeyword(ctx, guild, "python"); err != nil || added {
		t.Fatalf("AddKeyword(python) = %v, %v, want false, nil", added, err)
	}
	if added, err := store.AddKeyword(ctx, otherGuild, "python"); err != nil || !added {
		t.Fatalf("AddKeyword(other guild) = %v, %v, want true, nil", added, err)
	}

	config, err := store.GetGuildConfig(ctx, guild)
	if err != nil {
		t.Fatalf("GetGuildConfig() unexpected error: %v", err)
	}
	if !reflect.DeepEqual(config.Keywords, []string{"python"}) {
		t.Fatalf("Keywords = %v, want [python]", config.Keywords)
	}
}

func TestRemoveKeyword(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if _, err := store.AddKeyword(ctx, guild, "python"); err != nil {
		t.Fatalf("AddKeyword() unexpected error: %v", err)
	}
	if removed, err := store.RemoveKeyword(ctx, guild, "PYTHON"); err != nil || !removed {
		t.Fatalf("RemoveKeyword(PYTHON) = %v, %v, want true, nil", removed, err)
	}
	if removed, err := store.RemoveKeyword(ctx, guild, "python"); err != nil || removed {
		t.Fatalf("RemoveKeyword(python) = %v, %v, want false, nil", removed, err)
	}
	config, err := store.GetGuildConfig(ctx, guild)
	if err != nil {
		t.Fatalf("GetGuildConfig() unexpected error: %v", err)
	}
	if len(config.Keywords) != 0 {
		t.Fatalf("Keywords = %v, want empty", config.Keywords)
	}
}

func TestLocationsAreUnique(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if added, err := store.AddLocation(ctx, guild, "Santiago, Chile"); err != nil || !added {
		t.Fatalf("AddLocation() = %v, %v, want true, nil", added, err)
	}
	if added, err := store.AddLocation(ctx, guild, "Santiago, Chile"); err != nil || added {
		t.Fatalf("AddLocation() duplicate = %v, %v, want false, nil", added, err)
	}
	locations, err := store.GetLocations(ctx, guild)
	if err != nil {
		t.Fatalf("GetLocations() unexpected error: %v", err)
	}
	if !reflect.DeepEqual(locations, []string{"Santiago, Chile"}) {
		t.Fatalf("GetLocations() = %v, want [Santiago, Chile]", locations)
	}
}

func TestBlacklistedCompaniesAndLanguages(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if added, err := store.AddBlacklistedCompany(ctx, guild, "Acme"); err != nil || !added {
		t.Fatalf("AddBlacklistedCompany(Acme) = %v, %v, want true, nil", added, err)
	}
	if added, err := store.AddBlacklistedCompany(ctx, guild, "acme"); err != nil || added {
		t.Fatalf("AddBlacklistedCompany(acme) = %v, %v, want false, nil", added, err)
	}
	if added, err := store.AddLanguage(ctx, guild, "English"); err != nil || !added {
		t.Fatalf("AddLanguage(English) = %v, %v, want true, nil", added, err)
	}
	if added, err := store.AddLanguage(ctx, guild, "english"); err != nil || added {
		t.Fatalf("AddLanguage(english) = %v, %v, want false, nil", added, err)
	}

	config, err := store.GetGuildConfig(ctx, guild)
	if err != nil {
		t.Fatalf("GetGuildConfig() unexpected error: %v", err)
	}
	if !reflect.DeepEqual(config.BlacklistedCompanies, []string{"acme"}) {
		t.Errorf("BlacklistedCompanies = %v, want [acme]", config.BlacklistedCompanies)
	}
	if !reflect.DeepEqual(config.Languages, []string{"english"}) {
		t.Errorf("Languages = %v, want [english]", config.Languages)
	}
}

func TestBlacklistedKeywordsCaseInsensitiveAndMonitorConfigs(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if err := store.SetChannel(ctx, guild, 100); err != nil {
		t.Fatalf("SetChannel() unexpected error: %v", err)
	}
	if _, err := store.AddKeyword(ctx, guild, "python"); err != nil {
		t.Fatalf("AddKeyword() unexpected error: %v", err)
	}
	if added, err := store.AddBlacklistedKeyword(ctx, guild, "Práctica"); err != nil || !added {
		t.Fatalf("AddBlacklistedKeyword(Práctica) = %v, %v, want true, nil", added, err)
	}
	if added, err := store.AddBlacklistedKeyword(ctx, guild, "práctica"); err != nil || added {
		t.Fatalf("AddBlacklistedKeyword(práctica) = %v, %v, want false, nil", added, err)
	}
	if added, err := store.AddBlacklistedKeyword(ctx, guild, "Docente"); err != nil || !added {
		t.Fatalf("AddBlacklistedKeyword(Docente) = %v, %v, want true, nil", added, err)
	}
	if added, err := store.AddBlacklistedKeyword(ctx, otherGuild, "docente"); err != nil || !added {
		t.Fatalf("AddBlacklistedKeyword(other guild) = %v, %v, want true, nil", added, err)
	}

	config, err := store.GetGuildConfig(ctx, guild)
	if err != nil {
		t.Fatalf("GetGuildConfig() unexpected error: %v", err)
	}
	wantBlocked := []string{"docente", "práctica"}
	if !reflect.DeepEqual(config.BlacklistedKeywords, wantBlocked) {
		t.Fatalf("BlacklistedKeywords = %v, want %v", config.BlacklistedKeywords, wantBlocked)
	}

	configs, err := store.GetMonitorConfigs(ctx)
	if err != nil {
		t.Fatalf("GetMonitorConfigs() unexpected error: %v", err)
	}
	if len(configs) != 1 || configs[0].GuildID != guild {
		t.Fatalf("GetMonitorConfigs() = %+v, want one config for guild 1", configs)
	}
	if !reflect.DeepEqual(configs[0].BlacklistedKeywords, wantBlocked) {
		t.Fatalf("monitor blocked keywords = %v, want %v", configs[0].BlacklistedKeywords, wantBlocked)
	}

	if removed, err := store.RemoveBlacklistedKeyword(ctx, guild, "PRÁCTICA"); err != nil || !removed {
		t.Fatalf("RemoveBlacklistedKeyword(PRÁCTICA) = %v, %v, want true, nil", removed, err)
	}
	if removed, err := store.RemoveBlacklistedKeyword(ctx, guild, "PRÁCTICA"); err != nil || removed {
		t.Fatalf("RemoveBlacklistedKeyword(PRÁCTICA) again = %v, %v, want false, nil", removed, err)
	}
	config, err = store.GetGuildConfig(ctx, guild)
	if err != nil {
		t.Fatalf("GetGuildConfig() unexpected error: %v", err)
	}
	if !reflect.DeepEqual(config.BlacklistedKeywords, []string{"docente"}) {
		t.Fatalf("BlacklistedKeywords = %v, want [docente]", config.BlacklistedKeywords)
	}
}

func TestDefaultPortalAndAddRemove(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if _, err := store.AddKeyword(ctx, guild, "python"); err != nil {
		t.Fatalf("AddKeyword() unexpected error: %v", err)
	}
	portals, err := store.GetPortals(ctx, guild)
	if err != nil {
		t.Fatalf("GetPortals() unexpected error: %v", err)
	}
	if !reflect.DeepEqual(portals, []string{"linkedin"}) {
		t.Fatalf("GetPortals() = %v, want [linkedin]", portals)
	}
	if added, err := store.AddPortal(ctx, guild, "getonbrd"); err != nil || !added {
		t.Fatalf("AddPortal() = %v, %v, want true, nil", added, err)
	}
	if added, err := store.AddPortal(ctx, guild, "getonbrd"); err != nil || added {
		t.Fatalf("AddPortal() duplicate = %v, %v, want false, nil", added, err)
	}
	portals, err = store.GetPortals(ctx, guild)
	if err != nil {
		t.Fatalf("GetPortals() unexpected error: %v", err)
	}
	if !reflect.DeepEqual(portals, []string{"getonbrd", "linkedin"}) {
		t.Fatalf("GetPortals() = %v, want [getonbrd linkedin]", portals)
	}
	if removed, err := store.RemovePortal(ctx, guild, "getonbrd"); err != nil || !removed {
		t.Fatalf("RemovePortal() = %v, %v, want true, nil", removed, err)
	}
}

func TestChannelAndMaxAge(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if err := store.SetChannel(ctx, guild, 99); err != nil {
		t.Fatalf("SetChannel() unexpected error: %v", err)
	}
	if err := store.SetMaxAgeHours(ctx, guild, 48.0); err != nil {
		t.Fatalf("SetMaxAgeHours() unexpected error: %v", err)
	}
	config, err := store.GetGuildConfig(ctx, guild)
	if err != nil {
		t.Fatalf("GetGuildConfig() unexpected error: %v", err)
	}
	wantChannel := int64(99)
	assertChannel(t, config, &wantChannel)
	if config.MaxAgeHours != 48.0 {
		t.Errorf("MaxAgeHours = %v, want 48", config.MaxAgeHours)
	}
}

func TestUnconfiguredGuildHasDefaults(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	config, err := store.GetGuildConfig(ctx, guild)
	if err != nil {
		t.Fatalf("GetGuildConfig() unexpected error: %v", err)
	}
	assertChannel(t, config, nil)
	if config.MaxAgeHours != MaxMaxAgeHours {
		t.Errorf("MaxAgeHours = %v, want %v", config.MaxAgeHours, MaxMaxAgeHours)
	}
	if len(config.Keywords) != 0 {
		t.Errorf("Keywords = %v, want empty", config.Keywords)
	}
	if config.Location() != DefaultLocation {
		t.Errorf("Location() = %q, want %q", config.Location(), DefaultLocation)
	}
}

func TestMarkNotifiedIsIdempotentAndGuildScoped(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if added, err := store.MarkNotified(ctx, guild, "j1", "linkedin"); err != nil || !added {
		t.Fatalf("MarkNotified() = %v, %v, want true, nil", added, err)
	}
	if added, err := store.MarkNotified(ctx, guild, "j1", "linkedin"); err != nil || added {
		t.Fatalf("MarkNotified() duplicate = %v, %v, want false, nil", added, err)
	}
	keys, err := store.GetNotifiedKeys(ctx, guild)
	if err != nil {
		t.Fatalf("GetNotifiedKeys() unexpected error: %v", err)
	}
	want := map[NotifiedKey]struct{}{{Source: "linkedin", JobID: "j1"}: {}}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("GetNotifiedKeys() = %v, want %v", keys, want)
	}
	if count, err := store.CountNotified(ctx, guild); err != nil || count != 1 {
		t.Fatalf("CountNotified() = %d, %v, want 1, nil", count, err)
	}
	otherKeys, err := store.GetNotifiedKeys(ctx, otherGuild)
	if err != nil {
		t.Fatalf("GetNotifiedKeys(other guild) unexpected error: %v", err)
	}
	if len(otherKeys) != 0 {
		t.Fatalf("GetNotifiedKeys(other guild) = %v, want empty", otherKeys)
	}
}

func TestSameJobIDOnDifferentPortalsIsDistinct(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if added, err := store.MarkNotified(ctx, guild, "123", "linkedin"); err != nil || !added {
		t.Fatalf("MarkNotified(linkedin) = %v, %v, want true, nil", added, err)
	}
	if added, err := store.MarkNotified(ctx, guild, "123", "getonbrd"); err != nil || !added {
		t.Fatalf("MarkNotified(getonbrd) = %v, %v, want true, nil", added, err)
	}
	if count, err := store.CountNotified(ctx, guild); err != nil || count != 2 {
		t.Fatalf("CountNotified() = %d, %v, want 2, nil", count, err)
	}
}

func TestRemoveNotified(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if _, err := store.MarkNotified(ctx, guild, "j1", "linkedin"); err != nil {
		t.Fatalf("MarkNotified() unexpected error: %v", err)
	}
	if err := store.RemoveNotified(ctx, guild, "j1", "linkedin"); err != nil {
		t.Fatalf("RemoveNotified() unexpected error: %v", err)
	}
	if count, err := store.CountNotified(ctx, guild); err != nil || count != 0 {
		t.Fatalf("CountNotified() = %d, %v, want 0, nil", count, err)
	}
}

func TestCleanupKeepsRecentNotifications(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if _, err := store.MarkNotified(ctx, guild, "j1", "linkedin"); err != nil {
		t.Fatalf("MarkNotified() unexpected error: %v", err)
	}
	if removed, err := store.CleanupNotifiedJobs(ctx, 25.0); err != nil || removed != 0 {
		t.Fatalf("CleanupNotifiedJobs() = %d, %v, want 0, nil", removed, err)
	}
	if count, err := store.CountNotified(ctx, guild); err != nil || count != 1 {
		t.Fatalf("CountNotified() = %d, %v, want 1, nil", count, err)
	}
}

func TestSetupAndExperimentalFlags(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if has, err := store.HasSetupMessage(ctx, guild); err != nil || has {
		t.Fatalf("HasSetupMessage() = %v, %v, want false, nil", has, err)
	}
	if err := store.MarkSetupMessageSent(ctx, guild); err != nil {
		t.Fatalf("MarkSetupMessageSent() unexpected error: %v", err)
	}
	if has, err := store.HasSetupMessage(ctx, guild); err != nil || !has {
		t.Fatalf("HasSetupMessage() = %v, %v, want true, nil", has, err)
	}
	if err := store.SetExperimentalEnabled(ctx, guild, true); err != nil {
		t.Fatalf("SetExperimentalEnabled() unexpected error: %v", err)
	}
	if enabled, err := store.IsExperimentalEnabled(ctx, guild); err != nil || !enabled {
		t.Fatalf("IsExperimentalEnabled() = %v, %v, want true, nil", enabled, err)
	}
	ids, err := store.GetExperimentalEnabledGuildIDs(ctx)
	if err != nil || !reflect.DeepEqual(ids, []int64{guild}) {
		t.Fatalf("GetExperimentalEnabledGuildIDs() = %v, %v, want [1]", ids, err)
	}
}

func TestLocationReplacementAndRemoval(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if _, err := store.AddLocation(ctx, guild, "Santiago, Chile"); err != nil {
		t.Fatalf("AddLocation() unexpected error: %v", err)
	}
	if _, err := store.AddLocation(ctx, guild, "Madrid, España"); err != nil {
		t.Fatalf("AddLocation() unexpected error: %v", err)
	}
	if removed, err := store.RemoveLocation(ctx, guild, "Santiago, Chile"); err != nil || !removed {
		t.Fatalf("RemoveLocation() = %v, %v, want true, nil", removed, err)
	}
	locations, err := store.GetLocations(ctx, guild)
	if err != nil {
		t.Fatalf("GetLocations() unexpected error: %v", err)
	}
	if !reflect.DeepEqual(locations, []string{"Madrid, España"}) {
		t.Fatalf("GetLocations() = %v, want [Madrid, España]", locations)
	}
	config, err := store.GetGuildConfig(ctx, guild)
	if err != nil {
		t.Fatalf("GetGuildConfig() unexpected error: %v", err)
	}
	if config.Location() != "Madrid, España" {
		t.Fatalf("Location() = %q, want Madrid, España", config.Location())
	}

	if err := store.SetLocation(ctx, guild, "Worldwide"); err != nil {
		t.Fatalf("SetLocation() unexpected error: %v", err)
	}
	locations, err = store.GetLocations(ctx, guild)
	if err != nil {
		t.Fatalf("GetLocations() unexpected error: %v", err)
	}
	if !reflect.DeepEqual(locations, []string{"Worldwide"}) {
		t.Fatalf("GetLocations() = %v, want [Worldwide]", locations)
	}
}

func TestSQLitePragmasAreEnabled(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	var journalMode string
	if err := store.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}

	for _, pragma := range []struct {
		name string
		want int
	}{
		{name: "foreign_keys", want: 1},
		{name: "busy_timeout", want: 5000},
	} {
		var value int
		if err := store.db.QueryRowContext(ctx, "PRAGMA "+pragma.name).Scan(&value); err != nil {
			t.Fatalf("read %s: %v", pragma.name, err)
		}
		if value != pragma.want {
			t.Errorf("%s = %d, want %d", pragma.name, value, pragma.want)
		}
	}
}

func TestSchemaIndexesExist(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	rows, err := store.db.QueryContext(ctx, `
		SELECT name FROM sqlite_master
		WHERE type = 'index' AND name IN (
			'idx_notified_jobs_created_at',
			'idx_job_interactions_guild_clicked',
			'idx_job_interactions_user_clicked',
			'idx_tracked_jobs_created_at')`)
	if err != nil {
		t.Fatalf("query indexes: %v", err)
	}
	defer rows.Close()

	names := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan index name: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate index names: %v", err)
	}
	if len(names) != 4 {
		t.Fatalf("indexes = %v, want all four schema indexes", names)
	}
}

func TestCleanupUsesDefaultRetention(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if _, err := store.MarkNotified(ctx, guild, "j1", "linkedin"); err != nil {
		t.Fatalf("MarkNotified() unexpected error: %v", err)
	}
	if removed, err := store.CleanupNotifiedJobs(ctx); err != nil || removed != 0 {
		t.Fatalf("CleanupNotifiedJobs() = %d, %v, want 0, nil", removed, err)
	}
	if count, err := store.CountNotified(ctx, guild); err != nil || count != 1 {
		t.Fatalf("CountNotified() = %d, %v, want 1, nil", count, err)
	}
	if _, err := store.CleanupNotifiedJobs(ctx, 25.0, 26.0); err == nil {
		t.Fatal("CleanupNotifiedJobs() with two hours arguments error = nil, want an error")
	}
}

package monitor

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/scrapers"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/store"
)

func TestNewAppliesDefaults(t *testing.T) {
	monitor, _ := setupMonitor(t, func(options *Options, _ *testDeps) {
		options.Interval = 0
		options.Coalesce = 0
		options.SendPause = 0
		options.Retention = 0
		options.MaxPortals = 0
		options.MaxFlushes = 0
	})
	if monitor.interval != DefaultInterval {
		t.Fatalf("interval = %v, want %v", monitor.interval, DefaultInterval)
	}
	if monitor.coalesce != DefaultCoalesceDelay {
		t.Fatalf("coalesce = %v, want %v", monitor.coalesce, DefaultCoalesceDelay)
	}
	if monitor.sendPause != DefaultSendPause {
		t.Fatalf("send pause = %v, want %v", monitor.sendPause, DefaultSendPause)
	}
	if monitor.retention != store.NotifiedRetentionHours {
		t.Fatalf("retention = %v, want %v", monitor.retention, store.NotifiedRetentionHours)
	}
	if got := cap(monitor.portalSem.tokens); got != DefaultPortalConcurrency {
		t.Fatalf("portal concurrency = %d, want %d", got, DefaultPortalConcurrency)
	}
	if got := cap(monitor.flushSem.tokens); got != DefaultFlushConcurrency {
		t.Fatalf("flush concurrency = %d, want %d", got, DefaultFlushConcurrency)
	}
}

func TestNewRequiresDependencies(t *testing.T) {
	ctx := context.Background()
	if _, err := New(ctx, Options{}); err == nil {
		t.Fatal("New() with empty options error = nil, want error")
	}
	options := Options{Store: newFakeStore()}
	if _, err := New(ctx, options); err == nil {
		t.Fatal("New() without portals error = nil, want error")
	}
	options.Portals = &fakePortalLookup{}
	if _, err := New(ctx, options); err == nil {
		t.Fatal("New() without channel resolver error = nil, want error")
	}
	options.Channels = &fakeChannelResolver{available: true}
	if _, err := New(ctx, options); err == nil {
		t.Fatal("New() without sender error = nil, want error")
	}
}

func TestRunStopsWhileWaitingForReady(t *testing.T) {
	monitor, _ := setupMonitor(t, func(options *Options, _ *testDeps) {
		options.Ready = make(chan struct{})
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := monitor.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v, want nil on cancellation", err)
	}
}

func TestRunOnceSchedulesPortalAndDeliversJobs(t *testing.T) {
	monitor, deps := setupMonitor(t, func(_ *Options, deps *testDeps) {
		portal := newFakePortal("linkedin", "LinkedIn")
		portal.jobs["Worldwide"] = []job.Job{
			jobRecord("job-1", "LinkedIn", "Python engineer", "Acme"),
		}
		deps.portals.portals = []Portal{portal}
		deps.store.configs = []store.GuildMonitorConfig{channelConfig(1, 10)}
	})
	monitor.RunOnce(context.Background())

	waitFor(t, "delivery", func() bool {
		channels, groups := deps.sender.snapshot()
		return len(channels) == 1 && len(groups) == 1
	})
	channels, groups := deps.sender.snapshot()
	if channels[0] != "10" {
		t.Fatalf("channel = %q, want 10", channels[0])
	}
	if ids := jobIDs(groups[0]); !reflect.DeepEqual(ids, []string{"job-1"}) {
		t.Fatalf("delivered jobs = %v, want [job-1]", ids)
	}
	portal := deps.portals.Get("linkedin").(*fakePortal)
	calls := portal.fetchCalls()
	if len(calls) != 1 {
		t.Fatalf("fetch calls = %#v, want one", calls)
	}
	if calls[0].location != "Worldwide" || calls[0].maxAgeHours != 24 {
		t.Fatalf("fetch call = %#v", calls[0])
	}
	if want := []string{"python"}; !reflect.DeepEqual(calls[0].keywords, want) {
		t.Fatalf("keywords = %v, want %v", calls[0].keywords, want)
	}
	if portal.beginCount() != 1 {
		t.Fatalf("begin cycle calls = %d, want 1", portal.beginCount())
	}
	if deps.channels.callCount() != 2 {
		t.Fatalf("channel checks = %d, want 2", deps.channels.callCount())
	}
	if deps.store.cleanupCalls != 2 {
		t.Fatalf("cleanup calls = %d, want 2", deps.store.cleanupCalls)
	}
	logs := deps.logs.String()
	for _, want := range []string{
		"Cycle start: guilds=1 running_portals=0 available_portals=linkedin",
		"Guild 1: scheduling portals=LinkedIn keywords=1 max_age=24.0h",
		"Portal LinkedIn location 'Worldwide' returned 1 jobs for guild 1",
		"Pruned 1 notification records older than 25 hours",
		"Cycle scheduled in 0.0s; portals keep running in the background",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs do not contain %q:\n%s", want, logs)
		}
	}
}

func TestRunOnceDoesNotRestartRunningPortal(t *testing.T) {
	block := make(chan struct{})
	monitor, deps := setupMonitor(t, func(_ *Options, deps *testDeps) {
		portal := newFakePortal("linkedin", "LinkedIn")
		portal.block = block
		portal.jobs["Worldwide"] = []job.Job{
			jobRecord("job-1", "LinkedIn", "Python engineer", "Acme"),
		}
		deps.portals.portals = []Portal{portal}
		deps.store.configs = []store.GuildMonitorConfig{channelConfig(1, 10)}
	})
	monitor.RunOnce(context.Background())
	portal := deps.portals.Get("linkedin").(*fakePortal)
	waitFor(t, "first fetch", func() bool { return len(portal.fetchCalls()) == 1 })

	monitor.RunOnce(context.Background())
	if got := len(portal.fetchCalls()); got != 1 {
		t.Fatalf("fetch calls after second cycle = %d, want 1", got)
	}
	if !strings.Contains(deps.logs.String(), "not restarting it") {
		t.Fatalf("logs do not mention the running portal:\n%s", deps.logs.String())
	}

	close(block)
	waitFor(t, "first delivery", func() bool {
		_, groups := deps.sender.snapshot()
		return len(groups) == 1
	})
	waitFor(t, "portal task cleanup", func() bool {
		return monitor.runningPortalCount() == 0
	})
	monitor.RunOnce(context.Background())
	waitFor(t, "third fetch", func() bool { return len(portal.fetchCalls()) == 2 })
}

func TestRunOnceBoundsPortalConcurrency(t *testing.T) {
	block := make(chan struct{})
	monitor, deps := setupMonitor(t, func(options *Options, deps *testDeps) {
		portal := newFakePortal("linkedin", "LinkedIn")
		portal.block = block
		portal.jobs["Remoto"] = []job.Job{
			jobRecord("remote", "LinkedIn", "Python engineer", "Acme"),
		}
		portal.jobs["Worldwide"] = []job.Job{
			jobRecord("worldwide", "LinkedIn", "Python developer", "Acme"),
		}
		deps.portals.portals = []Portal{portal}
		config := channelConfig(1, 10)
		config.Locations = []string{"Remoto", "Worldwide"}
		deps.store.configs = []store.GuildMonitorConfig{config}
		options.MaxPortals = 1
	})
	monitor.RunOnce(context.Background())
	portal := deps.portals.Get("linkedin").(*fakePortal)
	waitFor(t, "first blocked fetch", func() bool { return len(portal.fetchCalls()) == 1 })
	if got := portal.maxConcurrent(); got != 1 {
		t.Fatalf("max concurrent fetches = %d, want 1", got)
	}
	if got := len(portal.fetchCalls()); got != 1 {
		t.Fatalf("fetch calls while blocked = %d, want 1", got)
	}
	close(block)
	waitFor(t, "second fetch", func() bool { return len(portal.fetchCalls()) == 2 })
	if got := portal.maxConcurrent(); got != 1 {
		t.Fatalf("max concurrent fetches = %d, want 1", got)
	}
}

func TestRunOnceCancelsPortalTask(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitor, deps := setupMonitor(t, func(_ *Options, deps *testDeps) {
		portal := newFakePortal("linkedin", "LinkedIn")
		portal.blockUntilCancel = true
		deps.portals.portals = []Portal{portal}
		deps.store.configs = []store.GuildMonitorConfig{channelConfig(1, 10)}
	})
	monitor.RunOnce(ctx)
	portal := deps.portals.Get("linkedin").(*fakePortal)
	waitFor(t, "blocked fetch", func() bool { return len(portal.fetchCalls()) == 1 })
	cancel()
	waitFor(t, "portal task cleanup", func() bool {
		return monitor.runningPortalCount() == 0
	})
	channels, groups := deps.sender.snapshot()
	if len(channels) != 0 || len(groups) != 0 {
		t.Fatalf("delivery after cancellation = %q, %#v, want none", channels, groups)
	}
}

func TestRunOnceUsesFirstLocationAndOverwritesDuplicateKey(t *testing.T) {
	monitor, deps := setupMonitor(t, func(_ *Options, deps *testDeps) {
		portal := newFakePortal("linkedin", "LinkedIn")
		portal.jobs["Remoto"] = []job.Job{
			jobRecord("job-1", "LinkedIn", "Old Python job", "Acme"),
			jobRecord("job-1", "LinkedIn", "New Python job", "Acme"),
		}
		deps.portals.portals = []Portal{portal}
		config := channelConfig(1, 10)
		config.Locations = []string{"Remoto", "not a place", "Remoto"}
		deps.store.configs = []store.GuildMonitorConfig{config}
	})
	monitor.RunOnce(context.Background())
	waitFor(t, "delivery", func() bool {
		_, groups := deps.sender.snapshot()
		return len(groups) == 1
	})
	_, groups := deps.sender.snapshot()
	if titles := jobTitles(groups[0]); !reflect.DeepEqual(titles, []string{"New Python job"}) {
		t.Fatalf("delivered titles = %v, want [New Python job]", titles)
	}
	portal := deps.portals.Get("linkedin").(*fakePortal)
	calls := portal.fetchCalls()
	if len(calls) != 1 || calls[0].location != "Remoto" {
		t.Fatalf("fetch calls = %#v, want one Remoto call", calls)
	}
	if !strings.Contains(deps.logs.String(), "invalid location 'not a place'") {
		t.Fatalf("logs do not mention invalid location:\n%s", deps.logs.String())
	}
}

func TestRunOnceSkipsUnsupportedLocation(t *testing.T) {
	monitor, deps := setupMonitor(t, func(_ *Options, deps *testDeps) {
		portal := newFakePortal("linkedin", "LinkedIn")
		portal.plans["Worldwide"] = scrapers.PortalPlan{
			Supported: false,
			Reason:    "solo cubre Chile",
		}
		deps.portals.portals = []Portal{portal}
		deps.store.configs = []store.GuildMonitorConfig{channelConfig(1, 10)}
	})
	monitor.RunOnce(context.Background())
	waitFor(t, "portal task cleanup", func() bool {
		return monitor.runningPortalCount() == 0
	})
	portal := deps.portals.Get("linkedin").(*fakePortal)
	if len(portal.fetchCalls()) != 0 {
		t.Fatalf("fetch calls = %#v, want none", portal.fetchCalls())
	}
	_, groups := deps.sender.snapshot()
	if len(groups) != 0 {
		t.Fatalf("delivered groups = %#v, want none", groups)
	}
	logs := deps.logs.String()
	for _, want := range []string{
		"Portal LinkedIn paused for guild 1: solo cubre Chile",
		"Portal LinkedIn has no compatible location for guild 1 (locations=Worldwide)",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs do not contain %q:\n%s", want, logs)
		}
	}
}

func TestRunOnceSkipsPortalWhenChannelIsUnavailable(t *testing.T) {
	monitor, deps := setupMonitor(t, func(_ *Options, deps *testDeps) {
		portal := newFakePortal("linkedin", "LinkedIn")
		portal.jobs["Worldwide"] = []job.Job{
			jobRecord("job-1", "LinkedIn", "Python engineer", "Acme"),
		}
		deps.portals.portals = []Portal{portal}
		deps.channels.available = false
		deps.store.configs = []store.GuildMonitorConfig{channelConfig(1, 10)}
	})
	monitor.RunOnce(context.Background())
	waitFor(t, "portal task cleanup", func() bool {
		return monitor.runningPortalCount() == 0
	})
	portal := deps.portals.Get("linkedin").(*fakePortal)
	if len(portal.fetchCalls()) != 0 {
		t.Fatalf("fetch calls = %#v, want none", portal.fetchCalls())
	}
	if !strings.Contains(deps.logs.String(), "Configured channel 10 is unavailable") {
		t.Fatalf("logs do not mention unavailable channel:\n%s", deps.logs.String())
	}
}

func TestRunOnceLogsCleanupCounts(t *testing.T) {
	monitor, deps := setupMonitor(t, func(_ *Options, deps *testDeps) {
		deps.store.cleanupBoth = true
	})
	monitor.RunOnce(context.Background())
	logs := deps.logs.String()
	for _, want := range []string{
		"Pruned 2 notification records older than 25 hours",
		"Pruned 3 tracking tokens older than 25 hours",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs do not contain %q:\n%s", want, logs)
		}
	}
}

func TestRunOnceSkipsGuildWithoutChannelOrPortals(t *testing.T) {
	monitor, deps := setupMonitor(t, func(_ *Options, deps *testDeps) {
		config := channelConfig(1, 10)
		config.ChannelID = nil
		deps.store.configs = []store.GuildMonitorConfig{
			config,
			{GuildID: 2, ChannelID: int64Pointer(20), Portals: []string{"unknown"}},
		}
	})
	monitor.RunOnce(context.Background())
	logs := deps.logs.String()
	if !strings.Contains(logs, "Guild 2 has unknown portal 'unknown'; skipping it") {
		t.Fatalf("logs do not mention unknown portal:\n%s", logs)
	}
	if !strings.Contains(logs, "Guild 2 has no available portals; skipping cycle") {
		t.Fatalf("logs do not mention empty portals:\n%s", logs)
	}
}

func TestRegistryPortals(t *testing.T) {
	scraper := &fullFakeScraper{BaseScraper: scrapers.NewBaseScraper("linkedin", "LinkedIn", nil)}
	registry := scrapers.NewRegistry(scraper)
	portals := NewRegistryPortals(registry)
	if portals.Get("missing") != nil {
		t.Fatal("Get(missing) != nil")
	}
	if portals.Get("linkedin") != scraper {
		t.Fatal("Get(linkedin) did not return the registered scraper")
	}
	all := portals.All()
	if len(all) != 1 || all[0].Slug() != "linkedin" {
		t.Fatalf("All() = %#v, want one linkedin portal", all)
	}
	if got := NewRegistryPortals(nil).All(); len(got) != 0 {
		t.Fatalf("All() with nil registry = %#v, want none", got)
	}
}

func jobIDs(jobs []job.Job) []string {
	result := make([]string, 0, len(jobs))
	for _, record := range jobs {
		result = append(result, record.ID)
	}
	return result
}

func jobTitles(jobs []job.Job) []string {
	result := make([]string, 0, len(jobs))
	for _, record := range jobs {
		result = append(result, record.Title)
	}
	return result
}

func int64Pointer(value int64) *int64 {
	return &value
}

type fullFakeScraper struct {
	scrapers.BaseScraper
}

func (s *fullFakeScraper) FetchJobs(
	context.Context, []string, string, float64, map[string]struct{},
) ([]job.Job, error) {
	return nil, nil
}

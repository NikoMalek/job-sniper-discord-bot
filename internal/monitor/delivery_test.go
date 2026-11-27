package monitor

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/store"
	"github.com/pemistahl/lingua-go"
)

func TestDeliverMarksBeforeSendAndPauses(t *testing.T) {
	monitor, deps := setupMonitor(t, func(options *Options, _ *testDeps) {
		options.Coalesce = 2 * time.Millisecond
		options.SendPause = time.Millisecond
	})
	config := channelConfig(1, 10)
	monitor.enqueueResults(config, []job.Job{
		jobRecord("job-1", "LinkedIn", "Python engineer", "Acme"),
	})
	waitFor(t, "delivery and flush log", func() bool {
		_, groups := deps.sender.snapshot()
		return len(groups) == 1 && strings.Contains(deps.logs.String(), "flush finished")
	})
	channels, groups := deps.sender.snapshot()
	if channels[0] != "10" || len(groups[0]) != 1 {
		t.Fatalf("delivery = %q, %#v", channels, groups)
	}
	marks, removals := deps.store.snapshot()
	if want := []store.NotifiedKey{{Source: "LinkedIn", JobID: "job-1"}}; !reflect.DeepEqual(marks, want) {
		t.Fatalf("marks = %#v, want %#v", marks, want)
	}
	if len(removals) != 0 {
		t.Fatalf("removals = %#v, want none", removals)
	}
	sleeps := deps.sleeper.sleeps()
	if len(sleeps) < 2 || sleeps[0] != 2*time.Millisecond || sleeps[1] != time.Millisecond {
		t.Fatalf("sleeps = %v, want coalescing delay then send pause", sleeps)
	}
}

func TestDeliverAppliesSkipReasonsInOrder(t *testing.T) {
	monitor, deps := setupMonitor(t, nil)
	config := channelConfig(1, 10)
	config.BlacklistedCompanies = []string{"badcorp"}
	config.BlacklistedKeywords = []string{"intern"}
	config.Keywords = []string{"python"}
	monitor.enqueueResults(config, []job.Job{
		jobRecord("company", "LinkedIn", "Python engineer", "BadCorp Inc"),
		jobRecord("keyword", "LinkedIn", "Senior intern", "Good Corp"),
		jobRecord("keyword-miss", "LinkedIn", "Data analyst", "Good Corp"),
		jobRecord("fresh", "LinkedIn", "Python developer", "Good Corp"),
	})
	waitFor(t, "flush log", func() bool {
		return strings.Contains(deps.logs.String(), "flush finished")
	})
	_, groups := deps.sender.snapshot()
	if len(groups) != 1 || len(groups[0]) != 1 || groups[0][0].ID != "fresh" {
		t.Fatalf("delivered groups = %#v, want only fresh job", groups)
	}
	marks, _ := deps.store.snapshot()
	if len(marks) != 1 || marks[0].JobID != "fresh" {
		t.Fatalf("marks = %#v, want only fresh job", marks)
	}
	logs := deps.logs.String()
	for _, want := range []string{
		"Skipping blacklisted company BadCorp Inc",
		"Skipping blacklisted keyword 'intern' in title 'Senior intern'",
		"Skipping non-matching job group",
		"blacklist_skips=1 blacklist_keyword_skips=1",
		"keyword_skips=1",
		"sent_messages=1 sent_variants=1",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs do not contain %q:\n%s", want, logs)
		}
	}
}

func TestDeliverCountsLanguageSkips(t *testing.T) {
	monitor, deps := setupMonitor(t, func(options *Options, _ *testDeps) {
		options.Matcher = matchingWithDetector(lingua.Spanish)
	})
	config := channelConfig(1, 10)
	config.Languages = []string{"english"}
	monitor.enqueueResults(config, []job.Job{
		jobRecord("lang", "LinkedIn", "Python engineer position", "Good Corp"),
	})
	waitFor(t, "flush log", func() bool {
		return strings.Contains(deps.logs.String(), "flush finished")
	})
	_, groups := deps.sender.snapshot()
	if len(groups) != 0 {
		t.Fatalf("delivered groups = %#v, want none", groups)
	}
	if !strings.Contains(deps.logs.String(), "language_skips=1") {
		t.Fatalf("logs do not contain language skip:\n%s", deps.logs.String())
	}
}

func TestDeliverGroupsDuplicateVariants(t *testing.T) {
	monitor, deps := setupMonitor(t, nil)
	first := jobRecord("one", "LinkedIn", "Python engineer", "Acme")
	second := jobRecord("two", "Get on Board", "Python engineer", "Acme")
	second.URL = first.URL
	monitor.enqueueResults(channelConfig(1, 10), []job.Job{first, second})
	waitFor(t, "flush log", func() bool {
		return strings.Contains(deps.logs.String(), "flush finished")
	})
	_, groups := deps.sender.snapshot()
	if len(groups) != 1 || len(groups[0]) != 2 {
		t.Fatalf("delivered groups = %#v, want one group with two variants", groups)
	}
	logs := deps.logs.String()
	for _, want := range []string{
		"Duplicate group detected for guild 1",
		"duplicate_groups=1 grouped_jobs=2",
		"sent_variants=2",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs do not contain %q:\n%s", want, logs)
		}
	}
}

func TestDeliverSkipsAlreadyNotifiedVariants(t *testing.T) {
	monitor, deps := setupMonitor(t, func(_ *Options, deps *testDeps) {
		deps.store.notified[store.NotifiedKey{Source: "LinkedIn", JobID: "seen"}] = struct{}{}
	})
	monitor.enqueueResults(channelConfig(1, 10), []job.Job{
		jobRecord("seen", "LinkedIn", "Python engineer", "Acme"),
	})
	waitFor(t, "flush log", func() bool {
		return strings.Contains(deps.logs.String(), "flush finished")
	})
	_, groups := deps.sender.snapshot()
	if len(groups) != 0 {
		t.Fatalf("delivered groups = %#v, want none", groups)
	}
	if !strings.Contains(deps.logs.String(), "already_seen=1") {
		t.Fatalf("logs do not contain already_seen:\n%s", deps.logs.String())
	}
}

func TestDeliverRollsBackFailedSend(t *testing.T) {
	monitor, deps := setupMonitor(t, func(_ *Options, deps *testDeps) {
		deps.sender.success = false
	})
	monitor.enqueueResults(channelConfig(1, 10), []job.Job{
		jobRecord("job-1", "LinkedIn", "Python engineer", "Acme"),
	})
	waitFor(t, "flush log", func() bool {
		return strings.Contains(deps.logs.String(), "flush finished")
	})
	_, removals := deps.store.snapshot()
	want := []store.NotifiedKey{{Source: "LinkedIn", JobID: "job-1"}}
	if !reflect.DeepEqual(removals, want) {
		t.Fatalf("removals = %#v, want %#v", removals, want)
	}
	if !strings.Contains(deps.logs.String(), "send_failures=1") {
		t.Fatalf("logs do not contain send failure:\n%s", deps.logs.String())
	}
	if sleeps := deps.sleeper.sleeps(); len(sleeps) < 2 {
		t.Fatalf("sleeps = %v, want coalescing and next-loop sleeps", sleeps)
	}
}

func TestDeliverSkipsWhenChannelIsUnavailable(t *testing.T) {
	monitor, deps := setupMonitor(t, func(_ *Options, deps *testDeps) {
		deps.channels.available = false
	})
	monitor.enqueueResults(channelConfig(1, 10), []job.Job{
		jobRecord("job-1", "LinkedIn", "Python engineer", "Acme"),
	})
	waitFor(t, "flush task cleanup", func() bool {
		monitor.mu.Lock()
		defer monitor.mu.Unlock()
		return len(monitor.flushTasks) == 0
	})
	_, groups := deps.sender.snapshot()
	if len(groups) != 0 {
		t.Fatalf("delivered groups = %#v, want none", groups)
	}
	marks, _ := deps.store.snapshot()
	if len(marks) != 0 {
		t.Fatalf("marks = %#v, want none", marks)
	}
}

func TestDeliverReturnsMarkAndSendErrors(t *testing.T) {
	ctx := context.Background()
	monitor, _ := setupMonitor(t, func(_ *Options, deps *testDeps) {
		deps.store.markErr = map[store.NotifiedKey]error{
			{Source: "LinkedIn", JobID: "job-1"}: errors.New("write failed"),
		}
	})
	config := channelConfig(1, 10)
	err := monitor.deliver(ctx, config, []job.Job{
		jobRecord("job-1", "LinkedIn", "Python engineer", "Acme"),
	})
	if err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("deliver() error = %v, want wrapped write failure", err)
	}

	monitor, _ = setupMonitor(t, func(_ *Options, deps *testDeps) {
		deps.sender.err = errors.New("discord failed")
	})
	err = monitor.deliver(ctx, config, []job.Job{
		jobRecord("job-2", "LinkedIn", "Python engineer", "Acme"),
	})
	if err == nil || !strings.Contains(err.Error(), "discord failed") {
		t.Fatalf("deliver() error = %v, want wrapped send failure", err)
	}
}

func TestFlushLoopBoundsGuildConcurrency(t *testing.T) {
	release := make(chan struct{})
	monitor, deps := setupMonitor(t, func(options *Options, deps *testDeps) {
		options.MaxFlushes = 1
		deps.sender.block = release
	})
	monitor.enqueueResults(channelConfig(1, 10), []job.Job{
		jobRecord("guild-1", "LinkedIn", "Python engineer", "Acme"),
	})
	monitor.enqueueResults(channelConfig(2, 20), []job.Job{
		jobRecord("guild-2", "LinkedIn", "Python developer", "Acme"),
	})
	waitFor(t, "first delivery", func() bool {
		channels, _ := deps.sender.snapshot()
		return len(channels) == 1
	})
	if got := deps.sender.maxConcurrent(); got != 1 {
		t.Fatalf("max concurrent deliveries = %d, want 1", got)
	}
	if channels, _ := deps.sender.snapshot(); len(channels) != 1 {
		t.Fatalf("deliveries while blocked = %q, want one", channels)
	}
	close(release)
	waitFor(t, "second delivery", func() bool {
		channels, _ := deps.sender.snapshot()
		return len(channels) == 2
	})
	if got := deps.sender.maxConcurrent(); got != 1 {
		t.Fatalf("max concurrent deliveries = %d, want 1", got)
	}
}

func TestFlushLoopCancelsCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gate := newGateSleeper()
	monitor, deps := setupMonitor(t, func(options *Options, _ *testDeps) {
		options.Sleeper = gate
	})
	monitor.setRoot(ctx)
	monitor.enqueueResults(channelConfig(1, 10), []job.Job{
		jobRecord("job-1", "LinkedIn", "Python engineer", "Acme"),
	})
	<-gate.entered
	cancel()
	waitFor(t, "flush task cleanup", func() bool {
		monitor.mu.Lock()
		defer monitor.mu.Unlock()
		return len(monitor.flushTasks) == 0
	})
	_, groups := deps.sender.snapshot()
	if len(groups) != 0 {
		t.Fatalf("delivered groups = %#v, want none", groups)
	}
}

func TestFlushLoopCoalescesResults(t *testing.T) {
	gate := newGateSleeper()
	coalesceDelay := 7 * time.Millisecond
	monitor, deps := setupMonitor(t, func(options *Options, _ *testDeps) {
		options.Sleeper = gate
		options.Coalesce = coalesceDelay
	})
	config := channelConfig(1, 10)
	monitor.enqueueResults(config, []job.Job{
		jobRecord("first", "LinkedIn", "Python engineer", "Acme"),
	})
	<-gate.entered
	monitor.enqueueResults(config, []job.Job{
		jobRecord("second", "LinkedIn", "Python developer", "Acme"),
	})
	gate.release <- struct{}{}
	for i := 0; i < 10; i++ {
		monitor.mu.Lock()
		pendingTasks := len(monitor.flushTasks)
		monitor.mu.Unlock()
		if pendingTasks == 0 {
			break
		}
		select {
		case <-gate.entered:
			gate.release <- struct{}{}
		default:
			time.Sleep(time.Millisecond)
		}
	}
	monitor.mu.Lock()
	pendingTasks := len(monitor.flushTasks)
	monitor.mu.Unlock()
	if pendingTasks != 0 {
		_, debugGroups := deps.sender.snapshot()
		t.Fatalf("flush tasks = %d, sleeps = %v, groups = %#v", pendingTasks, gate.durations, debugGroups)
	}
	_, groups := deps.sender.snapshot()
	if len(groups) != 2 {
		t.Fatalf("delivered groups = %#v, want two groups from one flush", groups)
	}
	ids := make([]string, 0, len(groups))
	for _, group := range groups {
		if len(group) != 1 {
			t.Fatalf("group = %#v, want one job", group)
		}
		ids = append(ids, group[0].ID)
	}
	if want := []string{"first", "second"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("delivered jobs = %v, want %v", ids, want)
	}
	durations := gate.durations
	if len(durations) == 0 || durations[0] != coalesceDelay {
		t.Fatalf("first sleep = %v, want coalescing delay", durations)
	}
	pauses := 0
	for _, duration := range durations {
		if duration == time.Millisecond {
			pauses++
		}
	}
	if pauses != 2 {
		t.Fatalf("send pauses = %d, want 2; durations = %v", pauses, durations)
	}
}

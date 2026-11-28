package monitor

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/dedupe"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/matching"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/store"
)

type deliveryCounters struct {
	sentMessages          int
	sentVariants          int
	duplicateGroups       int
	groupedJobs           int
	alreadySeen           int
	languageSkips         int
	keywordSkips          int
	blacklistSkips        int
	blacklistKeywordSkips int
	sendFailures          int
}

func (m *Monitor) enqueueResults(config store.GuildMonitorConfig, jobs []job.Job) {
	if len(jobs) == 0 {
		return
	}
	ctx := m.rootContext(context.Background())
	m.mu.Lock()
	m.pending[config.GuildID] = append(m.pending[config.GuildID], jobs...)
	m.pendingConfigs[config.GuildID] = config
	if taskRunning(m.flushTasks[config.GuildID]) {
		m.mu.Unlock()
		return
	}
	taskCtx, cancel := context.WithCancel(ctx)
	state := &taskState{done: make(chan struct{})}
	m.flushTasks[config.GuildID] = state
	m.mu.Unlock()
	go func() {
		defer close(state.done)
		defer cancel()
		m.flushLoop(taskCtx, config.GuildID, state)
	}()
}

func (m *Monitor) flushLoop(ctx context.Context, guildID int64, state *taskState) {
	for {
		if err := m.sleeper.Sleep(ctx, m.coalesce); err != nil {
			m.finishFlushTask(guildID, state)
			return
		}
		config, jobs, ok := m.drainPending(guildID)
		if !ok || len(jobs) == 0 {
			m.mu.Lock()
			if len(m.pending[guildID]) > 0 {
				m.mu.Unlock()
				continue
			}
			delete(m.pendingConfigs, guildID)
			if m.flushTasks[guildID] == state {
				delete(m.flushTasks, guildID)
			}
			m.mu.Unlock()
			return
		}
		if err := m.flushGuild(ctx, config, jobs); err != nil {
			m.log.Error(fmt.Sprintf(
				"Guild %d failed while delivering notifications: %v", guildID, err,
			))
		}
	}
}

func (m *Monitor) finishFlushTask(guildID int64, state *taskState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.flushTasks[guildID] == state {
		delete(m.flushTasks, guildID)
	}
}

func (m *Monitor) drainPending(guildID int64) (store.GuildMonitorConfig, []job.Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	config, hasConfig := m.pendingConfigs[guildID]
	jobs := m.pending[guildID]
	delete(m.pending, guildID)
	delete(m.pendingConfigs, guildID)
	return config, jobs, hasConfig
}

func (m *Monitor) flushGuild(
	ctx context.Context, config store.GuildMonitorConfig, pending []job.Job,
) error {
	if err := m.flushSem.acquire(ctx); err != nil {
		return fmt.Errorf("acquire delivery slot: %w", err)
	}
	defer m.flushSem.release()
	return m.deliver(ctx, config, pending)
}

func (m *Monitor) deliver(
	ctx context.Context, config store.GuildMonitorConfig, pending []job.Job,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if config.ChannelID == nil {
		return nil
	}
	available, err := m.channels.ResolveChannel(ctx, *config.ChannelID)
	if err != nil || !available {
		m.log.Warn(fmt.Sprintf(
			"Configured channel %s is unavailable", channelLabel(*config.ChannelID),
		))
		return nil
	}

	unique := make(map[jobKey]job.Job, len(pending))
	order := make([]jobKey, 0, len(pending))
	scrapedBySource := make(map[string]int)
	sourceOrder := make([]string, 0)
	for _, record := range pending {
		key := jobKey{source: record.Source, id: record.ID}
		if _, exists := unique[key]; !exists {
			order = append(order, key)
		}
		unique[key] = record
		if scrapedBySource[record.Source] == 0 {
			sourceOrder = append(sourceOrder, record.Source)
		}
		scrapedBySource[record.Source]++
	}
	records := make([]job.Job, 0, len(order))
	for _, key := range order {
		records = append(records, unique[key])
	}
	groups := dedupe.GroupJobs(records)
	var counters deliveryCounters
	sentBySource := make(map[string]int)
	sentOrder := make([]string, 0)

	for _, group := range groups {
		if err := ctx.Err(); err != nil {
			return err
		}
		label := groupLabel(group)
		if len(group) > 1 {
			counters.duplicateGroups++
			counters.groupedJobs += len(group)
			m.log.Info(fmt.Sprintf(
				"Duplicate group detected for guild %d: %s", config.GuildID, label,
			))
		}
		if company, blocked := blockedCompany(group, config.BlacklistedCompanies); blocked {
			counters.blacklistSkips += len(group)
			m.log.Info(fmt.Sprintf(
				"Skipping blacklisted company %s for job group [%s] in guild %d",
				company, label, config.GuildID,
			))
			continue
		}
		if hit, blocked := m.blockedKeyword(group, config.BlacklistedKeywords); blocked {
			counters.blacklistKeywordSkips += len(group)
			m.log.Info(fmt.Sprintf(
				"Skipping blacklisted keyword %s in title %s for job group [%s] in guild %d",
				repr(hit.keyword), repr(hit.job.Title), label, config.GuildID,
			))
			continue
		}
		if !anyMatchesLanguage(m.matcher, group, config.Languages) {
			counters.languageSkips += len(group)
			m.log.Info(fmt.Sprintf(
				"Skipping language-mismatched job group [%s] (%s) for guild %d",
				label, repr(group[0].Title), config.GuildID,
			))
			continue
		}
		if !anyMatchesKeyword(m.matcher, group, config.Keywords) {
			counters.keywordSkips += len(group)
			m.log.Info(fmt.Sprintf(
				"Skipping non-matching job group [%s] (%s) for guild %d",
				label, repr(group[0].Title), config.GuildID,
			))
			continue
		}

		fresh := make([]job.Job, 0, len(group))
		for _, record := range group {
			inserted, err := m.store.MarkNotified(
				ctx, config.GuildID, record.ID, record.Source,
			)
			if err != nil {
				return fmt.Errorf("mark notified %s:%s: %w", record.Source, record.ID, err)
			}
			if inserted {
				fresh = append(fresh, record)
			} else {
				counters.alreadySeen++
			}
		}
		if len(fresh) == 0 {
			continue
		}

		channelID := strconv.FormatInt(*config.ChannelID, 10)
		sent, err := m.sender.SendJobsForGuild(ctx, config.GuildID, channelID, fresh)
		if err != nil {
			return fmt.Errorf("send jobs: %w", err)
		}
		if sent {
			counters.sentMessages++
			counters.sentVariants += len(fresh)
			for _, record := range fresh {
				if sentBySource[record.Source] == 0 {
					sentOrder = append(sentOrder, record.Source)
				}
				sentBySource[record.Source]++
			}
			m.log.Info(fmt.Sprintf(
				"Sent job group [%s] (%s) at %s to guild %d",
				groupLabel(fresh), repr(fresh[0].Title), repr(fresh[0].Company),
				config.GuildID,
			))
			if err := m.sleeper.Sleep(ctx, m.sendPause); err != nil {
				return fmt.Errorf("pause after send: %w", err)
			}
			continue
		}

		counters.sendFailures += len(fresh)
		for _, record := range fresh {
			if err := m.store.RemoveNotified(
				ctx, config.GuildID, record.ID, record.Source,
			); err != nil {
				return fmt.Errorf("roll back notified %s:%s: %w", record.Source, record.ID, err)
			}
		}
	}

	m.log.Info(fmt.Sprintf(
		"Guild %d flush finished: portals=%s sent_by_source=%s unique=%d "+
			"sent_messages=%d sent_variants=%d duplicate_groups=%d grouped_jobs=%d "+
			"already_seen=%d language_skips=%d keyword_skips=%d blacklist_skips=%d "+
			"blacklist_keyword_skips=%d send_failures=%d",
		config.GuildID, joinCounts(scrapedBySource, sourceOrder),
		joinCounts(sentBySource, sentOrder), len(unique),
		counters.sentMessages, counters.sentVariants, counters.duplicateGroups,
		counters.groupedJobs, counters.alreadySeen, counters.languageSkips,
		counters.keywordSkips, counters.blacklistSkips, counters.blacklistKeywordSkips,
		counters.sendFailures,
	))
	return nil
}

func groupLabel(jobs []job.Job) string {
	parts := make([]string, 0, len(jobs))
	for _, record := range jobs {
		parts = append(parts, record.Source+":"+record.ID)
	}
	return strings.Join(parts, ", ")
}

func blockedCompany(jobs []job.Job, blocked []string) (string, bool) {
	for _, record := range jobs {
		if matching.MatchesCompanyBlacklist(record.Company, blocked) {
			return record.Company, true
		}
	}
	return "", false
}

type keywordHit struct {
	job     job.Job
	keyword string
}

func (m *Monitor) blockedKeyword(jobs []job.Job, blocked []string) (keywordHit, bool) {
	for _, record := range jobs {
		if hit := m.matcher.MatchesBlacklistedKeyword(record, blocked); hit != "" {
			return keywordHit{job: record, keyword: hit}, true
		}
	}
	return keywordHit{}, false
}

func anyMatchesLanguage(
	matcher *matching.Matcher, jobs []job.Job, languages []string,
) bool {
	for _, record := range jobs {
		if matcher.MatchesLanguage(record, languages) {
			return true
		}
	}
	return false
}

func anyMatchesKeyword(
	matcher *matching.Matcher, jobs []job.Job, keywords []string,
) bool {
	for _, record := range jobs {
		for _, keyword := range keywords {
			if matcher.MatchesKeyword(record, keyword) {
				return true
			}
		}
	}
	return false
}

func joinCounts(counts map[string]int, order []string) string {
	parts := make([]string, 0, len(counts))
	seen := make(map[string]struct{}, len(counts))
	for _, source := range order {
		count, ok := counts[source]
		if !ok {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s:%d", source, count))
		seen[source] = struct{}{}
	}
	remaining := make([]string, 0)
	for source := range counts {
		if _, ok := seen[source]; !ok {
			remaining = append(remaining, source)
		}
	}
	sort.Strings(remaining)
	for _, source := range remaining {
		parts = append(parts, fmt.Sprintf("%s:%d", source, counts[source]))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

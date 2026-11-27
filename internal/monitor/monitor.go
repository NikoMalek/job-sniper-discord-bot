// Package monitor runs periodic scraping cycles and delivers fresh jobs.
package monitor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/locations"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/matching"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/scrapers"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/store"
)

const (
	// DefaultInterval matches the Python five-minute task loop.
	DefaultInterval = 5 * time.Minute
	// DefaultCoalesceDelay is the per-guild delivery flush delay.
	DefaultCoalesceDelay = 20 * time.Second
	// DefaultSendPause is the pause after a successful message.
	DefaultSendPause = 1500 * time.Millisecond
	// DefaultPortalConcurrency bounds simultaneous portal fetches.
	DefaultPortalConcurrency = 4
	// DefaultFlushConcurrency bounds simultaneous guild deliveries.
	DefaultFlushConcurrency = 2
)

var portalSlugOrder = map[string]int{
	"linkedin":      0,
	"chiletrabajos": 1,
	"computrabajo":  2,
	"firstjob":      3,
	"getonbrd":      4,
}

// Store is the narrow persistence contract used by the monitor.
type Store interface {
	GetMonitorConfigs(ctx context.Context) ([]store.GuildMonitorConfig, error)
	GetNotifiedKeys(ctx context.Context, guildID int64) (map[store.NotifiedKey]struct{}, error)
	MarkNotified(ctx context.Context, guildID int64, jobID, source string) (bool, error)
	RemoveNotified(ctx context.Context, guildID int64, jobID, source string) error
	CleanupNotifiedJobs(ctx context.Context, hours ...float64) (int64, error)
	CleanupTrackedJobs(ctx context.Context, hours ...float64) (int64, error)
}

var _ Store = (*store.Store)(nil)

// Portal is the part of a scraper needed by a monitoring cycle.
type Portal interface {
	Slug() string
	Source() string
	Plan(intent *locations.LocationIntent) scrapers.PortalPlan
	BeginCycle()
	FetchJobs(
		ctx context.Context,
		keywords []string,
		location string,
		maxAgeHours float64,
		knownJobIDs map[string]struct{},
	) ([]job.Job, error)
}

// PortalLookup resolves configured portal slugs to scraper instances.
type PortalLookup interface {
	Get(slug string) Portal
	All() []Portal
}

// ChannelResolver reports whether a configured channel can receive messages.
type ChannelResolver interface {
	ResolveChannel(ctx context.Context, channelID int64) (bool, error)
}

// JobSender delivers a prepared job group to a channel.
type JobSender interface {
	SendJobs(ctx context.Context, channelID string, jobs []job.Job) (bool, error)
}

// Sleeper injects waits so cycles and delivery can be tested without real time.
type Sleeper interface {
	Sleep(ctx context.Context, duration time.Duration) error
}

// Options holds the monitor dependencies and timing configuration.
type Options struct {
	Store      Store
	Portals    PortalLookup
	Channels   ChannelResolver
	Sender     JobSender
	Matcher    *matching.Matcher
	Interval   time.Duration
	Coalesce   time.Duration
	SendPause  time.Duration
	Retention  float64
	Ready      <-chan struct{}
	Sleeper    Sleeper
	Now        func() time.Time
	Logger     *slog.Logger
	MaxPortals int
	MaxFlushes int
}

type portalKey struct {
	guildID int64
	slug    string
}

type jobKey struct {
	source string
	id     string
}

type taskState struct {
	done chan struct{}
}

type semaphore struct {
	tokens chan struct{}
}

func newSemaphore(limit int) *semaphore {
	if limit < 1 {
		limit = 1
	}
	return &semaphore{tokens: make(chan struct{}, limit)}
}

func (s *semaphore) acquire(ctx context.Context) error {
	select {
	case s.tokens <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *semaphore) release() {
	<-s.tokens
}

// Monitor schedules portal tasks and coalesces their results by guild.
type Monitor struct {
	store     Store
	portals   PortalLookup
	channels  ChannelResolver
	sender    JobSender
	matcher   *matching.Matcher
	interval  time.Duration
	coalesce  time.Duration
	sendPause time.Duration
	retention float64
	ready     <-chan struct{}
	sleeper   Sleeper
	now       func() time.Time
	log       *slog.Logger
	portalSem *semaphore
	flushSem  *semaphore

	mu             sync.Mutex
	root           context.Context
	portalTasks    map[portalKey]*taskState
	flushTasks     map[int64]*taskState
	pending        map[int64][]job.Job
	pendingConfigs map[int64]store.GuildMonitorConfig
}

// New builds a monitor. A nil matcher loads the embedded matching catalogs.
func New(ctx context.Context, options Options) (*Monitor, error) {
	if options.Store == nil {
		return nil, errors.New("monitor: store is required")
	}
	if options.Portals == nil {
		return nil, errors.New("monitor: portal lookup is required")
	}
	if options.Channels == nil {
		return nil, errors.New("monitor: channel resolver is required")
	}
	if options.Sender == nil {
		return nil, errors.New("monitor: job sender is required")
	}
	matcher := options.Matcher
	if matcher == nil {
		loaded, err := matching.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("monitor: load matcher: %w", err)
		}
		matcher = loaded
	}
	interval := options.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	coalesce := options.Coalesce
	if coalesce <= 0 {
		coalesce = DefaultCoalesceDelay
	}
	sendPause := options.SendPause
	if sendPause <= 0 {
		sendPause = DefaultSendPause
	}
	retention := options.Retention
	if retention <= 0 {
		retention = store.NotifiedRetentionHours
	}
	sleeper := options.Sleeper
	if sleeper == nil {
		sleeper = sleeperFunc(sleepWithContext)
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	maxPortals := options.MaxPortals
	if maxPortals <= 0 {
		maxPortals = DefaultPortalConcurrency
	}
	maxFlushes := options.MaxFlushes
	if maxFlushes <= 0 {
		maxFlushes = DefaultFlushConcurrency
	}
	return &Monitor{
		store:          options.Store,
		portals:        options.Portals,
		channels:       options.Channels,
		sender:         options.Sender,
		matcher:        matcher,
		interval:       interval,
		coalesce:       coalesce,
		sendPause:      sendPause,
		retention:      retention,
		ready:          options.Ready,
		sleeper:        sleeper,
		now:            now,
		log:            logger,
		portalSem:      newSemaphore(maxPortals),
		flushSem:       newSemaphore(maxFlushes),
		portalTasks:    make(map[portalKey]*taskState),
		flushTasks:     make(map[int64]*taskState),
		pending:        make(map[int64][]job.Job),
		pendingConfigs: make(map[int64]store.GuildMonitorConfig),
	}, nil
}

// Run waits for readiness and executes cycles until ctx is canceled.
func (m *Monitor) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	m.setRoot(runCtx)
	if m.ready != nil {
		select {
		case <-m.ready:
		case <-runCtx.Done():
			return nil
		}
	}
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		m.RunOnce(runCtx)
		select {
		case <-runCtx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// RunOnce executes one scheduling cycle. Portal and flush tasks continue in
// the background after it returns.
func (m *Monitor) RunOnce(ctx context.Context) {
	if err := ctx.Err(); err != nil {
		return
	}
	m.setRoot(ctx)
	started := m.now()
	for _, portal := range m.portals.All() {
		portal.BeginCycle()
	}
	configs, err := m.store.GetMonitorConfigs(ctx)
	if err != nil {
		m.log.Error(fmt.Sprintf("Could not load monitor configurations: %v", err))
		return
	}
	m.log.Info(fmt.Sprintf(
		"Cycle start: guilds=%d running_portals=%d available_portals=%s",
		len(configs), m.runningPortalCount(), m.availablePortals(),
	))
	for _, config := range configs {
		if err := ctx.Err(); err != nil {
			return
		}
		m.scheduleGuild(ctx, config)
	}
	removed, err := m.store.CleanupNotifiedJobs(ctx, m.retention)
	if err != nil {
		m.log.Error(fmt.Sprintf("Could not prune notified jobs: %v", err))
	} else if removed > 0 {
		m.log.Info(fmt.Sprintf(
			"Pruned %d notification records older than %.0f hours",
			removed, m.retention,
		))
	}
	expired, err := m.store.CleanupTrackedJobs(ctx, m.retention)
	if err != nil {
		m.log.Error(fmt.Sprintf("Could not prune tracked jobs: %v", err))
	} else if expired > 0 {
		m.log.Info(fmt.Sprintf(
			"Pruned %d tracking tokens older than %.0f hours",
			expired, m.retention,
		))
	}
	m.log.Info(fmt.Sprintf(
		"Cycle scheduled in %.1fs; portals keep running in the background",
		m.elapsed(started),
	))
}

// Wait blocks until all background portal and flush tasks have exited.
func (m *Monitor) Wait(ctx context.Context) error {
	for {
		m.mu.Lock()
		states := make([]*taskState, 0, len(m.portalTasks)+len(m.flushTasks))
		for _, state := range m.portalTasks {
			states = append(states, state)
		}
		for _, state := range m.flushTasks {
			states = append(states, state)
		}
		m.mu.Unlock()
		if len(states) == 0 {
			return nil
		}
		for _, state := range states {
			select {
			case <-state.done:
			case <-ctx.Done():
				return fmt.Errorf("wait for monitor tasks: %w", ctx.Err())
			}
		}
	}
}

func (m *Monitor) setRoot(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.root = ctx
}

func (m *Monitor) rootContext(fallback context.Context) context.Context {
	m.mu.Lock()
	ctx := m.root
	m.mu.Unlock()
	if ctx != nil {
		return ctx
	}
	return fallback
}

func (m *Monitor) elapsed(started time.Time) float64 {
	return m.now().Sub(started).Seconds()
}

func (m *Monitor) availablePortals() string {
	portals := m.portals.All()
	if len(portals) == 0 {
		return "none"
	}
	sort.SliceStable(portals, func(i, j int) bool {
		left, right := portals[i].Slug(), portals[j].Slug()
		leftOrder, leftKnown := portalSlugOrder[left]
		rightOrder, rightKnown := portalSlugOrder[right]
		switch {
		case leftKnown && rightKnown:
			if leftOrder != rightOrder {
				return leftOrder < rightOrder
			}
		case leftKnown:
			return true
		case rightKnown:
			return false
		}
		if left == right {
			return portals[i].Source() < portals[j].Source()
		}
		return left < right
	})
	names := make([]string, 0, len(portals))
	for _, portal := range portals {
		names = append(names, portal.Slug())
	}
	return strings.Join(names, ", ")
}

func (m *Monitor) runningPortalCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, state := range m.portalTasks {
		if taskRunning(state) {
			count++
		}
	}
	return count
}

func taskRunning(state *taskState) bool {
	if state == nil {
		return false
	}
	select {
	case <-state.done:
		return false
	default:
		return true
	}
}

func (m *Monitor) scheduleGuild(ctx context.Context, config store.GuildMonitorConfig) {
	if config.ChannelID == nil {
		return
	}
	portals := make([]Portal, 0, len(config.Portals))
	for _, slug := range config.Portals {
		portal := m.portals.Get(slug)
		if portal == nil {
			m.log.Warn(fmt.Sprintf(
				"Guild %d has unknown portal %s; skipping it", config.GuildID, repr(slug),
			))
			continue
		}
		portals = append(portals, portal)
	}
	if len(portals) == 0 {
		m.log.Warn(fmt.Sprintf(
			"Guild %d has no available portals; skipping cycle", config.GuildID,
		))
		return
	}
	sources := make([]string, 0, len(portals))
	for _, portal := range portals {
		sources = append(sources, portal.Source())
	}
	m.log.Info(fmt.Sprintf(
		"Guild %d: scheduling portals=%s keywords=%d max_age=%.1fh",
		config.GuildID, strings.Join(sources, ", "),
		len(config.Keywords), config.MaxAgeHours,
	))
	for _, portal := range portals {
		key := portalKey{guildID: config.GuildID, slug: portal.Slug()}
		m.mu.Lock()
		if taskRunning(m.portalTasks[key]) {
			m.mu.Unlock()
			m.log.Info(fmt.Sprintf(
				"Guild %d: %s is still running from a previous cycle; not restarting it",
				config.GuildID, portal.Source(),
			))
			continue
		}
		taskCtx, cancel := context.WithCancel(ctx)
		state := &taskState{done: make(chan struct{})}
		m.portalTasks[key] = state
		m.mu.Unlock()
		go func() {
			defer close(state.done)
			defer cancel()
			defer func() {
				m.mu.Lock()
				if m.portalTasks[key] == state {
					delete(m.portalTasks, key)
				}
				m.mu.Unlock()
			}()
			m.runPortal(taskCtx, config, portal)
		}()
	}
}

func (m *Monitor) runPortal(
	ctx context.Context, config store.GuildMonitorConfig, portal Portal,
) {
	if config.ChannelID == nil {
		return
	}
	available, err := m.channels.ResolveChannel(ctx, *config.ChannelID)
	if err != nil {
		m.log.Warn(fmt.Sprintf(
			"Configured channel %s is unavailable", channelLabel(*config.ChannelID),
		))
		return
	}
	if !available {
		m.log.Warn(fmt.Sprintf(
			"Configured channel %s is unavailable", channelLabel(*config.ChannelID),
		))
		return
	}
	notified, err := m.store.GetNotifiedKeys(ctx, config.GuildID)
	if err != nil {
		m.log.Error(fmt.Sprintf(
			"Portal %s failed for guild %d: %v", portal.Source(), config.GuildID, err,
		))
		return
	}
	known := make(map[string]struct{})
	for key := range notified {
		if key.Source == portal.Source() {
			known[key.JobID] = struct{}{}
		}
	}
	started := m.now()
	m.log.Info(fmt.Sprintf(
		"Portal %s starting for guild %d (known=%d, locations=%d)",
		portal.Source(), config.GuildID, len(known), len(config.Locations),
	))

	values := config.Locations
	if len(values) == 0 {
		values = []string{store.DefaultLocation}
	}
	intents := make([]locations.LocationIntent, 0, len(values))
	for _, value := range values {
		intent, err := locations.Parse(value)
		if err != nil {
			m.log.Warn(fmt.Sprintf(
				"Guild %d has an invalid location %s; skipping it",
				config.GuildID, repr(value),
			))
			continue
		}
		intents = append(intents, intent)
	}

	plans := make([]scrapers.PortalPlan, 0, len(intents))
	seenLocations := make(map[string]struct{})
	for index := range intents {
		plan := portal.Plan(&intents[index])
		if !plan.Supported {
			m.log.Info(fmt.Sprintf(
				"Portal %s paused for guild %d: %s",
				portal.Source(), config.GuildID, plan.Reason,
			))
			continue
		}
		if _, ok := seenLocations[plan.SearchLocation]; ok {
			continue
		}
		seenLocations[plan.SearchLocation] = struct{}{}
		plans = append(plans, plan)
	}
	if len(plans) == 0 {
		m.log.Warn(fmt.Sprintf(
			"Portal %s has no compatible location for guild %d (locations=%s)",
			portal.Source(), config.GuildID, strings.Join(values, ", "),
		))
		return
	}

	collected := make(map[jobKey]job.Job)
	order := make([]jobKey, 0)
	for _, plan := range plans {
		found, err := m.fetchJobs(
			ctx, portal, config.Keywords, plan.SearchLocation, config.MaxAgeHours, known,
		)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			m.log.Error(fmt.Sprintf(
				"Portal %s failed for guild %d at location %s",
				portal.Source(), config.GuildID, repr(plan.SearchLocation),
			))
			continue
		}
		for _, record := range found {
			key := jobKey{source: record.Source, id: record.ID}
			if _, exists := collected[key]; !exists {
				order = append(order, key)
			}
			collected[key] = record
		}
		m.log.Info(fmt.Sprintf(
			"Portal %s location %s returned %d jobs for guild %d",
			portal.Source(), repr(plan.SearchLocation), len(found), config.GuildID,
		))
	}
	m.log.Info(fmt.Sprintf(
		"Portal %s returned %d unique jobs for guild %d in %.1fs",
		portal.Source(), len(collected), config.GuildID, m.elapsed(started),
	))

	results := make([]job.Job, 0, len(order))
	for _, key := range order {
		results = append(results, collected[key])
	}
	m.enqueueResults(config, results)
}

func (m *Monitor) fetchJobs(
	ctx context.Context,
	portal Portal,
	keywords []string,
	location string,
	maxAgeHours float64,
	known map[string]struct{},
) ([]job.Job, error) {
	if err := m.portalSem.acquire(ctx); err != nil {
		return nil, fmt.Errorf("acquire portal slot: %w", err)
	}
	defer m.portalSem.release()
	return portal.FetchJobs(ctx, keywords, location, maxAgeHours, known)
}

type sleeperFunc func(context.Context, time.Duration) error

func (f sleeperFunc) Sleep(ctx context.Context, duration time.Duration) error {
	return f(ctx, duration)
}

func sleepWithContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func repr(value string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `'`, `\'`) + "'"
}

func channelLabel(channelID int64) string {
	return fmt.Sprintf("%d", channelID)
}

// RegistryPortals adapts a scrapers.Registry to PortalLookup with stable
// ordering for cycle logs.
type RegistryPortals struct {
	registry *scrapers.Registry
}

// NewRegistryPortals wraps an existing scraper registry.
func NewRegistryPortals(registry *scrapers.Registry) *RegistryPortals {
	return &RegistryPortals{registry: registry}
}

// Get resolves a portal slug.
func (p *RegistryPortals) Get(slug string) Portal {
	if p == nil || p.registry == nil {
		return nil
	}
	return p.registry.Get(slug)
}

// All returns every registered portal ordered by slug.
func (p *RegistryPortals) All() []Portal {
	if p == nil || p.registry == nil {
		return nil
	}
	scraperMap := p.registry.Scrapers()
	slugs := make([]string, 0, len(scraperMap))
	for slug := range scraperMap {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	portals := make([]Portal, 0, len(slugs))
	for _, slug := range slugs {
		portals = append(portals, scraperMap[slug])
	}
	return portals
}

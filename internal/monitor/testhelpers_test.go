package monitor

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/locations"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/matching"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/scrapers"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/store"
	"github.com/pemistahl/lingua-go"
)

var fixedNow = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

type testDeps struct {
	store    *fakeStore
	portals  *fakePortalLookup
	channels *fakeChannelResolver
	sender   *fakeJobSender
	sleeper  *recordingSleeper
	logs     *syncBuffer
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(value)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func setupMonitor(
	t *testing.T, configure func(*Options, *testDeps),
) (*Monitor, *testDeps) {
	t.Helper()
	deps := &testDeps{
		store:    newFakeStore(),
		portals:  &fakePortalLookup{},
		channels: &fakeChannelResolver{available: true},
		sender:   &fakeJobSender{success: true},
		sleeper:  &recordingSleeper{},
		logs:     &syncBuffer{},
	}
	detector := fakeDetector{language: lingua.English}
	options := Options{
		Store:      deps.store,
		Portals:    deps.portals,
		Channels:   deps.channels,
		Sender:     deps.sender,
		Matcher:    matching.NewWithCatalog(nil, detector),
		Coalesce:   time.Millisecond,
		SendPause:  time.Millisecond,
		Retention:  store.NotifiedRetentionHours,
		Sleeper:    deps.sleeper,
		Now:        func() time.Time { return fixedNow },
		Logger:     slog.New(slog.NewTextHandler(deps.logs, nil)),
		MaxPortals: 4,
		MaxFlushes: 2,
	}
	if configure != nil {
		configure(&options, deps)
	}
	monitor, err := New(context.Background(), options)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return monitor, deps
}

type fakeDetector struct {
	language lingua.Language
}

func (d fakeDetector) DetectLanguageOf(string) (lingua.Language, bool) {
	return d.language, true
}

type fakeStore struct {
	mu           sync.Mutex
	configs      []store.GuildMonitorConfig
	configErr    error
	notified     map[store.NotifiedKey]struct{}
	markErr      map[store.NotifiedKey]error
	removeErr    error
	getKeysErr   error
	cleanupErr   error
	cleanupBoth  bool
	cleanupCalls int
	marks        []store.NotifiedKey
	removals     []store.NotifiedKey
}

func newFakeStore() *fakeStore {
	return &fakeStore{notified: make(map[store.NotifiedKey]struct{})}
}

func (s *fakeStore) GetMonitorConfigs(context.Context) ([]store.GuildMonitorConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.GuildMonitorConfig(nil), s.configs...), s.configErr
}

func (s *fakeStore) GetNotifiedKeys(
	_ context.Context, guildID int64,
) (map[store.NotifiedKey]struct{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getKeysErr != nil {
		return nil, s.getKeysErr
	}
	result := make(map[store.NotifiedKey]struct{}, len(s.notified))
	for key := range s.notified {
		result[key] = struct{}{}
	}
	return result, nil
}

func (s *fakeStore) MarkNotified(
	_ context.Context, guildID int64, jobID, source string,
) (bool, error) {
	key := store.NotifiedKey{Source: source, JobID: jobID}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.marks = append(s.marks, key)
	if err := s.markErr[key]; err != nil {
		return false, err
	}
	if _, exists := s.notified[key]; exists {
		return false, nil
	}
	s.notified[key] = struct{}{}
	return true, nil
}

func (s *fakeStore) RemoveNotified(
	_ context.Context, guildID int64, jobID, source string,
) error {
	key := store.NotifiedKey{Source: source, JobID: jobID}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removals = append(s.removals, key)
	if s.removeErr != nil {
		return s.removeErr
	}
	delete(s.notified, key)
	return nil
}

func (s *fakeStore) CleanupNotifiedJobs(
	_ context.Context, hours ...float64,
) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupCalls++
	if s.cleanupErr != nil {
		return 0, s.cleanupErr
	}
	if s.cleanupBoth {
		return 2, nil
	}
	return 1, nil
}

func (s *fakeStore) CleanupTrackedJobs(
	_ context.Context, hours ...float64,
) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupCalls++
	if s.cleanupErr != nil {
		return 0, s.cleanupErr
	}
	if s.cleanupBoth {
		return 3, nil
	}
	return 0, nil
}

func (s *fakeStore) snapshot() ([]store.NotifiedKey, []store.NotifiedKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.NotifiedKey(nil), s.marks...),
		append([]store.NotifiedKey(nil), s.removals...)
}

type fetchCall struct {
	keywords    []string
	location    string
	maxAgeHours float64
	known       map[string]struct{}
}

type fakePortal struct {
	slug       string
	source     string
	beginCalls int

	mu               sync.Mutex
	plans            map[string]scrapers.PortalPlan
	jobs             map[string][]job.Job
	fetchErr         map[string]error
	calls            []fetchCall
	block            <-chan struct{}
	blockUntilCancel bool
	inFlight         int
	maxInFlight      int
}

func newFakePortal(slug, source string) *fakePortal {
	return &fakePortal{
		slug:     slug,
		source:   source,
		plans:    make(map[string]scrapers.PortalPlan),
		jobs:     make(map[string][]job.Job),
		fetchErr: make(map[string]error),
	}
}

func (p *fakePortal) Slug() string   { return p.slug }
func (p *fakePortal) Source() string { return p.source }

func (p *fakePortal) Plan(intent *locations.LocationIntent) scrapers.PortalPlan {
	label := ""
	if intent != nil {
		label = intent.Label()
	}
	if plan, ok := p.plans[label]; ok {
		return plan
	}
	return scrapers.PortalPlan{Supported: true, SearchLocation: label}
}

func (p *fakePortal) BeginCycle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.beginCalls++
}

func (p *fakePortal) FetchJobs(
	ctx context.Context,
	keywords []string,
	location string,
	maxAgeHours float64,
	knownJobIDs map[string]struct{},
) ([]job.Job, error) {
	p.mu.Lock()
	p.calls = append(p.calls, fetchCall{
		keywords:    append([]string(nil), keywords...),
		location:    location,
		maxAgeHours: maxAgeHours,
		known:       copyKnown(knownJobIDs),
	})
	p.inFlight++
	if p.inFlight > p.maxInFlight {
		p.maxInFlight = p.inFlight
	}
	block := p.block
	blockUntilCancel := p.blockUntilCancel
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		p.inFlight--
		p.mu.Unlock()
	}()
	if blockUntilCancel {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if block != nil {
		<-block
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.fetchErr[location]; err != nil {
		return nil, err
	}
	return append([]job.Job(nil), p.jobs[location]...), nil
}

func (p *fakePortal) fetchCalls() []fetchCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]fetchCall(nil), p.calls...)
}

func (p *fakePortal) beginCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.beginCalls
}

func (p *fakePortal) maxConcurrent() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxInFlight
}

func copyKnown(known map[string]struct{}) map[string]struct{} {
	result := make(map[string]struct{}, len(known))
	for value := range known {
		result[value] = struct{}{}
	}
	return result
}

type fakePortalLookup struct {
	portals []Portal
}

func (l *fakePortalLookup) Get(slug string) Portal {
	for _, portal := range l.portals {
		if portal.Slug() == slug {
			return portal
		}
	}
	return nil
}

func (l *fakePortalLookup) All() []Portal {
	return append([]Portal(nil), l.portals...)
}

type fakeChannelResolver struct {
	mu        sync.Mutex
	available bool
	err       error
	calls     []int64
}

func (r *fakeChannelResolver) ResolveChannel(
	_ context.Context, channelID int64,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, channelID)
	return r.available, r.err
}

func (r *fakeChannelResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

type fakeJobSender struct {
	success bool
	err     error
	block   <-chan struct{}

	mu          sync.Mutex
	guildIDs    []int64
	channels    []string
	groups      [][]job.Job
	inFlight    int
	maxInFlight int
}

func (s *fakeJobSender) SendJobsForGuild(
	ctx context.Context, guildID int64, channelID string, jobs []job.Job,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	s.guildIDs = append(s.guildIDs, guildID)
	s.channels = append(s.channels, channelID)
	s.groups = append(s.groups, append([]job.Job(nil), jobs...))
	s.inFlight++
	if s.inFlight > s.maxInFlight {
		s.maxInFlight = s.inFlight
	}
	success, sendErr := s.success, s.err
	block := s.block
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.inFlight--
		s.mu.Unlock()
	}()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	if sendErr != nil {
		return false, sendErr
	}
	return success, nil
}

func (s *fakeJobSender) snapshot() ([]string, [][]job.Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.channels...),
		append([][]job.Job(nil), s.groups...)
}

func (s *fakeJobSender) maxConcurrent() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxInFlight
}

type recordingSleeper struct {
	mu        sync.Mutex
	durations []time.Duration
	err       error
}

func (s *recordingSleeper) Sleep(ctx context.Context, duration time.Duration) error {
	s.mu.Lock()
	s.durations = append(s.durations, duration)
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return ctx.Err()
}

func (s *recordingSleeper) sleeps() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.durations...)
}

type gateSleeper struct {
	mu        sync.Mutex
	durations []time.Duration
	entered   chan int
	release   chan struct{}
}

func newGateSleeper() *gateSleeper {
	return &gateSleeper{
		entered: make(chan int, 16),
		release: make(chan struct{}),
	}
}

func (s *gateSleeper) Sleep(ctx context.Context, duration time.Duration) error {
	s.mu.Lock()
	s.durations = append(s.durations, duration)
	index := len(s.durations)
	s.mu.Unlock()
	select {
	case s.entered <- index:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func jobRecord(id, source, title, company string) job.Job {
	return job.Job{
		ID:          id,
		Source:      source,
		Title:       title,
		Company:     company,
		Location:    "Santiago",
		URL:         "https://example.com/" + source + "/" + id,
		Description: "Descripcion de la vacante para " + title,
	}
}

func matchingWithDetector(language lingua.Language) *matching.Matcher {
	return matching.NewWithCatalog(nil, fakeDetector{language: language})
}

func waitFor(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func channelConfig(guildID, channelID int64) store.GuildMonitorConfig {
	return store.GuildMonitorConfig{
		GuildID:              guildID,
		ChannelID:            &channelID,
		Keywords:             []string{"python"},
		MaxAgeHours:          24,
		Portals:              []string{"linkedin"},
		BlacklistedCompanies: []string{},
		BlacklistedKeywords:  []string{},
		Languages:            []string{},
	}
}

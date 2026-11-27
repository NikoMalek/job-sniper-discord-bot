package bot

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

type fakeOnboardingGateway struct {
	channels       map[int64]ChannelInfo
	channelErr     map[int64]error
	users          map[int64]UserInfo
	userErr        map[int64]error
	sendChannelErr map[int64]error
	sendUserErr    error

	mu           sync.Mutex
	sentChannels []int64
	sentUsers    []int64
	embeds       []*Embed
}

func (g *fakeOnboardingGateway) Channel(_ context.Context, channelID int64) (ChannelInfo, error) {
	if err := g.channelErr[channelID]; err != nil {
		return ChannelInfo{}, err
	}
	channel, ok := g.channels[channelID]
	if !ok {
		return ChannelInfo{}, fmt.Errorf("channel %d not found", channelID)
	}
	return channel, nil
}

func (g *fakeOnboardingGateway) User(_ context.Context, _, userID int64) (UserInfo, error) {
	if err := g.userErr[userID]; err != nil {
		return UserInfo{}, err
	}
	return g.users[userID], nil
}

func (g *fakeOnboardingGateway) SendChannel(_ context.Context, channelID int64, embed *Embed) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sentChannels = append(g.sentChannels, channelID)
	g.embeds = append(g.embeds, embed)
	return g.sendChannelErr[channelID]
}

func (g *fakeOnboardingGateway) SendUser(_ context.Context, userID int64, embed *Embed) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sentUsers = append(g.sentUsers, userID)
	g.embeds = append(g.embeds, embed)
	return g.sendUserErr
}

func (g *fakeOnboardingGateway) snapshot() ([]int64, []int64, []*Embed) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]int64(nil), g.sentChannels...), append([]int64(nil), g.sentUsers...), append([]*Embed(nil), g.embeds...)
}

func TestSendGuideUsesOrderedEligibleChannels(t *testing.T) {
	st := &fakeStore{config: channelConfig(10)}
	gateway := &fakeOnboardingGateway{
		channels: map[int64]ChannelInfo{
			10: {ID: 10, GuildID: 1, CanSend: true, CanEmbed: true},
			20: {ID: 20, GuildID: 1, CanSend: false, CanEmbed: true},
			30: {ID: 30, GuildID: 1, CanSend: true, CanEmbed: true},
			40: {ID: 40, GuildID: 1, CanSend: true, CanEmbed: true},
		},
		sendChannelErr: map[int64]error{10: errors.New("forbidden")},
	}
	onboarding, err := NewOnboarding(st, gateway)
	if err != nil {
		t.Fatalf("NewOnboarding() error = %v", err)
	}
	guild := GuildSnapshot{
		ID:              1,
		SystemChannelID: int64Pointer(20),
		OwnerID:         99,
		TextChannels: []ChannelInfo{
			{ID: 20, GuildID: 1, CanSend: false, CanEmbed: true},
			{ID: 10, GuildID: 1, CanSend: true, CanEmbed: true},
			{ID: 30, GuildID: 1, CanSend: true, CanEmbed: true},
			{ID: 40, GuildID: 1, CanSend: true, CanEmbed: true},
		},
	}
	delivered, err := onboarding.SendGuide(context.Background(), guild)
	if err != nil {
		t.Fatalf("SendGuide() error = %v", err)
	}
	if !delivered {
		t.Fatal("SendGuide() = false, want true")
	}
	channels, users, embeds := gateway.snapshot()
	if want := []int64{10, 30}; !reflect.DeepEqual(channels, want) {
		t.Fatalf("sent channels = %v, want %v", channels, want)
	}
	if len(users) != 0 {
		t.Fatalf("sent owner DMs = %v, want none", users)
	}
	if len(embeds) != 2 || embeds[0].Title != "¡Gracias por invitar a Job Sniper! 🎯" {
		t.Fatalf("embeds = %#v", embeds)
	}
}

func TestSendGuideFallsBackToOwnerDM(t *testing.T) {
	st := &fakeStore{}
	gateway := &fakeOnboardingGateway{
		users: map[int64]UserInfo{99: {ID: 99, Available: true}},
	}
	onboarding, err := NewOnboarding(st, gateway)
	if err != nil {
		t.Fatalf("NewOnboarding() error = %v", err)
	}
	delivered, err := onboarding.SendGuide(context.Background(), GuildSnapshot{ID: 1, OwnerID: 99})
	if err != nil {
		t.Fatalf("SendGuide() error = %v", err)
	}
	if !delivered {
		t.Fatal("SendGuide() = false, want true")
	}
	_, users, embeds := gateway.snapshot()
	if want := []int64{99}; !reflect.DeepEqual(users, want) {
		t.Fatalf("sent users = %v, want %v", users, want)
	}
	if len(embeds) != 1 {
		t.Fatalf("embeds = %#v", embeds)
	}
}

func TestSendGuideReturnsFalseWhenOwnerUnavailable(t *testing.T) {
	st := &fakeStore{}
	gateway := &fakeOnboardingGateway{userErr: map[int64]error{99: errors.New("not found")}}
	onboarding, err := NewOnboarding(st, gateway)
	if err != nil {
		t.Fatalf("NewOnboarding() error = %v", err)
	}
	delivered, err := onboarding.SendGuide(context.Background(), GuildSnapshot{ID: 1, OwnerID: 99})
	if err != nil || delivered {
		t.Fatalf("SendGuide() = %v, %v; want false, nil", delivered, err)
	}
	_, users, _ := gateway.snapshot()
	if len(users) != 0 {
		t.Fatalf("sent users = %v, want none", users)
	}
}

func TestCandidateChannelsSkipsConfiguredChannelFromOtherGuild(t *testing.T) {
	st := &fakeStore{config: channelConfig(10)}
	gateway := &fakeOnboardingGateway{
		channels: map[int64]ChannelInfo{
			10: {ID: 10, GuildID: 999, CanSend: true, CanEmbed: true},
		},
		users: map[int64]UserInfo{99: {ID: 99, Available: true}},
	}
	onboarding, err := NewOnboarding(st, gateway)
	if err != nil {
		t.Fatalf("NewOnboarding() error = %v", err)
	}
	channels, err := onboarding.candidateChannels(context.Background(), GuildSnapshot{
		ID:           1,
		OwnerID:      99,
		TextChannels: []ChannelInfo{{ID: 10, GuildID: 1, CanSend: true, CanEmbed: true}},
	})
	if err != nil {
		t.Fatalf("candidateChannels() error = %v", err)
	}
	if len(channels) != 0 {
		t.Fatalf("candidate channels = %#v, want none", channels)
	}
}

func TestSendIfNeededSkipsAlreadySentAndRecordsSuccess(t *testing.T) {
	st := &fakeStore{setupSent: true}
	gateway := &fakeOnboardingGateway{users: map[int64]UserInfo{99: {ID: 99, Available: true}}}
	onboarding, err := NewOnboarding(st, gateway)
	if err != nil {
		t.Fatalf("NewOnboarding() error = %v", err)
	}
	if err := onboarding.SendIfNeeded(context.Background(), GuildSnapshot{ID: 1}); err != nil {
		t.Fatalf("SendIfNeeded() error = %v", err)
	}
	channels, users, _ := gateway.snapshot()
	if len(channels) != 0 || len(users) != 0 || st.markCount != 0 {
		t.Fatalf("gateway = %v, %v; markCount = %d", channels, users, st.markCount)
	}

	st.setupSent = false
	if err := onboarding.SendIfNeeded(context.Background(), GuildSnapshot{ID: 2, OwnerID: 99}); err != nil {
		t.Fatalf("SendIfNeeded() error = %v", err)
	}
	if st.markCount != 1 {
		t.Fatalf("markCount = %d, want 1", st.markCount)
	}
}

func TestSendIfNeededWrapsStoreErrors(t *testing.T) {
	st := &fakeStore{hasSetupErr: errors.New("read failed")}
	gateway := &fakeOnboardingGateway{}
	onboarding, err := NewOnboarding(st, gateway)
	if err != nil {
		t.Fatalf("NewOnboarding() error = %v", err)
	}
	err = onboarding.SendIfNeeded(context.Background(), GuildSnapshot{ID: 1})
	if err == nil || !errors.Is(err, st.hasSetupErr) {
		t.Fatalf("SendIfNeeded() error = %v, want wrapped read error", err)
	}

	st.hasSetupErr = nil
	st.markSetupErr = errors.New("write failed")
	gateway.users = map[int64]UserInfo{99: {ID: 99, Available: true}}
	err = onboarding.SendIfNeeded(context.Background(), GuildSnapshot{ID: 1, OwnerID: 99})
	if err == nil || !errors.Is(err, st.markSetupErr) {
		t.Fatalf("SendIfNeeded() error = %v, want wrapped write error", err)
	}
}

func TestOnReadyChecksExistingGuildsOnlyOnce(t *testing.T) {
	st := &fakeStore{}
	gateway := &fakeOnboardingGateway{
		users: map[int64]UserInfo{
			1: {ID: 1, Available: true},
			2: {ID: 2, Available: true},
		},
	}
	onboarding, err := NewOnboarding(st, gateway)
	if err != nil {
		t.Fatalf("NewOnboarding() error = %v", err)
	}
	guilds := []GuildSnapshot{{ID: 1, OwnerID: 1}, {ID: 2, OwnerID: 2}}
	onboarding.OnReady(context.Background(), guilds)
	onboarding.OnReady(context.Background(), guilds)
	if st.setupChecks != 2 {
		t.Fatalf("setup checks = %d, want 2", st.setupChecks)
	}
	if st.markCount != 2 {
		t.Fatalf("markCount = %d, want 2", st.markCount)
	}
	_, users, _ := gateway.snapshot()
	if want := []int64{1, 2}; !reflect.DeepEqual(users, want) {
		t.Fatalf("sent users = %v, want %v", users, want)
	}
}

func TestConcurrentSendIfNeededDeliversOnce(t *testing.T) {
	st := &fakeStore{}
	gateway := &fakeOnboardingGateway{users: map[int64]UserInfo{99: {ID: 99, Available: true}}}
	onboarding, err := NewOnboarding(st, gateway)
	if err != nil {
		t.Fatalf("NewOnboarding() error = %v", err)
	}
	var wait sync.WaitGroup
	for i := 0; i < 10; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := onboarding.SendIfNeeded(context.Background(), GuildSnapshot{ID: 1, OwnerID: 99}); err != nil {
				t.Errorf("SendIfNeeded() error = %v", err)
			}
		}()
	}
	wait.Wait()
	if st.markCount != 1 {
		t.Fatalf("markCount = %d, want 1", st.markCount)
	}
	_, users, _ := gateway.snapshot()
	if want := []int64{99}; !reflect.DeepEqual(users, want) {
		t.Fatalf("sent users = %v, want %v", users, want)
	}
}

func TestNewOnboardingRequiresDependencies(t *testing.T) {
	gateway := &fakeOnboardingGateway{}
	if _, err := NewOnboarding(nil, gateway); err == nil {
		t.Fatal("NewOnboarding(nil, gateway) error = nil, want error")
	}
	if _, err := NewOnboarding(&fakeStore{}, nil); err == nil {
		t.Fatal("NewOnboarding(store, nil) error = nil, want error")
	}
}

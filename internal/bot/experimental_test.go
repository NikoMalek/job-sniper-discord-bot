package bot

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/scrapers"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/store"
	"github.com/bwmarrin/discordgo"
)

type recordedSync struct {
	guildID int64
	enabled bool
}

func newTestExperimental(
	t *testing.T,
) (*Experimental, *fakeStore, *fakeInteractionSender, *[]recordedSync) {
	t.Helper()
	st := &fakeStore{}
	repository := st.experimentalStore()
	repository.summaries = make(map[int64]store.ApplicationSummary)
	repository.applications = make(map[int64][]store.ApplicationRecord)
	repository.tracked = make(map[string]store.TrackedJob)
	sender := &fakeInteractionSender{}
	calls := &[]recordedSync{}
	syncErr := error(nil)
	exp, err := NewExperimental(ExperimentalOptions{
		Store:    st,
		Registry: basicRegistry(),
		Messages: sender,
		Sync: func(_ context.Context, guildID int64, enabled bool) error {
			*calls = append(*calls, recordedSync{guildID: guildID, enabled: enabled})
			return syncErr
		},
		Now: func() time.Time {
			return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatalf("NewExperimental() error = %v", err)
	}
	return exp, st, sender, calls
}

func expCommandInteraction(
	guildID, userID int64,
	name string,
	options ...*discordgo.ApplicationCommandInteractionDataOption,
) *discordgo.Interaction {
	return &discordgo.Interaction{
		ID:      "cmd-" + name,
		GuildID: strconv.FormatInt(guildID, 10),
		Type:    discordgo.InteractionApplicationCommand,
		Member:  &discordgo.Member{User: &discordgo.User{ID: strconv.FormatInt(userID, 10)}},
		Data: discordgo.ApplicationCommandInteractionData{
			Name:    name,
			Options: options,
		},
	}
}

func expComponentInteraction(
	guildID, userID int64, messageID, customID string,
) *discordgo.Interaction {
	return &discordgo.Interaction{
		ID:      "component-" + customID,
		GuildID: strconv.FormatInt(guildID, 10),
		Type:    discordgo.InteractionMessageComponent,
		Member:  &discordgo.Member{User: &discordgo.User{ID: strconv.FormatInt(userID, 10)}},
		Message: &discordgo.Message{ID: messageID},
		Data:    discordgo.MessageComponentInteractionData{CustomID: customID},
	}
}

func expModalInteraction(
	guildID, userID int64, customID string, choice string, notes string,
) *discordgo.Interaction {
	components := []discordgo.MessageComponent{}
	if choice != "" {
		components = append(components, discordgo.Label{
			Component: discordgo.SelectMenu{
				CustomID: jobApplyChoiceID,
				Values:   []string{choice},
			},
		})
	}
	if notes != "" {
		components = append(components, discordgo.Label{
			Component: discordgo.TextInput{CustomID: jobNotesID, Value: notes},
		})
	}
	return &discordgo.Interaction{
		ID:      "modal-" + customID,
		GuildID: strconv.FormatInt(guildID, 10),
		Type:    discordgo.InteractionModalSubmit,
		Member:  &discordgo.Member{User: &discordgo.User{ID: strconv.FormatInt(userID, 10)}},
		Data: discordgo.ModalSubmitInteractionData{
			CustomID:   customID,
			Components: components,
		},
	}
}

func expCommandData(interaction *discordgo.Interaction) *discordgo.ApplicationCommandInteractionData {
	data := interaction.ApplicationCommandData()
	return &data
}

func expSetCommandOptions(
	interaction *discordgo.Interaction,
	options []*discordgo.ApplicationCommandInteractionDataOption,
) {
	data := expCommandData(interaction)
	data.Options = options
	interaction.Data = *data
}

func expSetCommandResolved(
	interaction *discordgo.Interaction,
	resolved *discordgo.ApplicationCommandInteractionDataResolved,
) {
	data := expCommandData(interaction)
	data.Resolved = resolved
	interaction.Data = *data
}

func resultEmbed(t *testing.T, result *CommandResult) *discordgo.MessageEmbed {
	t.Helper()
	if result == nil || result.Respond == nil || len(result.Respond.Data.Embeds) != 1 {
		t.Fatalf("result = %#v, want one embedded response", result)
	}
	return result.Respond.Data.Embeds[0]
}

func resultContent(t *testing.T, result *CommandResult) string {
	t.Helper()
	if result == nil || result.Respond == nil {
		t.Fatalf("result = %#v, want a response", result)
	}
	return result.Respond.Data.Content
}

func buttonsFrom(components []discordgo.MessageComponent) []discordgo.Button {
	buttons := make([]discordgo.Button, 0)
	walkComponents(components, func(component discordgo.MessageComponent) {
		if button, ok := component.(discordgo.Button); ok {
			buttons = append(buttons, button)
		}
	})
	return buttons
}

func firstSelectMenu(components []discordgo.MessageComponent) *discordgo.SelectMenu {
	var found *discordgo.SelectMenu
	walkComponents(components, func(component discordgo.MessageComponent) {
		if found != nil {
			return
		}
		switch menu := component.(type) {
		case discordgo.SelectMenu:
			copy := menu
			found = &copy
		case *discordgo.SelectMenu:
			found = menu
		}
	})
	return found
}

func firstTextInput(components []discordgo.MessageComponent) *discordgo.TextInput {
	var found *discordgo.TextInput
	walkComponents(components, func(component discordgo.MessageComponent) {
		if found != nil {
			return
		}
		switch input := component.(type) {
		case discordgo.TextInput:
			copy := input
			found = &copy
		case *discordgo.TextInput:
			found = input
		}
	})
	return found
}

func TestExperimentalCommandDefinitions(t *testing.T) {
	global := GlobalCommandDefinitions()
	if len(global) != 1 {
		t.Fatalf("global commands = %d, want 1", len(global))
	}
	status := global[0]
	if status.Name != "experimental" ||
		status.Description != "Muestra información y estado; usa accion para activar o desactivar la Zona Experimental." {
		t.Fatalf("experimental command = %#v", status)
	}
	if len(status.Options) != 1 || status.Options[0].Name != "accion" ||
		!status.Options[0].Autocomplete || status.Options[0].Required {
		t.Fatalf("experimental options = %#v", status.Options)
	}
	if status.DefaultMemberPermissions == nil ||
		*status.DefaultMemberPermissions != int64(discordgo.PermissionManageGuild) {
		t.Fatalf("permissions = %v", status.DefaultMemberPermissions)
	}

	guildCommands := ExperimentalGuildCommandDefinitions()
	names := make([]string, 0, len(guildCommands))
	for _, command := range guildCommands {
		names = append(names, command.Name)
		if command.DefaultMemberPermissions == nil {
			t.Fatalf("%s is not restricted to manage-guild members", command.Name)
		}
	}
	wantNames := []string{"snipers", "applications", "debug"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("guild commands = %v, want %v", names, wantNames)
	}
	if len(guildCommands[2].Options) != 1 || !guildCommands[2].Options[0].Autocomplete {
		t.Fatalf("debug options = %#v", guildCommands[2].Options)
	}
}

func TestExperimentalStatusAndToggle(t *testing.T) {
	exp, st, sender, calls := newTestExperimental(t)
	ctx := context.Background()
	interaction := expCommandInteraction(10, 7, "experimental")

	result, err := exp.Command(ctx, interaction)
	if err != nil {
		t.Fatalf("Command() error = %v", err)
	}
	embed := resultEmbed(t, result)
	if embed.Title != "🧪 Zona Experimental" || embed.Color != experimentalColor {
		t.Fatalf("status embed = %#v", embed)
	}
	if len(embed.Fields) != 1 || embed.Fields[0].Value != "**Desactivada**" {
		t.Fatalf("status fields = %#v", embed.Fields)
	}
	if result.Respond.Data.Flags != discordgo.MessageFlagsEphemeral {
		t.Error("status response is not ephemeral")
	}

	expSetCommandOptions(interaction, []*discordgo.ApplicationCommandInteractionDataOption{
		{Name: "accion", Type: discordgo.ApplicationCommandOptionString, Value: "ACTIVAR"},
	})
	result, err = exp.Command(ctx, interaction)
	if err != nil {
		t.Fatalf("Command(activar) error = %v", err)
	}
	if enabled, _ := st.IsExperimentalEnabled(ctx, 10); !enabled {
		t.Error("experimental mode was not enabled")
	}
	if result.After == nil {
		t.Fatal("toggle did not schedule a command sync")
	}
	if err := result.After(ctx); err != nil {
		t.Fatalf("sync callback: %v", err)
	}
	if want := []recordedSync{{guildID: 10, enabled: true}}; !reflect.DeepEqual(*calls, want) {
		t.Fatalf("sync calls = %#v, want %#v", *calls, want)
	}

	result, err = exp.Command(ctx, interaction)
	if err != nil || result.After != nil {
		t.Fatalf("repeated toggle = %#v, %v; want no sync", result, err)
	}
	expCommandData(interaction).Options[0].Value = "maybe"
	if result, err = exp.Command(ctx, interaction); err != nil {
		t.Fatalf("Command(invalid) error = %v", err)
	}
	if got := resultContent(t, result); got != "No se pudo completar el comando. Revisa los logs del bot." {
		t.Errorf("invalid action content = %q", got)
	}

	enabled, _ := st.IsExperimentalEnabled(ctx, 10)
	if !enabled {
		t.Fatal("test setup lost the enabled state")
	}
	expCommandData(interaction).Options[0].Value = "desactivar"
	result, err = exp.Command(ctx, interaction)
	if err != nil {
		t.Fatalf("Command(desactivar) error = %v", err)
	}
	if err := result.After(ctx); err != nil {
		t.Fatalf("sync callback: %v", err)
	}
	if enabled, _ := st.IsExperimentalEnabled(ctx, 10); enabled {
		t.Error("experimental mode was not disabled")
	}
	if got := (*calls)[1]; got != (recordedSync{guildID: 10, enabled: false}) {
		t.Errorf("second sync = %#v", got)
	}

	// A failed sync adds the localized footer to the already-sent response.
	exp.sync = func(context.Context, int64, bool) error {
		return errors.New("sync failed")
	}
	expCommandData(interaction).Options[0].Value = "activar"
	result, err = exp.Command(ctx, interaction)
	if err != nil {
		t.Fatalf("Command(activar) error = %v", err)
	}
	_ = result.After(ctx)
	responses, edits, originals := sender.snapshotExperimental()
	if len(responses) != 0 || len(edits) != 1 || originals != 0 {
		t.Fatalf("sender state = %d responses, %d edits, %d originals", len(responses), len(edits), originals)
	}
	if edits[0].Embeds == nil || (*edits[0].Embeds)[0].Footer == nil {
		t.Fatalf("edit = %#v, want a footer", edits[0])
	}
	if got := (*edits[0].Embeds)[0].Footer.Text; got != "Estado guardado; los comandos se sincronizarán al reiniciar." {
		t.Errorf("sync footer = %q", got)
	}
}

func TestExperimentalAutocomplete(t *testing.T) {
	exp, st, _, _ := newTestExperimental(t)
	ctx := context.Background()
	interaction := expCommandInteraction(10, 7, "experimental")
	expSetCommandOptions(interaction, []*discordgo.ApplicationCommandInteractionDataOption{
		{Name: "accion", Value: "", Focused: true},
	})
	want := []Choice{{Name: "Activar", Value: "activar"}}
	if got := exp.Autocomplete(ctx, interaction); !reflect.DeepEqual(got, want) {
		t.Fatalf("disabled autocomplete = %#v, want %#v", got, want)
	}

	_ = st.SetExperimentalEnabled(ctx, 10, true)
	expCommandData(interaction).Options[0].Value = "des"
	want = []Choice{{Name: "Desactivar", Value: "desactivar"}}
	if got := exp.Autocomplete(ctx, interaction); !reflect.DeepEqual(got, want) {
		t.Fatalf("enabled autocomplete = %#v, want %#v", got, want)
	}

	st.config.Locations = []string{"Chile"}
	st.config.Portals = []string{"linkedin", "chiletrabajos"}
	exp.registry = &fakeRegistry{portals: map[string]scrapers.Scraper{
		"linkedin":      &fakeScraper{slug: "linkedin", source: "LinkedIn"},
		"chiletrabajos": &fakeScraper{slug: "chiletrabajos", source: "ChileTrabajos", covered: []string{"CL"}},
	}}
	debug := expCommandInteraction(10, 7, "debug")
	expSetCommandOptions(debug, []*discordgo.ApplicationCommandInteractionDataOption{
		{Name: "source", Value: "linked", Focused: true},
	})
	want = []Choice{{Name: "LinkedIn", Value: "LinkedIn"}}
	if got := exp.Autocomplete(ctx, debug); !reflect.DeepEqual(got, want) {
		t.Fatalf("debug autocomplete = %#v, want %#v", got, want)
	}
}

func TestSnipersRankingEmbed(t *testing.T) {
	exp, st, _, _ := newTestExperimental(t)
	ctx := context.Background()
	repository := st.experimentalStore()
	repository.rankings = []store.SniperRanking{
		{UserID: 7, Applications: 3, Viewed: 5},
		{UserID: 8, Applications: 1, Viewed: 4},
		{UserID: 9, Applications: 0, Viewed: 2},
		{UserID: 10, Applications: 0, Viewed: 1},
	}
	repository.totalsApplied = 4
	repository.totalsViewed = 12

	result, err := exp.Command(ctx, expCommandInteraction(10, 7, "snipers"))
	if err != nil {
		t.Fatalf("Command(snipers) error = %v", err)
	}
	embed := resultEmbed(t, result)
	if embed.Title != "🎯 Ranking de Snipers" || embed.Color != experimentalColor {
		t.Fatalf("snipers embed = %#v", embed)
	}
	if result.Respond.Data.Flags != 0 {
		t.Error("snipers response should not be ephemeral")
	}
	wantSummary := "Resumen del servidor: `4` postulaciones enviadas · `12` ofertas revisadas"
	if !strings.Contains(embed.Description, wantSummary) {
		t.Errorf("description = %q", embed.Description)
	}
	for _, fragment := range []string{
		"🥇 <@7>", "🥈 <@8>", "🥉 <@9>", "**`4.`** <@10>", "-# → `3` postulaciones · `5` vistas",
	} {
		if !strings.Contains(embed.Description, fragment) {
			t.Errorf("description missing %q: %q", fragment, embed.Description)
		}
	}
}

func TestApplicationsEmbedAndPaginator(t *testing.T) {
	exp, st, sender, _ := newTestExperimental(t)
	ctx := context.Background()
	st.config.Languages = []string{"english"}
	repository := st.experimentalStore()
	summary := store.ApplicationSummary{Clicked: 6, Applied: 2, Skipped: 1, Viewed: 3}
	repository.summaries[7] = summary
	repository.applications[7] = []store.ApplicationRecord{
		{JobTitle: "Role 6", Company: "Acme", JobURL: "https://example.com/6", Source: "LinkedIn", ClickedAt: "2026-01-06T00:00:00Z"},
		{JobTitle: "Role 5", Company: "Acme", JobURL: "https://example.com/5", Source: "LinkedIn", ClickedAt: "2026-01-05T00:00:00Z"},
		{JobTitle: "Role 4", Company: "Acme", JobURL: "https://example.com/4", Source: "LinkedIn", ClickedAt: "2026-01-04T00:00:00Z"},
		{JobTitle: "Role 3", Company: "Acme", JobURL: "https://example.com/3", Source: "LinkedIn", ClickedAt: "2026-01-03T00:00:00Z"},
		{JobTitle: "Role 2", Company: "Acme", JobURL: "https://example.com/2", Source: "LinkedIn", ClickedAt: "2026-01-02T00:00:00Z"},
		{JobTitle: "Role 1", Company: "Acme", JobURL: "https://example.com/1", Source: "LinkedIn", ClickedAt: "2026-01-01T00:00:00Z"},
	}
	sender.originalID = "msg-apps"

	result, err := exp.Command(ctx, expCommandInteraction(10, 7, "applications"))
	if err != nil {
		t.Fatalf("Command(applications) error = %v", err)
	}
	embed := resultEmbed(t, result)
	if embed.Title != "Your applications" {
		t.Fatalf("title = %q", embed.Title)
	}
	if !strings.Contains(embed.Description, "6 viewed · 2 applied · 1 skipped · 3 unanswered") {
		t.Errorf("totals = %q", embed.Description)
	}
	if embed.Footer == nil || embed.Footer.Text != "Page 1/2 · 6 records" {
		t.Fatalf("footer = %#v", embed.Footer)
	}
	buttons := buttonsFrom(result.Respond.Data.Components)
	if len(buttons) != 2 || !buttons[0].Disabled || buttons[1].Disabled {
		t.Fatalf("paginator buttons = %#v", buttons)
	}
	if result.After == nil {
		t.Fatal("paginator was not registered")
	}
	if err := result.After(ctx); err != nil {
		t.Fatalf("register paginator: %v", err)
	}
	if exp.getView("msg-apps") == nil {
		t.Fatal("paginator session was not stored")
	}

	next := expComponentInteraction(10, 7, "msg-apps", "applications_next")
	edited, err := exp.HandleComponent(ctx, next)
	if err != nil || edited.Edit == nil {
		t.Fatalf("HandleComponent(next) = %#v, %v", edited, err)
	}
	pageTwo := (*edited.Edit.Embeds)[0]
	if pageTwo.Footer.Text != "Page 2/2 · 6 records" {
		t.Errorf("page two footer = %q", pageTwo.Footer.Text)
	}
	if !strings.Contains(pageTwo.Description, "Role 1") {
		t.Errorf("page two description = %q", pageTwo.Description)
	}
	nextButtons := buttonsFrom(*edited.Edit.Components)
	if len(nextButtons) != 2 || nextButtons[1].Disabled == false {
		t.Fatalf("page two buttons = %#v", nextButtons)
	}

	other := expCommandInteraction(10, 7, "applications")
	expSetCommandOptions(other, []*discordgo.ApplicationCommandInteractionDataOption{
		{Name: "user", Type: discordgo.ApplicationCommandOptionUser, Value: "8"},
	})
	expSetCommandResolved(other, &discordgo.ApplicationCommandInteractionDataResolved{
		Users: map[string]*discordgo.User{"8": {ID: "8", GlobalName: "Ada"}},
	})
	repository.summaries[8] = store.ApplicationSummary{}
	result, err = exp.Command(ctx, other)
	if err != nil {
		t.Fatalf("Command(applications @user) error = %v", err)
	}
	embed = resultEmbed(t, result)
	if embed.Title != "Ada's applications" ||
		!strings.Contains(embed.Description, "You are viewing **<@8>'s applications**.") {
		t.Fatalf("other-user embed = %#v", embed)
	}
}

func TestDebugEmbedAndPaginator(t *testing.T) {
	exp, st, sender, _ := newTestExperimental(t)
	ctx := context.Background()
	repository := st.experimentalStore()
	repository.hasDebugCount = true
	repository.debugCount = 6
	repository.debugReports = []store.DebugReport{
		{
			UserID: 7, JobTitle: "Role_6", JobURL: "https://example.com/6",
			Source: "LinkedIn", ClickedAt: "2026-01-06T00:00:00Z", Notes: "broken_link",
		},
		{
			UserID: 7, JobTitle: "Role 5", JobURL: "https://example.com/5",
			Source: "LinkedIn", ClickedAt: "2026-01-05T00:00:00Z", Notes: "note",
		},
		{
			UserID: 7, JobTitle: "Role 4", JobURL: "https://example.com/4",
			Source: "LinkedIn", ClickedAt: "2026-01-04T00:00:00Z", Notes: "note",
		},
		{
			UserID: 7, JobTitle: "Role 3", JobURL: "https://example.com/3",
			Source: "LinkedIn", ClickedAt: "2026-01-03T00:00:00Z", Notes: "note",
		},
		{
			UserID: 7, JobTitle: "Role 2", JobURL: "https://example.com/2",
			Source: "LinkedIn", ClickedAt: "2026-01-02T00:00:00Z", Notes: "note",
		},
		{
			UserID: 7, JobTitle: "Role 1", JobURL: "https://example.com/1",
			Source: "LinkedIn", ClickedAt: "2026-01-01T00:00:00Z", Notes: "note",
		},
	}
	sender.originalID = "msg-debug"
	interaction := expCommandInteraction(10, 7, "debug")
	expSetCommandOptions(interaction, []*discordgo.ApplicationCommandInteractionDataOption{
		{Name: "source", Type: discordgo.ApplicationCommandOptionString, Value: "LinkedIn"},
	})

	result, err := exp.Command(ctx, interaction)
	if err != nil {
		t.Fatalf("Command(debug) error = %v", err)
	}
	embed := resultEmbed(t, result)
	if result.Respond.Data.Flags != discordgo.MessageFlagsEphemeral {
		t.Error("debug response should be ephemeral")
	}
	if embed.Title != "🐞 Observaciones / debug" {
		t.Fatalf("title = %q", embed.Title)
	}
	wantLine := "`06/01/2026` · <@7> · LinkedIn · [Role\\_6](https://example.com/6) · Sin responder\n> broken\\_link"
	if !strings.Contains(embed.Description, wantLine) {
		t.Errorf("debug description = %q, want %q", embed.Description, wantLine)
	}
	if !strings.Contains(embed.Description, "Filtro: **LinkedIn**") {
		t.Errorf("debug filter missing: %q", embed.Description)
	}
	if embed.Footer == nil || embed.Footer.Text != "Página 1/2 · 6 registros" {
		t.Fatalf("footer = %#v", embed.Footer)
	}
	if result.After == nil {
		t.Fatal("debug paginator was not registered")
	}
	if err := result.After(ctx); err != nil {
		t.Fatalf("register paginator: %v", err)
	}
	edited, err := exp.HandleComponent(
		ctx, expComponentInteraction(10, 7, "msg-debug", "debug_next"),
	)
	if err != nil || edited.Edit == nil {
		t.Fatalf("HandleComponent(debug_next) = %#v, %v", edited, err)
	}
	if footer := (*edited.Edit.Embeds)[0].Footer.Text; footer != "Página 2/2 · 6 registros" {
		t.Errorf("page two footer = %q", footer)
	}
}

func TestTrackingButtonAndModalSubmit(t *testing.T) {
	exp, st, _, _ := newTestExperimental(t)
	ctx := context.Background()
	repository := st.experimentalStore()
	_ = repository.SetExperimentalEnabled(ctx, 10, true)
	repository.tracked = map[string]store.TrackedJob{
		"tok": {
			Token: "tok", GuildID: 10, JobID: "job-1", Source: "LinkedIn",
			JobURL: "https://example.com/job-1", JobTitle: "Developer", Company: "Acme",
		},
	}

	button := expComponentInteraction(10, 7, "msg-job", "job_track:tok")
	result, err := exp.HandleComponent(ctx, button)
	if err != nil {
		t.Fatalf("HandleComponent(track) error = %v", err)
	}
	if result.Respond == nil || result.Respond.Type != discordgo.InteractionResponseModal {
		t.Fatalf("tracking response = %#v, want a modal", result)
	}
	if result.Respond.Data.CustomID != "job_modal:tok:7" {
		t.Errorf("modal custom ID = %q", result.Respond.Data.CustomID)
	}
	if result.Respond.Data.Title != "Registrar postulación" {
		t.Errorf("modal title = %q", result.Respond.Data.Title)
	}
	menu := firstSelectMenu(result.Respond.Data.Components)
	if menu == nil || menu.CustomID != jobApplyChoiceID ||
		len(menu.Options) != 2 || menu.Required == nil || !*menu.Required {
		t.Fatalf("select menu = %#v", menu)
	}
	input := firstTextInput(result.Respond.Data.Components)
	if input == nil || input.CustomID != jobNotesID ||
		input.Style != discordgo.TextInputParagraph || input.MaxLength != 250 {
		t.Fatalf("notes input = %#v", input)
	}

	submit := expModalInteraction(10, 7, "job_modal:tok:7", "yes", "  broken link  ")
	result, err = exp.HandleModalSubmit(ctx, submit)
	if err != nil || !result.Defer {
		t.Fatalf("HandleModalSubmit() = %#v, %v; want defer", result, err)
	}
	row, err := repository.GetJobInteraction(ctx, 1)
	if err != nil || row == nil {
		t.Fatalf("GetJobInteraction() = %#v, %v", row, err)
	}
	if row.Applied == nil || !*row.Applied || row.Notes == nil || *row.Notes != "broken link" {
		t.Fatalf("stored application = %#v", row)
	}
	if result, err = exp.HandleModalSubmit(ctx, submit); err != nil {
		t.Fatalf("repeated submit error = %v", err)
	}
	if got := resultContent(t, result); got != "Esa oferta ya no está disponible para registrar." {
		t.Errorf("repeated submit = %q", got)
	}

	if _, err = exp.HandleComponent(ctx, button); err != nil {
		t.Fatalf("tracking click before wrong-user submit: %v", err)
	}
	wrongUser := expModalInteraction(10, 8, "job_modal:tok:7", "no", "")
	if result, err = exp.HandleModalSubmit(ctx, wrongUser); err != nil {
		t.Fatalf("wrong-user submit error = %v", err)
	}
	if got := resultContent(t, result); got != "Ese registro pertenece a otro usuario." {
		t.Errorf("wrong-user submit = %q", got)
	}

	missingChoice := expModalInteraction(10, 7, "job_modal:tok:7", "", "")
	if _, err = exp.HandleComponent(ctx, button); err != nil {
		t.Fatalf("second tracking click: %v", err)
	}
	if result, err = exp.HandleModalSubmit(ctx, missingChoice); err != nil || !result.Defer {
		t.Fatalf("missing-choice submit = %#v, %v", result, err)
	}
	row, _ = repository.GetJobInteraction(ctx, 1)
	if row.Applied == nil || *row.Applied {
		t.Errorf("missing choice applied = %v, want false", row.Applied)
	}
}

func TestTrackingButtonExpiredAndDisabled(t *testing.T) {
	exp, st, _, _ := newTestExperimental(t)
	ctx := context.Background()
	repository := st.experimentalStore()
	repository.tracked = map[string]store.TrackedJob{
		"other": {Token: "other", GuildID: 99, JobURL: "https://example.com/other"},
	}

	tests := []struct {
		name          string
		customID      string
		wantText      string
		wantEphemeral bool
	}{
		{"unknown token", "job_track:missing", "Esa oferta ya no está disponible para registrar.", true},
		{"wrong guild", "job_track:other", "Esa oferta ya no está disponible para registrar.", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := exp.HandleComponent(ctx, expComponentInteraction(10, 7, "msg", test.customID))
			if err != nil {
				t.Fatalf("HandleComponent() error = %v", err)
			}
			if got := resultContent(t, result); !strings.Contains(got, test.wantText) {
				t.Fatalf("content = %q, want %q", got, test.wantText)
			}
			if result.Respond.Data.Flags != discordgo.MessageFlagsEphemeral {
				t.Error("expired response should be ephemeral")
			}
		})
	}

	repository.tracked["tok"] = store.TrackedJob{
		Token: "tok", GuildID: 10, JobURL: "https://example.com/job",
	}
	result, err := exp.HandleComponent(ctx, expComponentInteraction(10, 7, "msg", "job_track:tok"))
	if err != nil {
		t.Fatalf("disabled tracking button error = %v", err)
	}
	got := resultContent(t, result)
	if !strings.Contains(got, "El modo experimental está desactivado en este servidor.") ||
		!strings.Contains(got, "[Abrir enlace de la oferta](https://example.com/job)") {
		t.Fatalf("disabled content = %q", got)
	}
}

func TestExperimentalErrorResult(t *testing.T) {
	exp, _, _, _ := newTestExperimental(t)
	ctx := context.Background()
	permission := exp.ErrorResult(ctx, 10, ErrPermissionDenied)
	if got := resultContent(t, permission); got != "No tienes permiso para usar este comando." {
		t.Errorf("permission error = %q", got)
	}
	generic := exp.ErrorResult(ctx, 10, errors.New("boom"))
	if got := resultContent(t, generic); got != "No se pudo completar el comando. Revisa los logs del bot." {
		t.Errorf("generic error = %q", got)
	}
}

func TestExperimentalFormattingHelpers(t *testing.T) {
	epoch := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	wantTimestamp := "<t:" + strconv.FormatInt(epoch.Unix(), 10) + ":d> · <t:" +
		strconv.FormatInt(epoch.Unix(), 10) + ":t>"
	if got := formatClickedAt("2026-01-02T03:04:05Z"); got != wantTimestamp {
		t.Errorf("formatClickedAt() = %q, want %q", got, wantTimestamp)
	}
	if got := formatClickedAt("not a date"); got != "—" {
		t.Errorf("formatClickedAt(invalid) = %q", got)
	}
	if got := formatClickedDate("2026-01-02T03:04:05+01:00"); got != "02/01/2026" {
		t.Errorf("formatClickedDate() = %q", got)
	}
	if got := formatClickedDate("not a date"); got != "not a date" {
		t.Errorf("formatClickedDate(invalid) = %q", got)
	}
	if got := escapeMarkdown("_x_ [link](https://a/b_c) > # heading"); got != "\\_x\\_ \\[link](https://a/b_c) > # heading" {
		t.Errorf("escapeMarkdown() = %q", got)
	}
	if got := statusText(nil, "spanish"); got != "Sin responder" {
		t.Errorf("statusText(nil) = %q", got)
	}
	yes := true
	if got := statusText(&yes, "spanish"); got != "Postulado" {
		t.Errorf("statusText(true) = %q", got)
	}
	no := false
	if got := statusText(&no, "spanish"); got != "Omitido" {
		t.Errorf("statusText(false) = %q", got)
	}
	if pageCount(0) != 1 || pageCount(5) != 1 || pageCount(6) != 2 {
		t.Error("pageCount() returned unexpected page totals")
	}
	if got := rankingLines(nil, "spanish"); got != "Aún no hay registros." {
		t.Errorf("rankingLines(nil) = %q", got)
	}
}

func TestExperimentalSessionsExpireAndRefresh(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	current := base
	type timer struct {
		delay time.Duration
		run   func()
	}
	timers := make([]timer, 0, 2)
	sender := &fakeInteractionSender{}
	exp, err := NewExperimental(ExperimentalOptions{
		Store:    &fakeStore{},
		Messages: sender,
		Now:      func() time.Time { return current },
		AfterFunc: func(delay time.Duration, run func()) {
			timers = append(timers, timer{delay: delay, run: run})
		},
	})
	if err != nil {
		t.Fatalf("NewExperimental() error = %v", err)
	}
	runTimers := func() {
		for _, scheduled := range timers {
			scheduled.run()
		}
	}

	exp.putView("view", &viewSession{
		Kind: "applications", Language: "spanish", ChannelID: "20", MessageID: "30",
	})
	exp.putModal("modal", &modalSession{})
	if len(timers) != 2 || timers[0].delay != experimentalViewTTL ||
		timers[1].delay != experimentalModalTTL {
		t.Fatalf("timers = %#v", timers)
	}

	current = base.Add(170 * time.Second)
	runTimers()
	if exp.getView("view") == nil || exp.getModal("modal") == nil {
		t.Fatal("sessions expired before their deadlines")
	}

	// Reading a view extends it, so the original timer must not remove it.
	current = base.Add(310 * time.Second)
	runTimers()
	if exp.getView("view") == nil {
		t.Fatal("refreshed view was removed by its original timer")
	}
	if exp.getModal("modal") != nil {
		t.Fatal("expired modal was not removed")
	}

	current = base.Add(500 * time.Second)
	runTimers()
	if exp.getView("view") != nil {
		t.Fatal("expired view was not removed")
	}
	edits := sender.messageEditSnapshot()
	if len(edits) != 1 || edits[0].channelID != "20" || edits[0].messageID != "30" {
		t.Fatalf("message edits = %#v", edits)
	}
	buttons := buttonsFrom(edits[0].components)
	if len(buttons) != 2 || !buttons[0].Disabled || !buttons[1].Disabled {
		t.Fatalf("disabled buttons = %#v", buttons)
	}
}

func TestModalComponentsMarshalToDiscordLayout(t *testing.T) {
	type componentPayload struct {
		Type        int               `json:"type"`
		Content     string            `json:"content"`
		Label       string            `json:"label"`
		Component   *componentPayload `json:"component"`
		CustomID    string            `json:"custom_id"`
		Placeholder string            `json:"placeholder"`
		MinValues   *int              `json:"min_values"`
		MaxValues   int               `json:"max_values"`
		Required    *bool             `json:"required"`
		Style       int               `json:"style"`
		MaxLength   int               `json:"max_length"`
		Options     []struct {
			Label string `json:"label"`
			Value string `json:"value"`
		} `json:"options"`
	}

	tracked := &store.TrackedJob{
		Token: "token-1", GuildID: 10, JobID: "job-1", Source: "LinkedIn",
		JobURL: "https://example.test/job-1", JobTitle: "Developer", Company: "Acme",
	}
	data, err := json.Marshal(modalComponents("spanish", tracked))
	if err != nil {
		t.Fatalf("json.Marshal(modal components) error = %v", err)
	}
	var components []componentPayload
	if err := json.Unmarshal(data, &components); err != nil {
		t.Fatalf("json.Unmarshal(modal components) error = %v", err)
	}
	if len(components) != 3 {
		t.Fatalf("top-level components = %d, want 3: %s", len(components), data)
	}

	text := components[0]
	if text.Type != int(discordgo.TextDisplayComponent) {
		t.Errorf("text display type = %d, want %d", text.Type, discordgo.TextDisplayComponent)
	}
	wantContent := "[Abrir enlace de la oferta](https://example.test/job-1)\n\n" +
		"¿Postulaste a **Developer** en **Acme**?"
	if text.Content != wantContent {
		t.Errorf("text display content = %q, want %q", text.Content, wantContent)
	}

	choice := components[1]
	if choice.Type != int(discordgo.LabelComponent) || choice.Label != "¿Postulaste?" ||
		choice.Component == nil {
		t.Fatalf("choice label = %#v", choice)
	}
	selectMenu := choice.Component
	if selectMenu.Type != int(discordgo.SelectMenuComponent) ||
		selectMenu.CustomID != jobApplyChoiceID {
		t.Fatalf("select menu = %#v", selectMenu)
	}
	if selectMenu.MinValues == nil || *selectMenu.MinValues != 1 ||
		selectMenu.MaxValues != 1 || selectMenu.Required == nil || !*selectMenu.Required {
		t.Errorf("select constraints = min %v, max %d, required %v",
			selectMenu.MinValues, selectMenu.MaxValues, selectMenu.Required)
	}
	wantOptions := []struct {
		Label string `json:"label"`
		Value string `json:"value"`
	}{
		{Label: "✅ Sí, postulé", Value: "yes"},
		{Label: "❌ No, no aplica", Value: "no"},
	}
	if !reflect.DeepEqual(selectMenu.Options, wantOptions) {
		t.Errorf("select options = %#v, want %#v", selectMenu.Options, wantOptions)
	}

	notes := components[2]
	if notes.Type != int(discordgo.LabelComponent) ||
		notes.Label != "Observación / debug (opcional)" || notes.Component == nil {
		t.Fatalf("notes label = %#v", notes)
	}
	input := notes.Component
	if input.Type != int(discordgo.TextInputComponent) ||
		input.CustomID != jobNotesID || input.MaxLength != 250 {
		t.Fatalf("notes input = %#v", input)
	}
	if input.Style != int(discordgo.TextInputParagraph) {
		t.Errorf("notes style = %d, want %d", input.Style, discordgo.TextInputParagraph)
	}
	if input.Required == nil || *input.Required {
		t.Errorf("notes required = %v, want false", input.Required)
	}
}

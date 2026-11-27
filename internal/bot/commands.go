package bot

import (
	"strings"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/languages"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/scrapers"
	"github.com/bwmarrin/discordgo"
)

// LanguageChoices returns the slash-command choices for canonical languages.
func LanguageChoices() []Choice {
	choices := make([]Choice, 0, len(languages.LanguageLabelOrder))
	for _, code := range languages.LanguageLabelOrder {
		choices = append(choices, Choice{
			Name:  titleLabel(languages.Label(code)) + " (" + code + ")",
			Value: code,
		})
	}
	return choices
}

// PortalChoices returns the slash-command choices for registered portals.
func PortalChoices(registry PortalRegistry) []Choice {
	if registry == nil {
		registry = scrapers.DefaultRegistry()
	}
	choices := make([]Choice, 0)
	for _, slug := range orderedPortalSlugs(registry, defaultPortalOrder()) {
		scraper := registry.Get(slug)
		if scraper == nil {
			continue
		}
		choices = append(choices, Choice{Name: scraper.Source(), Value: slug})
	}
	return choices
}

func titleLabel(label string) string {
	if label == "" {
		return label
	}
	runes := []rune(label)
	return strings.ToUpper(string(runes[0])) + string(runes[1:])
}

func boolPointer(value bool) *bool { return &value }

func int64Pointer(value int64) *int64 { return &value }

func adminCommand(name, description string, options ...*discordgo.ApplicationCommandOption) *discordgo.ApplicationCommand {
	manageGuild := int64(discordgo.PermissionManageGuild)
	contexts := []discordgo.InteractionContextType{discordgo.InteractionContextGuild}
	integrations := []discordgo.ApplicationIntegrationType{discordgo.ApplicationIntegrationGuildInstall}
	return &discordgo.ApplicationCommand{
		Name:                     name,
		Description:              description,
		Options:                  options,
		DefaultMemberPermissions: &manageGuild,
		DMPermission:             boolPointer(false),
		Contexts:                 &contexts,
		IntegrationTypes:         &integrations,
	}
}

func stringOption(name, description string, required bool) *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{
		Type:        discordgo.ApplicationCommandOptionString,
		Name:        name,
		Description: description,
		Required:    required,
	}
}

func autocompleteOption(name, description string) *discordgo.ApplicationCommandOption {
	option := stringOption(name, description, true)
	option.Autocomplete = true
	return option
}

func choiceOption(name, description string, choices []Choice) *discordgo.ApplicationCommandOption {
	option := stringOption(name, description, true)
	option.Choices = stringCommandChoices(choices)
	return option
}

func stringCommandChoices(choices []Choice) []*discordgo.ApplicationCommandOptionChoice {
	result := make([]*discordgo.ApplicationCommandOptionChoice, 0, len(choices))
	for _, choice := range choices {
		result = append(result, &discordgo.ApplicationCommandOptionChoice{
			Name:  choice.Name,
			Value: choice.Value,
		})
	}
	return result
}

// AdminCommandDefinitions returns the globally registered admin commands.
func AdminCommandDefinitions(portals []Choice) []*discordgo.ApplicationCommand {
	languages := LanguageChoices()
	return []*discordgo.ApplicationCommand{
		adminCommand(
			"set-channel", "Configura el canal de notificaciones.",
			&discordgo.ApplicationCommandOption{
				Type:         discordgo.ApplicationCommandOptionChannel,
				Name:         "channel",
				Description:  "channel",
				Required:     true,
				ChannelTypes: []discordgo.ChannelType{discordgo.ChannelTypeGuildText},
			},
		),
		adminCommand(
			"set-location", "Configura la ubicación de búsqueda (reemplaza las anteriores).",
			stringOption("location", "Ej.: Worldwide · Remoto · Santiago, Chile · Remoto, España", true),
		),
		adminCommand(
			"add-location", "Agrega una ubicación de búsqueda (máximo 3).",
			stringOption("location", "Ej.: Osorno, Chile · La Unión, Chile · Remoto, España", true),
		),
		adminCommand(
			"remove-location", "Quita una ubicación de búsqueda configurada.",
			autocompleteOption("location", "Ubicación que quieres quitar."),
		),
		adminCommand(
			"set-max-age", "Configura la antigüedad máxima en horas (0,5 = 30 minutos).",
			&discordgo.ApplicationCommandOption{
				Type:        discordgo.ApplicationCommandOptionNumber,
				Name:        "hours",
				Description: "Horas hacia atrás. En español usa coma decimal: 0,5 son 30 minutos.",
				Required:    true,
			},
		),
		adminCommand("add-keyword", "Agrega una palabra clave.", stringOption("keyword", "keyword", true)),
		adminCommand("remove-keyword", "Elimina una palabra clave.", autocompleteOption("keyword", "keyword")),
		adminCommand("add-blacklist-company", "Ignora vacantes de una empresa.", stringOption("company", "company", true)),
		adminCommand("remove-blacklist-company", "Deja de ignorar una empresa.", stringOption("company", "company", true)),
		adminCommand(
			"add-blacklist-keyword",
			"Ignora vacantes por palabras en el título (opcional: incluir alias comunes).",
			stringOption("keyword", "Palabras a excluir en el título, separadas por comas (ej. practicante, docente).", true),
			&discordgo.ApplicationCommandOption{
				Type:        discordgo.ApplicationCommandOptionInteger,
				Name:        "incluir_alias",
				Description: "Si es Sí, agrega también variantes comunes (ej. práctica → practicante, pasante, intern).",
				Choices: []*discordgo.ApplicationCommandOptionChoice{
					{Name: "Sí (incluir alias comunes)", Value: 1},
					{Name: "No (solo las palabras indicadas)", Value: 0},
				},
			},
		),
		adminCommand(
			"remove-blacklist-keyword",
			"Deja de ignorar vacantes con una palabra clave excluida.",
			autocompleteOption("keyword", "Palabra o palabras separadas por comas que quieres quitar de la exclusión."),
		),
		adminCommand("help", "Muestra cómo configurar Job Sniper."),
		adminCommand("add-language", "Configura idiomas permitidos para vacantes.", choiceOption("language", "Idioma que quieres activar.", languages)),
		adminCommand("remove-language", "Elimina idiomas permitidos.", choiceOption("language", "Idioma que quieres desactivar.", languages)),
		adminCommand("add-portal", "Activa un portal de empleo para este servidor.", choiceOption("portal", "Portal que quieres activar.", portals)),
		adminCommand("remove-portal", "Desactiva un portal de empleo de este servidor.", autocompleteOption("portal", "Portal activo o pausado que quieres desactivar.")),
		adminCommand("list-languages", "Muestra los idiomas disponibles y los activos en este servidor."),
		adminCommand("list-portals", "Muestra los portales disponibles, su estado y cobertura."),
		adminCommand("list-locations", "Muestra tus ubicaciones y cómo se traducen en cada portal."),
		adminCommand("list-keywords", "Muestra la configuración del servidor."),
		adminCommand("status", "Muestra el estado del bot en este servidor."),
	}
}

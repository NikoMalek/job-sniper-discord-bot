// Package bot implements Discord onboarding, admin commands and client wiring.
package bot

// EmbedColor is the blue used by the setup and help embeds.
const EmbedColor = 0x0A66C2

// EmbedField is one field of an embed shown to a user.
type EmbedField struct {
	Name   string
	Value  string
	Inline bool
}

// Embed is the Discord-independent representation of a message embed.
type Embed struct {
	Title       string
	Description string
	Color       int
	Fields      []EmbedField
	Footer      string
}

// BuildSetupEmbed returns the one-time guide shown when the bot joins a guild.
func BuildSetupEmbed() *Embed {
	return &Embed{
		Title:       "¡Gracias por invitar a Job Sniper! 🎯",
		Description: "Job Sniper monitorea ofertas de empleo en tiempo real y te avisa cuando encuentra una oportunidad.",
		Color:       EmbedColor,
		Fields: []EmbedField{
			{
				Name: "🚀 Primeros Pasos",
				Value: `1. Configura el canal de avisos: ` + "`/set-channel`" + `
2. Define tu ubicación: ` + "`/set-location`" + ` (por defecto: Worldwide)
3. Agrega términos para buscar: ` + "`/add-keyword`",
			},
			{
				Name: "📍 Ubicación",
				Value: `Usa uno de estos formatos:
• ` + "`Worldwide`" + ` — cualquier modalidad, sin filtro geográfico
• ` + "`Remoto`" + ` — solo ofertas remotas en todo el mundo
• ` + "`Remoto, país`" + ` — ej. ` + "`Remoto, Chile`" + `
• ` + "`País`" + ` — ej. ` + "`Chile`, `España`" + `
• ` + "`Ciudad, país`" + ` — ej. ` + "`Santiago, Chile`" + `

Puedes guardar hasta **3** ubicaciones con ` + "`/add-location`" + `.
Algunos portales solo cubren ciertos países: si filtras por una ubicación fuera de su cobertura, el portal queda en pausa y se reactiva solo cuando uses una ubicación compatible.`,
			},
			{
				Name: "💡 Configuración avanzada",
				Value: `Por defecto busca ofertas de las últimas **24 horas** en **LinkedIn** y acepta todos los idiomas.
Puedes activar otros portales con ` + "`/add-portal`" + ` (por ejemplo ` + "`chiletrabajos`" + ` o ` + "`getonbrd`" + `). Para ajustar la antigüedad, palabras o empresas ignoradas, idiomas y portales, usa ` + "`/help`.",
			},
			{
				Name:  "🔐 Permisos de comandos",
				Value: `Por defecto los comandos se requieren configurar. Puedes elegir quién puede usar cada uno en **Ajustes del servidor → Integraciones → Job Sniper**.`,
			},
		},
	}
}

// BuildHelpEmbed returns the response for the /help command.
func BuildHelpEmbed() *Embed {
	return &Embed{
		Title:       "Ayuda de Job Sniper",
		Description: "Job Sniper monitorea ofertas de empleo y envía avisos al canal que configures cuando encuentra coincidencias.",
		Color:       EmbedColor,
		Fields: []EmbedField{
			{
				Name: "🚀 Primeros Pasos",
				Value: "1. `/set-channel` — elige dónde se publicarán las alertas. No hay un canal predeterminado.\n" +
					"2. `/set-location` — define la ubicación con un formato estándar: `Worldwide`, `Remoto`, `Remoto, país`, `País` o `Ciudad, país`. " +
					"Por ejemplo `Worldwide`, `Chile`, `Santiago, Chile` o `Remoto, España`. Por defecto se busca en `Worldwide`. " +
					"Puedes sumar hasta 3 con `/add-location`.\n" +
					"3. `/add-keyword` — agrega los cargos o tecnologías que quieres encontrar, por ejemplo `python, backend`.",
			},
			{
				Name: "⚙️ Configuración avanzada",
				Value: "`/set-max-age` — limita la antigüedad de las ofertas. Por defecto busca las últimas 24 horas.\n" +
					"`/add-language` y `/remove-language` — filtra por idioma. Por defecto acepta todos los idiomas.\n" +
					"`/add-portal` y `/remove-portal` — activa o desactiva portales. LinkedIn está activo por defecto y puedes sumar ChileTrabajos o Get on Board. Cada portal tiene su propia cobertura: si todas tus ubicaciones quedan fuera, se pausa automáticamente.\n" +
					"`/add-blacklist-company` y `/remove-blacklist-company` — ignora o vuelve a permitir empresas.\n" +
					"`/add-blacklist-keyword` y `/remove-blacklist-keyword` — excluye o vuelve a permitir palabras en el título de la oferta (ej. `practicante, docente`), con opción de incluir alias comunes.",
			},
			{
				Name: "📋 Consultar configuración",
				Value: "Usa `/list-keywords`, `/list-languages`, `/list-portals` o `/list-locations` para revisar cada sección. " +
					"`/status` muestra un resumen general y confirma si el bot está listo.",
			},
			{
				Name: "🧪 Experimental Zone",
				Value: "Usa `/experimental` para ver la información y el estado de la Zona Experimental; con " +
					"`/experimental accion:activar` o `accion:desactivar` puedes cambiarlo.",
			},
		},
	}
}

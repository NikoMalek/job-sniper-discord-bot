package bot

import (
	"reflect"
	"testing"
)

func TestBuildSetupEmbed(t *testing.T) {
	got := BuildSetupEmbed()
	if got.Title != "¡Gracias por invitar a Job Sniper! 🎯" {
		t.Errorf("Title = %q", got.Title)
	}
	if got.Description != "Job Sniper monitorea ofertas de empleo en tiempo real y te avisa cuando encuentra una oportunidad." {
		t.Errorf("Description = %q", got.Description)
	}
	if got.Color != 0x0A66C2 {
		t.Errorf("Color = %#x", got.Color)
	}
	want := []EmbedField{
		{
			Name: "🚀 Primeros Pasos",
			Value: "1. Configura el canal de avisos: `/set-channel`\n" +
				"2. Define tu ubicación: `/set-location` (por defecto: Worldwide)\n" +
				"3. Agrega términos para buscar: `/add-keyword`",
		},
		{
			Name: "📍 Ubicación",
			Value: "Usa uno de estos formatos:\n" +
				"• `Worldwide` — cualquier modalidad, sin filtro geográfico\n" +
				"• `Remoto` — solo ofertas remotas en todo el mundo\n" +
				"• `Remoto, país` — ej. `Remoto, Chile`\n" +
				"• `País` — ej. `Chile`, `España`\n" +
				"• `Ciudad, país` — ej. `Santiago, Chile`\n\n" +
				"Puedes guardar hasta **3** ubicaciones con `/add-location`.\n" +
				"Algunos portales solo cubren ciertos países: si filtras por una ubicación fuera de su cobertura, " +
				"el portal queda en pausa y se reactiva solo cuando uses una ubicación compatible.",
		},
		{
			Name: "💡 Configuración avanzada",
			Value: "Por defecto busca ofertas de las últimas **24 horas** en **LinkedIn** y acepta todos los idiomas.\n" +
				"Puedes activar otros portales con `/add-portal` (por ejemplo `chiletrabajos` o `getonbrd`). " +
				"Para ajustar la antigüedad, palabras o empresas ignoradas, idiomas y portales, usa `/help`.",
		},
		{
			Name: "🔐 Permisos de comandos",
			Value: "Por defecto los comandos se requieren configurar. Puedes elegir quién puede usar cada uno en " +
				"**Ajustes del servidor → Integraciones → Job Sniper**.",
		},
	}
	if !reflect.DeepEqual(got.Fields, want) {
		t.Fatalf("setup fields mismatch:\n got: %#v\nwant: %#v", got.Fields, want)
	}
}

func TestBuildHelpEmbed(t *testing.T) {
	got := BuildHelpEmbed()
	if got.Title != "Ayuda de Job Sniper" {
		t.Errorf("Title = %q", got.Title)
	}
	if got.Description != "Job Sniper monitorea ofertas de empleo y envía avisos al canal que configures cuando encuentra coincidencias." {
		t.Errorf("Description = %q", got.Description)
	}
	want := []EmbedField{
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
				"`/add-portal` y `/remove-portal` — activa o desactiva portales. LinkedIn está activo por defecto y puedes sumar ChileTrabajos o Get on Board. " +
				"Cada portal tiene su propia cobertura: si todas tus ubicaciones quedan fuera, se pausa automáticamente.\n" +
				"`/add-blacklist-company` y `/remove-blacklist-company` — ignora o vuelve a permitir empresas.\n" +
				"`/add-blacklist-keyword` y `/remove-blacklist-keyword` — excluye o vuelve a permitir palabras en el título de la oferta " +
				"(ej. `practicante, docente`), con opción de incluir alias comunes.",
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
	}
	if !reflect.DeepEqual(got.Fields, want) {
		t.Fatalf("help fields mismatch:\n got: %#v\nwant: %#v", got.Fields, want)
	}
}

"""Language aliases and normalization shared by commands and filters."""

from __future__ import annotations

from collections.abc import Sequence

LANGUAGE_ALIASES: dict[str, str] = {
    "es": "spanish",
    "espanol": "spanish",
    "español": "spanish",
    "spanish": "spanish",
    "en": "english",
    "ingles": "english",
    "inglés": "english",
    "english": "english",
    "pt": "portuguese",
    "portugues": "portuguese",
    "portugués": "portuguese",
    "portuguese": "portuguese",
    "fr": "french",
    "frances": "french",
    "francés": "french",
    "french": "french",
    "de": "german",
    "aleman": "german",
    "alemán": "german",
    "german": "german",
}

SUPPORTED_LANGUAGES: frozenset[str] = frozenset(LANGUAGE_ALIASES.values())

#: Canonical language -> label shown in Discord. Insertion order is the display order.
LANGUAGE_LABELS: dict[str, str] = {
    "spanish": "español",
    "english": "inglés",
    "portuguese": "portugués",
    "french": "francés",
    "german": "alemán",
}


def normalize_language(value: str) -> str:
    """Return the canonical language name, or the cleaned value if unknown."""
    cleaned = " ".join(value.split()).strip().casefold()
    return LANGUAGE_ALIASES.get(cleaned, cleaned)


def parse_languages(raw: str) -> list[str]:
    """Parse a comma separated language list, keeping order and removing duplicates."""
    languages = [
        normalize_language(value) for value in raw.split(",") if value.strip()
    ]
    return list(dict.fromkeys(languages))


#: Order used when a guild has several languages configured. The bot's UI has
#: historically been Spanish, so that remains the fallback.
UI_LANGUAGES: tuple[str, ...] = (
    "spanish",
    "english",
    "portuguese",
    "french",
    "german",
)

_TRACKING_TEXTS: dict[str, dict[str, str]] = {
    "view_job": {
        "spanish": "🔗 Ver oferta",
        "english": "🔗 View Job",
        "portuguese": "🔗 Ver vaga",
        "french": "🔗 Voir l'offre",
        "german": "🔗 Stelle ansehen",
    },
    "open_link": {
        "spanish": "Abrir enlace de la oferta",
        "english": "Open Application Link",
        "portuguese": "Abrir link da vaga",
        "french": "Ouvrir le lien de l'offre",
        "german": "Link zur Stelle öffnen",
    },
    "applied_question": {
        "spanish": "¿Postulaste a **{title}** en **{company}**?",
        "english": "Did you apply to **{title}** at **{company}**?",
        "portuguese": "Você se candidatou a **{title}** na **{company}**?",
        "french": "Avez-vous postulé à **{title}** chez **{company}** ?",
        "german": "Hast du dich auf **{title}** bei **{company}** beworben?",
    },
    "yes_button": {
        "spanish": "✅ Sí, postulé",
        "english": "✅ Yes, I applied",
        "portuguese": "✅ Sim, me candidatei",
        "french": "✅ Oui, j'ai postulé",
        "german": "✅ Ja, beworben",
    },
    "no_button": {
        "spanish": "❌ No, no aplica",
        "english": "❌ No, not applicable",
        "portuguese": "❌ Não, não se aplica",
        "french": "❌ Non, sans objet",
        "german": "❌ Nein, nicht zutreffend",
    },
    "modal_title": {
        "spanish": "Registrar postulación",
        "english": "Log application",
        "portuguese": "Registrar candidatura",
        "french": "Enregistrer la candidature",
        "german": "Bewerbung erfassen",
    },
    "choice_placeholder": {
        "spanish": "¿Postulaste?",
        "english": "Did you apply?",
        "portuguese": "Você se candidatou?",
        "french": "Avez-vous postulé ?",
        "german": "Beworben?",
    },
    "observation_label": {
        "spanish": "Observación / debug (opcional)",
        "english": "Notes / debug (optional)",
        "portuguese": "Observação / debug (opcional)",
        "french": "Remarque / debug (facultatif)",
        "german": "Bemerkung / Debug (optional)",
    },
    "observation_placeholder": {
        "spanish": "Ej: El link redirige mal, el idioma no coincide, o cualquier comentario...",
        "english": "e.g., Broken link, wrong language, or any comment...",
        "portuguese": "Ex: O link redireciona mal, o idioma não corresponde, ou qualquer comentário...",
        "french": "Ex : Le lien redirige mal, la langue ne correspond pas, ou tout commentaire...",
        "german": "Z. B.: Link führt falsch weiter, Sprache passt nicht, oder beliebiger Kommentar...",
    },
    "saved_yes": {
        "spanish": "✅ Guardado: postulaste a **{title}**.",
        "english": "✅ Saved: you applied to **{title}**.",
        "portuguese": "✅ Registrado: você se candidatou a **{title}**.",
        "french": "✅ Enregistré : vous avez postulé à **{title}**.",
        "german": "✅ Gespeichert: Du hast dich auf **{title}** beworben.",
    },
    "saved_no": {
        "spanish": "❌ Guardado: no postulaste a **{title}**.",
        "english": "❌ Saved: you did not apply to **{title}**.",
        "portuguese": "❌ Registrado: você não se candidatou a **{title}**.",
        "french": "❌ Enregistré : vous n'avez pas postulé à **{title}**.",
        "german": "❌ Gespeichert: Du hast dich nicht auf **{title}** beworben.",
    },
    "expired": {
        "spanish": "Esa oferta ya no está disponible para registrar.",
        "english": "That offer is no longer available to track.",
        "portuguese": "Essa vaga não está mais disponível para registro.",
        "french": "Cette offre n'est plus disponible pour le suivi.",
        "german": "Diese Stelle kann nicht mehr erfasst werden.",
    },
    "disabled": {
        "spanish": "El modo experimental está desactivado en este servidor.",
        "english": "Experimental mode is disabled in this server.",
        "portuguese": "O modo experimental está desativado neste servidor.",
        "french": "Le mode expérimental est désactivé sur ce serveur.",
        "german": "Der experimentelle Modus ist auf diesem Server deaktiviert.",
    },
    "not_allowed": {
        "spanish": "Ese registro pertenece a otro usuario.",
        "english": "That record belongs to another user.",
        "portuguese": "Esse registro pertence a outro usuário.",
        "french": "Cet enregistrement appartient à un autre utilisateur.",
        "german": "Dieser Eintrag gehört zu einem anderen Benutzer.",
    },
    "experimental_title": {
        "spanish": "🧪 Zona Experimental",
        "english": "🧪 Experimental Zone",
        "portuguese": "🧪 Zona Experimental",
        "french": "🧪 Zone expérimentale",
        "german": "🧪 Experimentelle Zone",
    },
    "experimental_enabled": {
        "spanish": "Activada",
        "english": "Enabled",
        "portuguese": "Ativada",
        "french": "Activée",
        "german": "Aktiviert",
    },
    "experimental_disabled": {
        "spanish": "Desactivada",
        "english": "Disabled",
        "portuguese": "Desativada",
        "french": "Désactivée",
        "german": "Deaktiviert",
    },
    "experimental_desc": {
        "spanish": (
            "Los botones de ofertas abren un modal privado con el enlace, la "
            "confirmación Sí/No y un campo opcional de observación/debug. "
            "Al abrirlo se cuenta como visto; al enviarlo se marca Postulado "
            "u Omitido.\n"
            "`/experimental` activa o desactiva el modo.\n"
            "`/snipers` muestra el ranking, `/applications` (o "
            "`/applications @user`) el historial paginado con totales.\n"
            "`/debug` permite revisar las observaciones y comentarios dejados en "
            "las ofertas. Los permisos de estos comandos se configuran en "
            "Integraciones del servidor, como los de `/list-keywords`. Estos "
            "comandos solo existen mientras el modo está activado."
        ),
        "english": (
            "Offer buttons open a private modal with the link, a Yes/No "
            "confirmation and an optional notes/debug field. Opening it counts "
            "as viewed; submitting marks it as Applied or Skipped.\n"
            "`/experimental` enables or disables the mode.\n"
            "`/snipers` shows the ranking, `/applications` (or "
            "`/applications @user`) the paginated history with totals.\n"
            "`/debug` reviews the notes left on offers. The permissions for "
            "these commands are configured in server Integrations, like "
            "`/list-keywords`. These commands only exist while the mode is "
            "enabled."
        ),
        "portuguese": (
            "Os botões de vagas abrem um modal privado com o link, a confirmação "
            "Sim/Não e um campo opcional de observação/debug. Ao abrir conta "
            "como visto; ao enviar marca Candidatado ou Ignorado.\n"
            "`/experimental` ativa ou desativa o modo.\n"
            "`/snipers` mostra o ranking, `/applications` (ou "
            "`/applications @user`) o histórico paginado com totais.\n"
            "`/debug` permite revisar as observações deixadas nas vagas. As "
            "permissões destes comandos são configuradas em Integrações do "
            "servidor, como as de `/list-keywords`. Estes comandos só existem "
            "com o modo ativo."
        ),
        "french": (
            "Les boutons d'offres ouvrent un formulaire privé avec le lien, la "
            "confirmation Oui/Non et un champ facultatif de remarques/debug. "
            "L'ouverture compte comme vue ; l'envoi marque Candidaté ou Ignoré.\n"
            "`/experimental` active ou désactive le mode.\n"
            "`/snipers` affiche le classement, `/applications` (ou "
            "`/applications @user`) l'historique paginé avec les totaux.\n"
            "`/debug` permet de consulter les remarques laissées sur les offres. "
            "Les permissions de ces commandes se configurent dans les "
            "Intégrations du serveur, comme celles de `/list-keywords`. Ces "
            "commandes n'existent que lorsque le mode est actif."
        ),
        "german": (
            "Die Stellen-Buttons öffnen ein privates Formular mit Link, "
            "Ja/Nein-Bestätigung und einem optionalen Bemerkungen/Debug-Feld. "
            "Beim Öffnen zählt es als angesehen; beim Senden wird es als "
            "Beworben oder Übersprungen markiert.\n"
            "`/experimental` aktiviert oder deaktiviert den Modus.\n"
            "`/snipers` zeigt die Rangliste, `/applications` (oder "
            "`/applications @user`) den paginierten Verlauf mit Summen.\n"
            "`/debug` zeigt die zu Stellen hinterlegten Bemerkungen. Die Rechte "
            "dieser Befehle werden wie bei `/list-keywords` unter Integrationen "
            "konfiguriert. Diese Befehle existieren nur bei aktivem Modus."
        ),
    },
    "enable_button": {
        "spanish": "Activar",
        "english": "Enable",
        "portuguese": "Ativar",
        "french": "Activer",
        "german": "Aktivieren",
    },
    "disable_button": {
        "spanish": "Desactivar",
        "english": "Disable",
        "portuguese": "Desativar",
        "french": "Désactiver",
        "german": "Deaktivieren",
    },
    "snipers_title": {
        "spanish": "🎯 Ranking de Snipers",
        "english": "🎯 Snipers Ranking",
        "portuguese": "🎯 Ranking de Snipers",
        "french": "🎯 Classement des Snipers",
        "german": "🎯 Sniper-Rangliste",
    },
    "snipers_summary": {
        "spanish": (
            "Resumen del servidor: `{total_applied}` postulaciones enviadas · "
            "`{total_views}` ofertas revisadas"
        ),
        "english": (
            "Server summary: `{total_applied}` applications sent · "
            "`{total_views}` jobs viewed"
        ),
        "portuguese": (
            "Resumo do servidor: `{total_applied}` candidaturas enviadas · "
            "`{total_views}` vagas revisadas"
        ),
        "french": (
            "Résumé du serveur : `{total_applied}` candidatures envoyées · "
            "`{total_views}` offres consultées"
        ),
        "german": (
            "Serverübersicht: `{total_applied}` Bewerbungen gesendet · "
            "`{total_views}` Stellen angesehen"
        ),
    },
    "snipers_applications_one": {
        "spanish": "`{count}` postulación",
        "english": "`{count}` application",
        "portuguese": "`{count}` candidatura",
        "french": "`{count}` candidature",
        "german": "`{count}` Bewerbung",
    },
    "snipers_applications_many": {
        "spanish": "`{count}` postulaciones",
        "english": "`{count}` applications",
        "portuguese": "`{count}` candidaturas",
        "french": "`{count}` candidatures",
        "german": "`{count}` Bewerbungen",
    },
    "snipers_views_one": {
        "spanish": "`{count}` vista",
        "english": "`{count}` view",
        "portuguese": "`{count}` visualização",
        "french": "`{count}` consultation",
        "german": "`{count}` Aufruf",
    },
    "snipers_views_many": {
        "spanish": "`{count}` vistas",
        "english": "`{count}` views",
        "portuguese": "`{count}` visualizações",
        "french": "`{count}` consultations",
        "german": "`{count}` Aufrufe",
    },
    "no_data": {
        "spanish": "Aún no hay registros.",
        "english": "No records yet.",
        "portuguese": "Ainda não há registros.",
        "french": "Aucun enregistrement pour l'instant.",
        "german": "Noch keine Einträge.",
    },
    "status_label": {
        "spanish": "Estado",
        "english": "Status",
        "portuguese": "Status",
        "french": "Statut",
        "german": "Status",
    },
    "guild_id_label": {
        "spanish": "ID del servidor",
        "english": "Guild ID",
        "portuguese": "ID do servidor",
        "french": "ID du serveur",
        "german": "Server-ID",
    },
    "no_permission": {
        "spanish": "No tienes permiso para usar este comando.",
        "english": "You do not have permission to use this command.",
        "portuguese": "Você não tem permissão para usar este comando.",
        "french": "Vous n'avez pas la permission d'utiliser cette commande.",
        "german": "Du hast keine Berechtigung, diesen Befehl zu verwenden.",
    },
    "sync_fallback": {
        "spanish": "Estado guardado; los comandos se sincronizarán al reiniciar.",
        "english": "State saved; commands will sync after a restart.",
        "portuguese": "Estado salvo; os comandos serão sincronizados na reinicialização.",
        "french": "État enregistré ; les commandes seront synchronisées au redémarrage.",
        "german": "Status gespeichert; die Befehle werden nach einem Neustart synchronisiert.",
    },
    "command_error": {
        "spanish": "No se pudo completar el comando. Revisa los logs del bot.",
        "english": "The command could not be completed. Check the bot logs.",
        "portuguese": "Não foi possível concluir o comando. Verifique os logs do bot.",
        "french": "La commande n'a pas pu être terminée. Consultez les logs du bot.",
        "german": "Der Befehl konnte nicht abgeschlossen werden. Prüfe die Bot-Logs.",
    },
    "untitled": {
        "spanish": "Sin título",
        "english": "Untitled",
        "portuguese": "Sem título",
        "french": "Sans titre",
        "german": "Ohne Titel",
    },
    "unknown_company": {
        "spanish": "Sin empresa",
        "english": "Unknown company",
        "portuguese": "Empresa desconhecida",
        "french": "Entreprise inconnue",
        "german": "Unbekanntes Unternehmen",
    },
    "applications_title": {
        "spanish": " Historial de ofertas revisadas",
        "english": " Application history",
        "portuguese": " Histórico de vagas",
        "french": " Historique des offres",
        "german": " Verlauf der Stellen",
    },
    "applications_self_title": {
        "spanish": "Tus postulaciones",
        "english": "Your applications",
        "portuguese": "Suas candidaturas",
        "french": "Vos candidatures",
        "german": "Deine Bewerbungen",
    },
    "applications_other_title": {
        "spanish": "Postulaciones de {user}",
        "english": "{user}'s applications",
        "portuguese": "Candidaturas de {user}",
        "french": "Candidatures de {user}",
        "german": "Bewerbungen von {user}",
    },
    "applications_self_desc": {
        "spanish": (
            "Usa `/applications @usuario` para ver las de otro usuario."
        ),
        "english": (
            "Use `/applications @user` to see another user's."
        ),
        "portuguese": (
            "Use `/applications @usuário` para ver as de outro usuário."
        ),
        "french": (
            "Utilisez `/applications @utilisateur` pour voir celles d'un autre "
            "utilisateur."
        ),
        "german": (
            "Nutze `/applications @user`, um die eines anderen Nutzers zu sehen."
        ),
    },
    "applications_other_desc": {
        "spanish": (
            "Estás viendo las **postulaciones de {user}**.\n"
            "Usa `/applications` sin el `@` para ver las tuyas."
        ),
        "english": (
            "You are viewing **{user}'s applications**.\n"
            "Use `/applications` without the `@` to view your own."
        ),
        "portuguese": (
            "Você está vendo as **candidaturas de {user}**.\n"
            "Use `/applications` sem o `@` para ver as suas."
        ),
        "french": (
            "Vous consultez les **candidatures de {user}**.\n"
            "Utilisez `/applications` sans le `@` pour voir les vôtres."
        ),
        "german": (
            "Du siehst die **Bewerbungen von {user}**.\n"
            "Nutze `/applications` ohne das `@`, um deine eigenen zu sehen."
        ),
    },
    "totals_label": {
        "spanish": "Totales",
        "english": "Totals",
        "portuguese": "Totais",
        "french": "Totaux",
        "german": "Gesamt",
    },
    "totals_value": {
        "spanish": (
            "{clicked} vistas · {applied} postuladas · "
            "{skipped} omitidas · {viewed} sin responder"
        ),
        "english": (
            "{clicked} viewed · {applied} applied · "
            "{skipped} skipped · {viewed} unanswered"
        ),
        "portuguese": (
            "{clicked} visualizadas · {applied} candidatadas · "
            "{skipped} ignoradas · {viewed} sem resposta"
        ),
        "french": (
            "{clicked} consultées · {applied} postulées · "
            "{skipped} ignorées · {viewed} sans réponse"
        ),
        "german": (
            "{clicked} angesehen · {applied} beworben · "
            "{skipped} übersprungen · {viewed} unbeantwortet"
        ),
    },
    "page_footer": {
        "spanish": "Página {page}/{pages} · {total} registros",
        "english": "Page {page}/{pages} · {total} records",
        "portuguese": "Página {page}/{pages} · {total} registros",
        "french": "Page {page}/{pages} · {total} enregistrements",
        "german": "Seite {page}/{pages} · {total} Einträge",
    },
    "previous_button": {
        "spanish": "◀ Anterior",
        "english": "◀ Previous",
        "portuguese": "◀ Anterior",
        "french": "◀ Précédent",
        "german": "◀ Zurück",
    },
    "next_button": {
        "spanish": "Siguiente ▶",
        "english": "Next ▶",
        "portuguese": "Próxima ▶",
        "french": "Suivant ▶",
        "german": "Weiter ▶",
    },
    "debug_title": {
        "spanish": "🐞 Observaciones / debug",
        "english": "🐞 Notes / debug",
        "portuguese": "🐞 Observações / debug",
        "french": "🐞 Remarques / debug",
        "german": "🐞 Bemerkungen / Debug",
    },
    "debug_desc": {
        "spanish": "Reportes recientes dejados al registrar una postulación.",
        "english": "Recent reports left while logging an application.",
        "portuguese": "Relatórios recentes deixados ao registrar uma candidatura.",
        "french": "Signalements récents laissés lors de l'enregistrement d'une candidature.",
        "german": "Aktuelle Meldungen beim Erfassen einer Bewerbung.",
    },
    "debug_filter": {
        "spanish": "Filtro: **{source}**",
        "english": "Filter: **{source}**",
        "portuguese": "Filtro: **{source}**",
        "french": "Filtre : **{source}**",
        "german": "Filter: **{source}**",
    },
    "debug_empty": {
        "spanish": "Aún no hay observaciones registradas.",
        "english": "No notes have been logged yet.",
        "portuguese": "Ainda não há observações registradas.",
        "french": "Aucune remarque n'a encore été enregistrée.",
        "german": "Es sind noch keine Bemerkungen erfasst.",
    },
    "status_applied": {
        "spanish": "Postulado",
        "english": "Applied",
        "portuguese": "Candidatado",
        "french": "Postulé",
        "german": "Beworben",
    },
    "status_skipped": {
        "spanish": "Omitido",
        "english": "Skipped",
        "portuguese": "Ignorado",
        "french": "Ignoré",
        "german": "Übersprungen",
    },
    "status_viewed": {
        "spanish": "Sin responder",
        "english": "Unanswered",
        "portuguese": "Sem resposta",
        "french": "Sans réponse",
        "german": "Unbeantwortet",
    },
}


def preferred_ui_language(languages: Sequence[str] | None) -> str:
    """Pick the first configured language supported by the bot UI."""
    for value in languages or ():
        canonical = normalize_language(value)
        if canonical in UI_LANGUAGES:
            return canonical
    return "spanish"


def tracking_text(key: str, language: str = "spanish", **values: object) -> str:
    """Return a localized experimental-zone string."""
    translations = _TRACKING_TEXTS[key]
    template = translations.get(language) or translations["spanish"]
    return template.format(**values)

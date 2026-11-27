# Inbox-Benachrichtigungen und Tagesberichte

Multicas Inbox bleibt die kanonische Quelle. Beim Einfügen eines Inbox-Eintrags
legt ein DB-Trigger abhängig von den persönlichen Workspace-Präferenzen
Outbox-Einträge für `email` und `telegram` an. Die Outbox ist dadurch mit dem
Inbox-Schreibvorgang atomar und gilt für alle Workspaces des Servers; sie hängt
nicht von der Sichtbarkeit oder Version einer Plugin-Installation ab.

## Einstellungen und API

`GET/PATCH /api/notification-preferences` bleibt je angemeldetem Mitglied und
Workspace. Die bestehenden Inbox-Gruppen bleiben erhalten. Zusätzliche
String-Werte steuern Kanalzustellung und Bericht:

| Key | Werte | Bedeutung |
|---|---|---|
| `email_enabled` / `telegram_enabled` | `true` / `false` | Kanalzustellung |
| `email_address` | E-Mail-Adresse oder leer | Zieladresse; nur für den angemeldeten Empfänger lesbar |
| `digest_minutes` | `15`, `30`, `60`, `240` | Digest-Fenster; Standard `60` |
| `channel_event_*` | `true` / `false` | Zuweisung, Erwähnung, `@all`, Statusänderung, Agentenfehler/-abschluss |
| `daily_report_enabled` | `true` / `false` | Automatischer Tageszeitplan |
| `daily_report_time`, `daily_report_timezone` | `HH:mm`, IANA-Zeitzone | Lokaler Zeitplan |
| `daily_report_agent_id` | UUID oder leer | Aktiver Workspace-Agent |
| `daily_report_format` | `dev` / `standard` | Berichtsstil |
| `daily_report_instructions` | bis 4.000 Zeichen | Zusätzliche Agentenvorgaben |

Der frühere `PUT`-Endpunkt bleibt additiv, damit installierte Clients keine
neuen Präferenzen überschreiben. `POST /api/notification-preferences/daily-report`
startet bei gewähltem Agenten einen manuellen Bericht, auch wenn der tägliche
Zeitplan ausgeschaltet ist. Pro Mitglied, Workspace und lokalem Kalendertag
gibt es höchstens einen Berichtslauf.

## Versand

Der globale Scheduler beansprucht fällige Outbox-Zeilen mit
`FOR UPDATE SKIP LOCKED`. Normale Ereignisse werden auf die nächste gemeinsame
UTC-Grenze des gewählten Intervalls gelegt; Action-required-Ereignisse sind
sofort fällig. E-Mail gruppiert über Workspaces pro Empfänger, Zieladresse und
Zeitfenster zu einer Nachricht. Telegram bleibt pro Workspace an die vorhandene
Mitgliedsbindung gekoppelt und verwendet ausschließlich Multicas Channel-Registry.

E-Mail-Inhalt wird mit der abgeleiteten Careli-Vorlage `notify-email/email.html`
und den eingebetteten Pink-/White-Logos gerendert. Jedes Element zeigt
Workspace, Ereignistyp, Issue-Titel, Kontext, relevanten Kommentar-/Fehler-/Berichtstext
und einen Link. Beim Versand prüft der Worker erneut Mitgliedschaft, Kanalstatus,
Inbox-Lesestatus und die Ereignisfilter. Getrennte Outbox-Zeilen isolieren
Fehler zwischen Kanälen; nach fünf Versuchen endet ein Eintrag als `failed`.

SMTP-Annahme und Datenbank-Commit sind nicht atomar. Ein Prozessausfall genau
zwischen beiden Schritten kann die betroffene Digest-Nachricht nach dem
Lease-Ablauf erneut zustellen; SMTP bietet hier keine Exactly-once-Garantie.

## Tagesberichte

Der Scheduler erstellt einen Agenten-Issue-Auftrag im selben Workspace. Die
Beschreibung enthält zuerst den erfolgreichen Bericht des unmittelbar
vorherigen Kalendertags als Kontext. Ein älterer Bericht wird bei einer Lücke
nicht ersatzweise als Vortag verwendet; ein vorhandener Legacy-Bericht genau
dieses Tages wird vorab importiert. Gibt es keinen Bericht für den Vortag,
fordert der Auftrag einen Projektüberblick aus dem aktuellen Workspace- und
Issue-Stand an. Der Agent muss seinen fertigen Bericht mit
`<!-- notify-daily-report -->` beginnen. Danach speichert der Worker den Bericht
als `daily_report`-Inbox-Eintrag und verlinkt den Auftrags-Issue. Dieselbe
Outbox bündelt Bericht und Ereignisbenachrichtigungen.

Vor dem ersten Core-Lauf importiert Migration 564 alte Notify-Präferenzen aus
`plugin_storage` und erfolgreiche markierte Tagesberichte aus ihren Issues.
Die ursprünglichen `prefs.*`-Zeilen und SQLite-Historie bleiben für einen
Rollback erhalten. Alte Hook-Installationen werden vom Notify-Server mit
`LEGACY_NOTIFY_DELIVERY=false` signiert bestätigt, aber nicht mehr zugestellt.

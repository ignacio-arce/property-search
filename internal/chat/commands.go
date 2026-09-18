package chat

import "zonapropbot/internal/telegram"

// BotCommands is the catalog Telegram shows next to the message field. It mirrors
// helpText(): the user reads the menu, and a test keeps both in sync so a command
// cannot exist in one view and not the other.
func BotCommands() []telegram.BotCommand {
	return []telegram.BotCommand{
		{Command: "start", Description: "Darte de alta o reanudar las notificaciones"},
		{Command: "addurl", Description: "Sumar otra búsqueda"},
		{Command: "rmurl", Description: "Borrar una búsqueda"},
		{Command: "list", Description: "Ver tus búsquedas y su estado"},
		{Command: "model", Description: "Qué aprendió de tus calificaciones"},
		{Command: "stop", Description: "Pausar las notificaciones"},
		{Command: "buscar", Description: "Buscar publicaciones nuevas ahora"},
		{Command: "borrardatos", Description: "Borrar todos tus datos"},
		{Command: "help", Description: "Ver la ayuda"},
	}
}

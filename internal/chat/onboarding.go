package chat

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"zonapropbot/internal/repo"
	"zonapropbot/internal/searchurl"
)

// Onboarding states stored in users.state.
const (
	stateIdle       = "idle"
	stateAwaitURL   = "await_url"
	stateAwaitLabel = "await_label"
	stateReady      = "ready"
	stateStopped    = "stopped"
)

var userLocks sync.Map // userID -> *sync.Mutex

// lockUser serialises the handling of one user's messages. The poll loop and the
// scheduler run concurrently, so two messages arriving at once could otherwise
// interleave transitions and lose or duplicate a search.
func (p *Poller) lockUser(userID int64) func() {
	value, _ := userLocks.LoadOrStore(userID, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// startOnboarding resets the flow and asks for the first search URL.
func (p *Poller) startOnboarding(ctx context.Context, userID, chatID int64) error {
	defer p.lockUser(userID)()

	user, err := p.Repo.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	isNew := user == nil
	if isNew {
		if err := p.Repo.EnsureUser(ctx, userID, chatID); err != nil {
			return err
		}
	}
	if err := p.Repo.SetUserState(ctx, userID, stateAwaitURL, nil); err != nil {
		return err
	}
	p.Logger.Info("chat: /start", "user", userID, "new", isNew)

	return p.API.SendText(ctx, formatID(chatID), strings.Join([]string{
		"👋 Soy un buscador de propiedades que aprende de tus gustos.",
		"",
		"Mandame la *URL de búsqueda de Zonaprop* que querés que vigile.",
		"La armás en Zonaprop con los filtros que quieras y copiás la barra del navegador.",
		"",
		"Después te pido un nombre corto para reconocerla.",
	}, "\n"))
}

// handleConversationInput processes a non-command message according to the user's
// onboarding state. Unrecognised input gets an answer rather than silence.
func (p *Poller) handleConversationInput(ctx context.Context, userID, chatID int64, text string) error {
	defer p.lockUser(userID)()

	user, err := p.Repo.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	chat := formatID(chatID)

	if user == nil {
		return p.API.SendText(ctx, chat, "Todavía no arrancamos. Mandá /start y te guío.")
	}

	switch user.State {
	case stateAwaitLabel:
		return p.completeSearchWithLabel(ctx, userID, chat, user, text)
	case stateAwaitURL, stateIdle, stateReady, stateStopped, "":
		// A bare URL is the obvious intent; anything else gets guidance.
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(text)), "http") {
			return p.API.SendText(ctx, chat, "Mandame la URL de búsqueda de Zonaprop (empieza con https://).")
		}
		return p.beginSearchFromURL(ctx, userID, chat, text)
	default:
		return p.API.SendText(ctx, chat, "No entendí eso. Mandá /help para ver qué puedo hacer.")
	}
}

// beginSearchFromURL validates the URL syntactically, injects the recency order and
// asks for the label.
func (p *Poller) beginSearchFromURL(ctx context.Context, userID int64, chat, rawURL string) error {
	// Only https on Zonaprop itself: a substring check would let
	// "zonaprop.com.ar.attacker.test" through and the deep validation would then
	// load it in a real browser on the operator's network.
	if !searchurl.IsZonapropURL(rawURL) {
		return p.API.SendText(ctx, chat,
			"Esa URL no es de Zonaprop. Tiene que ser https://www.zonaprop.com.ar/...")
	}

	urlWithSort, changed, err := searchurl.InjectRecentSort(rawURL)
	if err != nil {
		return p.API.SendText(ctx, chat, "No pude leer esa URL. Probá copiarla de nuevo desde el navegador.")
	}

	notice := ""
	if changed {
		// Without the recency order the bot would see whatever order the portal
		// picks, and the newest listings could be missed.
		notice = "\n\n(Le agregué el orden por más recientes, que es lo que hace que vea lo nuevo primero.)"
	}

	if err := p.Repo.SetUserState(ctx, userID, stateAwaitLabel, map[string]string{"url": urlWithSort}); err != nil {
		return err
	}
	return p.API.SendText(ctx, chat,
		"Perfecto. ¿Cómo la llamo? Un nombre corto, por ejemplo `San Isidro 3amb`."+notice)
}

// completeSearchWithLabel registers the search and closes the onboarding.
func (p *Poller) completeSearchWithLabel(ctx context.Context, userID int64, chat string, user *repo.UserState, label string) error {
	rawURL := user.StateData["url"]
	if rawURL == "" {
		// The scratch data was lost; restart rather than register a broken search.
		_ = p.Repo.SetUserState(ctx, userID, stateAwaitURL, nil)
		return p.API.SendText(ctx, chat, "Se me perdió la URL. Mandala de nuevo, por favor.")
	}

	label = strings.TrimSpace(label)
	if label == "" || strings.ContainsAny(label, "|,") {
		return p.API.SendText(ctx, chat, "Ese nombre no me sirve. Usá uno corto sin `|` ni comas.")
	}
	if len([]rune(label)) > 40 {
		return p.API.SendText(ctx, chat, "Muy largo. Probá con algo de hasta 40 caracteres.")
	}

	if _, err := p.Repo.AddSearchURLChecked(ctx, userID, label, rawURL); err != nil {
		switch {
		case errors.Is(err, repo.ErrLimitReached):
			_ = p.Repo.SetUserState(ctx, userID, stateReady, nil)
			return p.API.SendText(ctx, chat, fmt.Sprintf(
				"Ya tenés %d búsquedas, que es mi máximo. Borrá una con /rmurl y volvé a intentar.",
				repo.MaxSearchURLsPerUser))
		case errors.Is(err, repo.ErrLabelTaken):
			// Stay in await_label so the user can just pick another name.
			return p.API.SendText(ctx, chat, "Ya usaste ese nombre. Elegí otro.")
		default:
			return err
		}
	}
	if err := p.Repo.SetUserState(ctx, userID, stateReady, nil); err != nil {
		return err
	}
	p.Logger.Info("chat: search added", "user", userID, "label", label)

	return p.API.SendText(ctx, chat, p.afterRegistrationText(ctx, userID, label))
}

// afterRegistrationText states the two things the user must know: that the portal
// only exposes the first page, and that notifications wait for the operator.
func (p *Poller) afterRegistrationText(ctx context.Context, userID int64, label string) string {
	searches, err := p.Repo.ListSearchURLs(ctx, userID)
	more := ""
	if err == nil && len(searches) < repo.MaxSearchURLsPerUser {
		more = "\nPodés agregar más con /addurl (hasta " +
			strconv.Itoa(repo.MaxSearchURLsPerUser) + ")."
	}
	return fmt.Sprintf(
		"✅ Guardé `%s`. Estoy validando que la búsqueda responda; te aviso cuando esté.\n\n"+
			"Tené en cuenta que Zonaprop solo me muestra la *primera página* de resultados: "+
			"vigilo las ~30 publicaciones más nuevas, que con el orden por fecha son las que importan.\n\n"+
			"⚠️ Las notificaciones diarias no empiezan hasta que te habilite. Cuando lo haga, te llegan solas."+
			more, label)
}

// listSearches renders /list.
func (p *Poller) listSearches(ctx context.Context, userID int64, chat string) error {
	searches, err := p.Repo.ListSearchURLs(ctx, userID)
	if err != nil {
		return err
	}
	if len(searches) == 0 {
		return p.API.SendText(ctx, chat, "No tenés búsquedas todavía. Mandá /start para agregar una.")
	}

	var b strings.Builder
	b.WriteString("📋 Tus búsquedas:\n")
	for _, s := range searches {
		fmt.Fprintf(&b, "\n· `%s` — %s\n  %s\n", s.Label, validationLabel(s.ValidationStatus), s.URL)
	}
	return p.API.SendText(ctx, chat, b.String())
}

// validationLabel translates the stored status for a human, without pretending a
// pending check is a problem.
func validationLabel(status string) string {
	switch status {
	case "valid":
		return "vigilando"
	case "valid_empty":
		return "vigilando (hoy sin resultados)"
	case "pending", "retrying":
		return "validando…"
	case "invalid", "failed":
		return "no pude validarla"
	default:
		return status
	}
}

// addURL starts the flow again without losing the existing searches.
func (p *Poller) addURL(ctx context.Context, userID, chatID int64, chat string) error {
	defer p.lockUser(userID)()

	// A user may go straight to /addurl without /start; without this the state
	// update would target a row that does not exist.
	if err := p.Repo.EnsureUser(ctx, userID, chatID); err != nil {
		return err
	}
	searches, err := p.Repo.ListSearchURLs(ctx, userID)
	if err != nil {
		return err
	}
	if len(searches) >= repo.MaxSearchURLsPerUser {
		return p.API.SendText(ctx, chat, fmt.Sprintf(
			"Ya tenés %d búsquedas, que es mi máximo. Borrá una con /rmurl primero.",
			repo.MaxSearchURLsPerUser))
	}
	if err := p.Repo.SetUserState(ctx, userID, stateAwaitURL, nil); err != nil {
		return err
	}
	return p.API.SendText(ctx, chat, "Mandame la URL de la nueva búsqueda.")
}

// removeURL deletes a search by label. Labels are unique per user, so the user
// never has to guess an index.
func (p *Poller) removeURL(ctx context.Context, userID int64, chat, label string) error {
	defer p.lockUser(userID)()

	label = strings.TrimSpace(label)
	if label == "" {
		return p.API.SendText(ctx, chat, "Decime cuál: /rmurl <nombre>. Podés ver los nombres con /list.")
	}
	removed, err := p.Repo.RemoveSearchURL(ctx, userID, label)
	if err != nil {
		return err
	}
	if !removed {
		return p.API.SendText(ctx, chat, fmt.Sprintf("No encontré una búsqueda llamada `%s`. Mirá /list.", label))
	}
	p.Logger.Info("chat: search removed", "user", userID, "label", label)
	return p.API.SendText(ctx, chat, fmt.Sprintf("Listo, borré `%s`.", label))
}

// setStopped pauses or resumes the user's own delivery.
func (p *Poller) setStopped(ctx context.Context, userID int64, chat string, stopped bool) error {
	defer p.lockUser(userID)()

	if err := p.Repo.SetUserStopped(ctx, userID, stopped); err != nil {
		return err
	}
	p.Logger.Info("chat: notifications stopped", "user", userID, "stopped", stopped)
	if stopped {
		return p.API.SendText(ctx, chat, "Listo, pausé las notificaciones. Tus datos y calificaciones quedan guardados; /start reanuda.")
	}
	return p.API.SendText(ctx, chat, "Reanudé las notificaciones.")
}

// deleteEverything removes the user's data through the cascade.
func (p *Poller) deleteEverything(ctx context.Context, userID int64, chat string) error {
	defer p.lockUser(userID)()

	if err := p.Repo.DeleteUser(ctx, userID); err != nil {
		return err
	}
	p.Logger.Info("chat: user data deleted", "user", userID)
	return p.API.SendText(ctx, chat, "Borré tus búsquedas, entregas y calificaciones. Si querés empezar de nuevo, mandá /start.")
}

func formatID(v int64) string { return strconv.FormatInt(v, 10) }

package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/statuspage"
	"github.com/bernardojoao/upwatch/internal/store"
)

// A superfície sem credencial.
//
// Estes são os únicos handlers que respondem a quem não provou ser
// ninguém, e por isso não montam a resposta: delegam ao pacote
// statuspage, que é o lugar único onde se decide o que sai. Um handler
// que montasse "quase igual" seria o caminho mais provável para um campo
// interno escapar.

// publicMaxAge é quanto a resposta pública pode ser reaproveitada.
//
// Meio minuto. É a única rota do UpWatch que pode receber tráfego de
// verdade — o link circula em chat e em e-mail durante uma queda,
// exatamente quando o servidor já está sob pressão. Meio minuto corta a
// avalanche sem que a página pareça congelada.
const publicMaxAge = 30 * time.Second

func (a *API) handlePublicStatus(w http.ResponseWriter, r *http.Request) {
	view, ok := a.publicView(w, r)
	if !ok {
		return
	}

	corpo, err := json.Marshal(view)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(publicMaxAge.Seconds())))
	if serveCached(w, r, corpo) {
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(corpo)
}

// publicView resolve a página ou responde o erro.
//
// Página inexistente e página desligada dão o mesmo 404: distinguir as
// duas confirmaria a existência da página a quem só chutou o endereço.
func (a *API) publicView(w http.ResponseWriter, r *http.Request) (domain.PublicView, bool) {
	slug := chi.URLParam(r, "slug")
	if err := domain.ValidateSlug(slug); err != nil {
		// Slug inválido não chega ao banco: devolver 404 direto evita
		// transformar a rota pública num caminho de sondagem barato.
		writeError(w, http.StatusNotFound, codeNotFound, "página não encontrada")
		return domain.PublicView{}, false
	}

	view, err := statuspage.NewBuilder(a.store, a.clock).Build(r.Context(), slug)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "página não encontrada")
			return domain.PublicView{}, false
		}
		writeStoreError(w, err)
		return domain.PublicView{}, false
	}
	return view, true
}

// serveCached responde 304 quando o cliente já tem esta versão.
//
// O ETag é o hash do corpo. Durante uma queda a mesma página é recarregada
// muitas vezes por muita gente, e quase sempre nada mudou entre duas
// visitas.
func serveCached(w http.ResponseWriter, r *http.Request, corpo []byte) bool {
	soma := sha256.Sum256(corpo)
	etag := `"` + hex.EncodeToString(soma[:16]) + `"`
	w.Header().Set("ETag", etag)

	if match := r.Header.Get("If-None-Match"); match == etag {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}

// ---------- feed ----------

// handlePublicFeed publica os relatos em Atom.
//
// Existe porque acompanhar uma página de estado sem cadastrar e-mail é o
// caminho que a maioria prefere, e é o que as páginas de referência
// oferecem. Também é o que permite integrar num canal de chat sem que o
// UpWatch precise saber falar com aquele canal.
func (a *API) handlePublicFeed(w http.ResponseWriter, r *http.Request) {
	view, ok := a.publicView(w, r)
	if !ok {
		return
	}

	feed := buildFeed(view, publicBaseURL(r), view.Slug)

	corpo, err := xml.MarshalIndent(feed, "", "  ")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	corpo = append([]byte(xml.Header), corpo...)

	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(publicMaxAge.Seconds())))
	if serveCached(w, r, corpo) {
		return
	}

	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(corpo)
}

// atomFeed é o documento Atom.
type atomFeed struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	Title   string      `xml:"title"`
	ID      string      `xml:"id"`
	Updated string      `xml:"updated"`
	Link    []atomLink  `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
}

type atomEntry struct {
	Title   string `xml:"title"`
	ID      string `xml:"id"`
	Updated string `xml:"updated"`
	Summary string `xml:"summary"`
	Content string `xml:"content"`
}

// buildFeed converte a visão pública em Atom.
//
// Recebe a visão já montada, e não o store: assim o feed não tem como
// mostrar nada que a página não mostre, e o teste de vazamento cobre os
// dois de uma vez.
func buildFeed(view domain.PublicView, base, slug string) atomFeed {
	feed := atomFeed{
		Title:   view.Title,
		ID:      base + "/status/" + slug,
		Updated: view.GeneratedAt.UTC().Format(time.RFC3339),
		Link: []atomLink{
			{Rel: "alternate", Href: base + "/status/" + slug},
			{Rel: "self", Href: base + "/api/v1/public/" + slug + "/feed.atom"},
		},
	}

	for i, a := range view.Announcements {
		atualizado := a.StartedAt
		resumo := a.Title
		if n := len(a.Updates); n > 0 {
			// A entrada carrega a última atualização, que é o que alguém
			// acompanhando quer ver primeiro.
			atualizado = a.Updates[n-1].PublishedAt
			resumo = a.Updates[n-1].Body
		}

		feed.Entries = append(feed.Entries, atomEntry{
			Title: a.Title,
			// Identificador estável por relato, para o leitor não repetir a
			// mesma notícia a cada consulta.
			ID:      fmt.Sprintf("%s/status/%s#%d-%d", base, slug, a.StartedAt.UTC().Unix(), i),
			Updated: atualizado.UTC().Format(time.RFC3339),
			Summary: resumo,
			Content: feedContent(a),
		})
	}
	return feed
}

// feedContent monta a linha do tempo em texto.
func feedContent(a domain.PublicAnnouncement) string {
	if len(a.Updates) == 0 {
		return a.Title
	}

	texto := ""
	for _, u := range a.Updates {
		if texto != "" {
			texto += "\n\n"
		}
		texto += fmt.Sprintf("%s — %s: %s",
			u.PublishedAt.UTC().Format(time.RFC3339), phaseLabel(u.Phase), u.Body)
	}
	return texto
}

// phaseLabel traduz a fase para quem lê o feed.
func phaseLabel(p domain.IncidentPhase) string {
	switch p {
	case domain.PhaseInvestigating:
		return "Investigando"
	case domain.PhaseIdentified:
		return "Identificado"
	case domain.PhaseMonitoring:
		return "Monitorando"
	case domain.PhaseResolved:
		return "Resolvido"
	default:
		return "Atualização"
	}
}

// publicBaseURL reconstrói o endereço externo da instalação.
//
// Sem confiar em cabeçalho de proxy para o host: X-Forwarded-Host é
// controlado por quem chama quando não há proxy na frente, e usá-lo
// deixaria qualquer pessoa injetar o próprio domínio nos identificadores
// do feed.
func publicBaseURL(r *http.Request) string {
	esquema := "http"
	if r.TLS != nil {
		esquema = "https"
	}
	return esquema + "://" + r.Host
}

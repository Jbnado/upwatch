package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"strings"
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
	view, ok := a.publicView(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return
	}

	a.writePublicJSON(w, r, view)
}

// writePublicJSON serializa a visão com cache e revalidação.
func (a *API) writePublicJSON(w http.ResponseWriter, r *http.Request, view domain.PublicView) {
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

// handlePublicDefault responde em "/status", sem slug.
//
// Existe porque numa instalação com uma página só o slug repete o que o
// caminho já diz: "/status/estado" é redundante, e é o endereço que se
// cola em contrato e em rodapé de e-mail.
func (a *API) handlePublicDefault(w http.ResponseWriter, r *http.Request) {
	view, ok := a.publicView(w, r, "")
	if !ok {
		return
	}
	a.writePublicJSON(w, r, view)
}

func (a *API) handlePublicDefaultFeed(w http.ResponseWriter, r *http.Request) {
	view, ok := a.publicView(w, r, "")
	if !ok {
		return
	}
	a.writeFeed(w, r, view)
}

// publicView resolve a página ou responde o erro.
//
// Slug vazio busca a padrão. Página inexistente, página desligada e
// instalação sem padrão dão todas o mesmo 404: distinguir os casos
// confirmaria a existência da página a quem só chutou o endereço.
func (a *API) publicView(w http.ResponseWriter, r *http.Request, slug string) (domain.PublicView, bool) {
	if slug == "" {
		view, err := statuspage.NewBuilder(a.store, a.clock).BuildDefault(r.Context())
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
	view, ok := a.publicView(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return
	}

	a.writeFeed(w, r, view)
}

// writeFeed serializa o Atom com cache e revalidação.
func (a *API) writeFeed(w http.ResponseWriter, r *http.Request, view domain.PublicView) {
	feed := buildFeed(view, a.publicURL, view.Slug)

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
//
// base vazio produz endereços relativos, que o Atom aceita e o leitor
// resolve contra a URL do próprio documento. É o padrão de propósito —
// ver publicHref.
func buildFeed(view domain.PublicView, base, slug string) atomFeed {
	feed := atomFeed{
		Title: view.Title,
		// URN, não URL. O identificador de um feed precisa ser estável
		// para sempre: derivá-lo do endereço faria todo leitor tratar as
		// notícias como novas no dia em que a instalação mudasse de
		// domínio — e, pior, faria o identificador depender de um
		// cabeçalho que quem chama controla.
		ID:      "urn:upwatch:status:" + slug,
		Updated: view.GeneratedAt.UTC().Format(time.RFC3339),
		Link: []atomLink{
			{Rel: "alternate", Href: publicHref(base, "/status/"+slug)},
			{Rel: "self", Href: publicHref(base, "/api/v1/public/"+slug+"/feed.atom")},
		},
	}

	for _, a := range view.Announcements {
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
			// Estável por relato, pelo instante em que começou: o leitor
			// não repete a mesma notícia a cada consulta.
			ID:      fmt.Sprintf("urn:upwatch:status:%s:%d", slug, a.StartedAt.UTC().Unix()),
			Updated: atualizado.UTC().Format(time.RFC3339),
			Summary: resumo,
			Content: feedContent(a),
		})
	}
	return feed
}

// publicHref monta um endereço do feed.
//
// Sem base configurada devolve o caminho relativo. É a escolha segura: a
// alternativa seria reconstruir o endereço a partir do cabeçalho Host, e
// esse cabeçalho é controlado por quem chama. Como a resposta pública
// carrega Cache-Control público, um cache compartilhado guardaria a
// versão envenenada e a serviria a quem viesse depois, com links
// apontando para o domínio do atacante num documento que parece nosso.
//
// Quem estiver atrás de proxy e quiser endereços absolutos configura
// UPWATCH_PUBLIC_URL.
func publicHref(base, caminho string) string {
	if base == "" {
		return caminho
	}
	return strings.TrimSuffix(base, "/") + caminho
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

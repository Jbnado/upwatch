package api_test

import (
	"net/http"
	"strings"
	"testing"
)

// A superfície sem credencial, pela porta da frente.
//
// Os testes do pacote statuspage já cobrem a montagem. Aqui o que se
// verifica é o contrato HTTP: quem responde sem sessão, o que devolve
// para uma página que não existe, e se o corpo que sai pela rede — não o
// struct em memória — continua sem nada interno.

// publicar cria página, monitor e vínculo, devolvendo o id do monitor.
func (s *server) publicar(t *testing.T, slug, nomeInterno, alvo, rotulo string) int64 {
	t.Helper()

	resp := s.do(t, http.MethodPost, "/api/v1/monitors", map[string]any{
		"name": nomeInterno, "type": "http", "target": alvo,
		"interval_seconds": 60, "timeout_seconds": 10,
	})
	assertStatus(t, resp, http.StatusCreated)

	monitorID := decode[idResp](t, resp).ID

	resp = s.do(t, http.MethodPost, "/api/v1/status-pages", map[string]any{
		"slug": slug, "title": "Estado da plataforma",
	})
	assertStatus(t, resp, http.StatusCreated)

	paginaID := decode[idResp](t, resp).ID

	resp = s.do(t, http.MethodPut,
		"/api/v1/status-pages/"+itoa(paginaID)+"/components/"+itoa(monitorID),
		map[string]any{"label": rotulo})
	assertStatus(t, resp, http.StatusOK)

	return monitorID
}

func TestPublicStatusNeedsNoCredential(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.publicar(t, "estado", "api", "https://exemplo.com", "API")

	// Sem sessão: é o ponto inteiro da página.
	s.cookie = nil

	resp := s.do(t, http.MethodGet, "/api/v1/public/estado", nil)

	assertStatus(t, resp, http.StatusOK)
	if !strings.Contains(readBody(t, resp), "API") {
		t.Error("página pública não trouxe o componente")
	}
}

func TestPublicStatusDoesNotLeakInternals(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.publicar(t, "estado", "api-prod-us-east-1", "http://banco-interno.vpc.local:5432/health", "API")
	s.cookie = nil

	resp := s.do(t, http.MethodGet, "/api/v1/public/estado", nil)
	assertStatus(t, resp, http.StatusOK)
	corpo := readBody(t, resp)

	// O corpo que sai pela rede, não o struct em memória: um handler que
	// acrescentasse um campo depois da montagem escaparia do teste do
	// pacote statuspage, mas não deste.
	for _, agulha := range []string{
		"banco-interno.vpc.local", "5432", "api-prod-us-east-1", "target", "monitor_id",
	} {
		if strings.Contains(corpo, agulha) {
			t.Errorf("resposta pública revelou %q:\n%s", agulha, corpo)
		}
	}
}

func TestPublicStatusOfUnknownPageIs404(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.cookie = nil

	resp := s.do(t, http.MethodGet, "/api/v1/public/nao-existe", nil)

	assertStatus(t, resp, http.StatusNotFound)
}

func TestPublicStatusOfDisabledPageIs404(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.publicar(t, "estado", "api", "https://exemplo.com", "API")

	resp := s.do(t, http.MethodGet, "/api/v1/status-pages", nil)
	assertStatus(t, resp, http.StatusOK)
	lista := decode[struct {
		Items []idResp `json:"items"`
	}](t, resp)

	resp = s.do(t, http.MethodPut, "/api/v1/status-pages/"+itoa(lista.Items[0].ID), map[string]any{
		"slug": "estado", "title": "Estado da plataforma", "enabled": false,
	})
	assertStatus(t, resp, http.StatusOK)

	s.cookie = nil
	resp = s.do(t, http.MethodGet, "/api/v1/public/estado", nil)

	// 404 e não 403: "existe, mas está desligada" confirmaria a existência
	// da página a quem só chutou o endereço.
	assertStatus(t, resp, http.StatusNotFound)
}

func TestPublicStatusRejectsDangerousSlug(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.cookie = nil

	// Não chega ao banco: a rota pública não deve virar caminho barato de
	// sondagem.
	resp := s.do(t, http.MethodGet, "/api/v1/public/ESTADO", nil)

	assertStatus(t, resp, http.StatusNotFound)
}

func TestPublicStatusIsCacheable(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.publicar(t, "estado", "api", "https://exemplo.com", "API")
	s.cookie = nil

	resp := s.do(t, http.MethodGet, "/api/v1/public/estado", nil)
	assertStatus(t, resp, http.StatusOK)

	// O link circula em chat durante uma queda, exatamente quando o
	// servidor já está sob pressão.
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "max-age=") {
		t.Errorf("resposta pública sem Cache-Control: %q", cc)
	}

	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("resposta pública sem ETag")
	}

	repetida := s.withHeader(t, http.MethodGet, "/api/v1/public/estado", "If-None-Match", etag)
	if repetida.StatusCode != http.StatusNotModified {
		t.Fatalf("revisita devolveu %d, esperava 304", repetida.StatusCode)
	}
}

func TestPublicFeedCarriesTheTimeline(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	monitorID := s.publicar(t, "estado", "api", "https://exemplo.com", "API")

	resp := s.do(t, http.MethodPost, "/api/v1/announcements", map[string]any{
		"title": "Lentidão na API", "impact": "minor", "phase": "investigating",
		"components": []int64{monitorID}, "body": "Estamos investigando erros elevados.",
	})
	assertStatus(t, resp, http.StatusCreated)

	s.cookie = nil
	resp = s.do(t, http.MethodGet, "/api/v1/public/estado/feed.atom", nil)
	assertStatus(t, resp, http.StatusOK)

	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "atom+xml") {
		t.Errorf("feed com tipo errado: %q", ct)
	}

	corpo := readBody(t, resp)
	for _, esperado := range []string{"Lentidão na API", "Estamos investigando erros elevados.", "<entry>"} {
		if !strings.Contains(corpo, esperado) {
			t.Errorf("feed não trouxe %q:\n%s", esperado, corpo)
		}
	}
}

func TestPublicFeedDoesNotLeakInternals(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	monitorID := s.publicar(t, "estado", "api-prod-us-east-1", "http://banco-interno.vpc.local:5432/health", "API")

	resp := s.do(t, http.MethodPost, "/api/v1/announcements", map[string]any{
		"title": "Falha", "impact": "major", "phase": "investigating",
		"components": []int64{monitorID}, "body": "Investigando.",
	})
	assertStatus(t, resp, http.StatusCreated)

	s.cookie = nil
	resp = s.do(t, http.MethodGet, "/api/v1/public/estado/feed.atom", nil)
	corpo := readBody(t, resp)

	for _, agulha := range []string{"banco-interno.vpc.local", "api-prod-us-east-1", "5432"} {
		if strings.Contains(corpo, agulha) {
			t.Errorf("feed revelou %q:\n%s", agulha, corpo)
		}
	}
}

func TestAnnouncementOfAnotherPageStaysHidden(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.publicar(t, "cliente-a", "alvo-do-a", "https://exemplo.com/a", "Serviço")

	// Um alvo que não está em página nenhuma, com um relato próprio.
	resp := s.do(t, http.MethodPost, "/api/v1/monitors", map[string]any{
		"name": "alvo-do-b", "type": "http", "target": "https://exemplo.com/b",
		"interval_seconds": 60, "timeout_seconds": 10,
	})
	assertStatus(t, resp, http.StatusCreated)
	outroID := decode[idResp](t, resp).ID

	resp = s.do(t, http.MethodPost, "/api/v1/announcements", map[string]any{
		"title": "Queda no ambiente do cliente B", "impact": "critical",
		"phase": "investigating", "components": []int64{outroID},
	})
	assertStatus(t, resp, http.StatusCreated)

	s.cookie = nil
	resp = s.do(t, http.MethodGet, "/api/v1/public/cliente-a", nil)
	corpo := readBody(t, resp)

	// O pior vazamento numa página pública é informação sobre terceiro.
	if strings.Contains(corpo, "cliente B") {
		t.Errorf("relato de outro cliente apareceu:\n%s", corpo)
	}
}

// ---------- administração ----------

func TestStatusPageAdminNeedsCredential(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.cookie = nil

	for _, caso := range []struct {
		metodo, rota string
	}{
		{http.MethodGet, "/api/v1/status-pages"},
		{http.MethodPost, "/api/v1/status-pages"},
		{http.MethodGet, "/api/v1/announcements"},
		{http.MethodPost, "/api/v1/announcements"},
	} {
		t.Run(caso.metodo+" "+caso.rota, func(t *testing.T) {
			resp := s.do(t, caso.metodo, caso.rota, map[string]any{})
			assertStatus(t, resp, http.StatusUnauthorized)
		})
	}
}

func TestStatusPageRejectsDangerousSlug(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/status-pages", map[string]any{
		"slug": "estado/../interno", "title": "Estado",
	})

	assertStatus(t, resp, http.StatusBadRequest)
	if !strings.Contains(readBody(t, resp), `"field":"slug"`) {
		t.Error("erro não apontou o campo slug")
	}
}

func TestStatusPageRejectsDuplicateSlug(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	corpo := map[string]any{"slug": "estado", "title": "Estado"}
	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/status-pages", corpo), http.StatusCreated)

	resp := s.do(t, http.MethodPost, "/api/v1/status-pages", corpo)

	assertStatus(t, resp, http.StatusConflict)
}

func TestPublishingUpdateAdvancesThePhase(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/announcements", map[string]any{
		"title": "Falha", "impact": "major", "phase": "investigating", "global": true,
	})
	assertStatus(t, resp, http.StatusCreated)
	criadoID := decode[idResp](t, resp).ID

	resp = s.do(t, http.MethodPost, "/api/v1/announcements/"+itoa(criadoID)+"/updates",
		map[string]any{"phase": "resolved", "body": "Normalizado."})
	assertStatus(t, resp, http.StatusCreated)

	resp = s.do(t, http.MethodGet, "/api/v1/announcements/"+itoa(criadoID), nil)
	assertStatus(t, resp, http.StatusOK)

	lido := decode[struct {
		Announcement struct {
			Phase      string  `json:"phase"`
			ResolvedAt *string `json:"resolved_at"`
		} `json:"announcement"`
		Updates []struct {
			Body string `json:"body"`
		} `json:"updates"`
	}](t, resp)

	// A fase acompanha a entrada publicada. Mantê-las separadas produziria
	// uma página dizendo "investigando" sob um texto que diz "resolvido".
	if lido.Announcement.Phase != "resolved" {
		t.Errorf("fase não acompanhou a atualização: %q", lido.Announcement.Phase)
	}
	if lido.Announcement.ResolvedAt == nil {
		t.Error("relato resolvido ficou sem carimbo de encerramento")
	}
	if len(lido.Updates) != 1 || lido.Updates[0].Body != "Normalizado." {
		t.Errorf("linha do tempo divergiu: %+v", lido.Updates)
	}
}

func TestAnnouncementWithoutScopeIsRejected(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	// Sem componente e sem alcance global, o relato não apareceria em
	// página nenhuma — e quem escreveu não descobriria sozinho.
	resp := s.do(t, http.MethodPost, "/api/v1/announcements", map[string]any{
		"title": "Falha", "impact": "major", "phase": "investigating",
	})

	assertStatus(t, resp, http.StatusBadRequest)
	if !strings.Contains(readBody(t, resp), `"field":"components"`) {
		t.Error("erro não apontou o campo components")
	}
}

func TestComponentOfUnknownMonitorIsRejected(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/status-pages", map[string]any{
		"slug": "estado", "title": "Estado",
	})
	assertStatus(t, resp, http.StatusCreated)
	paginaID := decode[idResp](t, resp).ID

	// Publicar um id inexistente criaria um componente que a página
	// pública não conseguiria montar.
	resp = s.do(t, http.MethodPut,
		"/api/v1/status-pages/"+itoa(paginaID)+"/components/9999", map[string]any{})

	assertStatus(t, resp, http.StatusNotFound)
}

// ---------- utilidades ----------

// idResp cobre as respostas de criação, que só precisam do id.
type idResp struct {
	ID int64 `json:"id"`
}

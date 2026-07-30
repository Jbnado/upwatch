package api_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Tentativas de invasão pela porta aberta.
//
// A página pública é a única superfície que responde sem credencial, e é
// por onde alguém tentaria entrar. Cada teste aqui é um ataque concreto,
// escrito do ponto de vista de quem só tem o endereço e quer conseguir
// mais do que deveria.

// atacar faz uma requisição crua, sem sessão e sem normalizar o caminho.
//
// O cliente do Go normaliza "/a/../b" antes de enviar, o que esconderia
// justamente o que se quer testar. Aqui o caminho vai como está.
func (s *server) atacar(t *testing.T, caminho string) *http.Response {
	t.Helper()

	alvo, err := url.Parse(s.URL)
	if err != nil {
		t.Fatalf("parse da base: %v", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, s.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	// Opaque evita a normalização do caminho pelo cliente.
	req.URL = &url.URL{Scheme: alvo.Scheme, Host: alvo.Host, Opaque: "//" + alvo.Host + caminho}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("requisição: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestPublicRefusesPathTraversal(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.publicar(t, "estado", "api", "https://exemplo.com", "API")
	s.cookie = nil

	// Cada um destes tenta sair da rota pública e alcançar outra coisa.
	ataques := map[string]string{
		"volta um nível":         "/api/v1/public/../monitors",
		"volta dois níveis":      "/api/v1/public/../../healthz",
		"ponto-ponto codificado": "/api/v1/public/%2e%2e/monitors",
		"barra codificada":       "/api/v1/public/estado%2f..%2fmonitors",
		"barra dupla":            "/api/v1/public//monitors",
		"barra invertida":        `/api/v1/public/..\monitors`,
		"nulo no meio":           "/api/v1/public/estado%00.json",
		"caminho absoluto":       "/api/v1/public//etc/passwd",
		"unicode que vira ponto": "/api/v1/public/%c0%ae%c0%ae/monitors",
	}

	for nome, caminho := range ataques {
		t.Run(nome, func(t *testing.T) {
			resp := s.atacar(t, caminho)

			// O que não pode acontecer é devolver 200 com dado de outra
			// rota. 404, 400 ou 301 são todos aceitáveis.
			if resp.StatusCode == http.StatusOK {
				corpo := readBody(t, resp)
				if strings.Contains(corpo, "target") || strings.Contains(corpo, "\"items\"") {
					t.Fatalf("travessia alcançou outra rota: %s", corpo)
				}
			}
			if resp.StatusCode >= 500 {
				t.Fatalf("travessia derrubou o servidor: %d", resp.StatusCode)
			}
		})
	}
}

func TestPublicRefusesSQLInjectionInSlug(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.publicar(t, "estado", "api", "https://exemplo.com", "API")
	s.cookie = nil

	// A validação do slug recusa aspas e espaço antes de o valor chegar ao
	// banco; o teste existe para que isso continue verdade se alguém
	// afrouxar a regra.
	for _, agulha := range []string{
		"estado' OR '1'='1",
		"estado; DROP TABLE monitor;--",
		"estado' UNION SELECT target FROM monitor--",
	} {
		t.Run(agulha, func(t *testing.T) {
			resp := s.do(t, http.MethodGet, "/api/v1/public/"+url.PathEscape(agulha), nil)

			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status %d para %q: %s", resp.StatusCode, agulha, readBody(t, resp))
			}
		})
	}

	// E a tabela continua lá.
	s.login(t)
	resp := s.do(t, http.MethodGet, "/api/v1/monitors", nil)
	assertStatus(t, resp, http.StatusOK)
}

func TestPublicIsReadOnly(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.publicar(t, "estado", "api", "https://exemplo.com", "API")
	s.cookie = nil

	// Nenhum verbo de escrita responde na rota pública. Um POST aceito
	// aqui seria escrita sem credencial.
	for _, metodo := range []string{
		http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
	} {
		t.Run(metodo, func(t *testing.T) {
			resp := s.do(t, metodo, "/api/v1/public/estado", map[string]any{"title": "invadido"})

			if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
				t.Fatalf("%s foi aceito na rota pública: %s", metodo, readBody(t, resp))
			}
		})
	}
}

func TestPublicCannotEnumeratePages(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.publicar(t, "secreta", "api", "https://exemplo.com", "API")

	// Desliga a página, para comparar com uma que nunca existiu.
	lista := decode[struct {
		Items []idResp `json:"items"`
	}](t, s.do(t, http.MethodGet, "/api/v1/status-pages", nil))
	assertStatus(t, s.do(t, http.MethodPut, "/api/v1/status-pages/"+itoa(lista.Items[0].ID),
		map[string]any{"slug": "secreta", "title": "Estado", "enabled": false}), http.StatusOK)

	s.cookie = nil

	desligada := s.do(t, http.MethodGet, "/api/v1/public/secreta", nil)
	inexistente := s.do(t, http.MethodGet, "/api/v1/public/nunca-existiu", nil)

	// As duas respostas precisam ser indistinguíveis: qualquer diferença
	// vira um oráculo para descobrir quais páginas existem.
	if desligada.StatusCode != inexistente.StatusCode {
		t.Fatalf("status divergiu: desligada %d, inexistente %d",
			desligada.StatusCode, inexistente.StatusCode)
	}
	if readBody(t, desligada) != readBody(t, inexistente) {
		t.Fatal("corpo da resposta permite distinguir página desligada de inexistente")
	}
}

func TestPublicEscapesHostileText(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	monitorID := s.publicar(t, "estado", "api", "https://exemplo.com", "API")

	// Quem escreve o relato é de dentro, mas o texto vai para uma página
	// que qualquer pessoa abre. Um script injetado aqui rodaria no
	// navegador de todo cliente que visitasse.
	hostil := `<script>alert(document.cookie)</script>`
	resp := s.do(t, http.MethodPost, "/api/v1/announcements", map[string]any{
		"title": hostil, "impact": "major", "phase": "investigating",
		"components": []int64{monitorID}, "body": "]]><script>alert(1)</script>",
	})
	assertStatus(t, resp, http.StatusCreated)

	s.cookie = nil

	t.Run("json", func(t *testing.T) {
		resp := s.do(t, http.MethodGet, "/api/v1/public/estado", nil)

		// O JSON carrega o texto cru — é o React que escapa ao desenhar.
		// O perigo aqui é o navegador tratar a resposta como HTML: basta
		// abrir a URL direto na barra de endereços. Content-Type correto e
		// nosniff fecham esse caminho.
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("resposta pública com Content-Type %q", ct)
		}
		if sniff := resp.Header.Get("X-Content-Type-Options"); sniff != "nosniff" {
			t.Errorf("resposta pública sem nosniff: %q", sniff)
		}
		if op := resp.Header.Get("X-Frame-Options"); op == "" {
			t.Error("resposta pública sem proteção contra enquadramento")
		}
	})

	t.Run("atom", func(t *testing.T) {
		resp := s.do(t, http.MethodGet, "/api/v1/public/estado/feed.atom", nil)
		corpo := readBody(t, resp)

		// No Atom o risco é real: um leitor que renderize o conteúdo
		// executaria a tag. O XML precisa sair escapado.
		if strings.Contains(corpo, "<script>") {
			t.Errorf("script não escapado no feed: %s", corpo)
		}
		if !strings.Contains(corpo, "&lt;script&gt;") {
			t.Errorf("feed não escapou o texto hostil: %s", corpo)
		}
		// E o CDATA falso não pode quebrar a estrutura do documento.
		if strings.Count(corpo, "<feed") != 1 {
			t.Errorf("estrutura do feed comprometida: %s", corpo)
		}
	})
}

func TestPublicDoesNotReflectForwardedHost(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.publicar(t, "estado", "api", "https://exemplo.com", "API")
	s.cookie = nil

	resp := s.withHeader(t, http.MethodGet, "/api/v1/public/estado/feed.atom",
		"X-Forwarded-Host", "atacante.exemplo")
	corpo := readBody(t, resp)

	if strings.Contains(corpo, "atacante.exemplo") {
		t.Errorf("feed refletiu X-Forwarded-Host:\n%s", corpo)
	}
}

func TestPublicDoesNotReflectHostHeader(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.publicar(t, "estado", "api", "https://exemplo.com", "API")
	s.cookie = nil

	// O Host também vem de quem chama, e o feed é servido com
	// Cache-Control público: um cache compartilhado guardaria a versão
	// envenenada e a serviria a quem viesse depois, com links apontando
	// para o domínio do atacante num documento que parece nosso.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		s.URL+"/api/v1/public/estado/feed.atom", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Host = "atacante.exemplo"

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("requisição: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })

	corpo := readBody(t, resp)
	if strings.Contains(corpo, "atacante.exemplo") {
		t.Errorf("feed refletiu o cabeçalho Host:\n%s", corpo)
	}
}

func TestPublicSendsNoSetCookie(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.publicar(t, "estado", "api", "https://exemplo.com", "API")
	s.cookie = nil

	resp := s.do(t, http.MethodGet, "/api/v1/public/estado", nil)

	// Uma visita anônima não pode ganhar sessão. Além do problema de
	// privacidade, um cookie emitido aqui viraria superfície de fixação.
	if len(resp.Cookies()) > 0 {
		t.Fatalf("resposta pública emitiu cookie: %+v", resp.Cookies())
	}
}

func TestPublicIgnoresStolenSessionScope(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.publicar(t, "estado", "api-interna", "http://banco.interno:5432", "API")

	// Mesmo com sessão válida, a rota pública devolve a visão pública: ela
	// não deve virar um atalho privilegiado para dados internos só porque
	// quem chamou está logado.
	resp := s.do(t, http.MethodGet, "/api/v1/public/estado", nil)
	corpo := readBody(t, resp)

	for _, agulha := range []string{"banco.interno", "api-interna", "target"} {
		if strings.Contains(corpo, agulha) {
			t.Errorf("rota pública mostrou %q para sessão autenticada:\n%s", agulha, corpo)
		}
	}
}

func TestPublicRefusesOversizedSlug(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.cookie = nil

	// Um slug enorme não pode virar consulta ao banco nem alocação grande.
	resp := s.do(t, http.MethodGet, "/api/v1/public/"+strings.Repeat("a", 4000), nil)

	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusRequestURITooLong {
		t.Fatalf("status %d para slug gigante", resp.StatusCode)
	}
}

func TestAdminRefusesCrossPageComponentRemoval(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	// Duas páginas, o mesmo alvo publicado nas duas. Remover de uma não
	// pode remover da outra — é o vazamento entre clientes ao contrário.
	monitorID := s.publicar(t, "cliente-a", "api", "https://exemplo.com", "API")

	resp := s.do(t, http.MethodPost, "/api/v1/status-pages", map[string]any{
		"slug": "cliente-b", "title": "Cliente B",
	})
	assertStatus(t, resp, http.StatusCreated)
	outraID := decode[idResp](t, resp).ID

	assertStatus(t, s.do(t, http.MethodPut,
		"/api/v1/status-pages/"+itoa(outraID)+"/components/"+itoa(monitorID),
		map[string]any{"label": "API"}), http.StatusOK)

	lista := decode[struct {
		Items []idResp `json:"items"`
	}](t, s.do(t, http.MethodGet, "/api/v1/status-pages", nil))

	assertStatus(t, s.do(t, http.MethodDelete,
		"/api/v1/status-pages/"+itoa(lista.Items[0].ID)+"/components/"+itoa(monitorID), nil),
		http.StatusNoContent)

	s.cookie = nil
	corpo := readBody(t, s.do(t, http.MethodGet, "/api/v1/public/cliente-b", nil))
	if !strings.Contains(corpo, "API") {
		t.Errorf("remoção numa página apagou o componente da outra:\n%s", corpo)
	}
}

func TestDefaultPageAnswersWithoutSlug(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.publicar(t, "acme", "api", "https://exemplo.com", "API")

	// Sem página padrão marcada, "/status" não elege uma sozinho.
	semPadrao := s.do(t, http.MethodGet, "/api/v1/public", nil)
	if semPadrao.StatusCode != http.StatusNotFound {
		t.Fatalf("sem padrão devolveu %d", semPadrao.StatusCode)
	}

	lista := decode[struct {
		Items []idResp `json:"items"`
	}](t, s.do(t, http.MethodGet, "/api/v1/status-pages", nil))
	assertStatus(t, s.do(t, http.MethodPut,
		"/api/v1/status-pages/"+itoa(lista.Items[0].ID)+"/default", nil), http.StatusOK)

	s.cookie = nil
	resp := s.do(t, http.MethodGet, "/api/v1/public", nil)

	assertStatus(t, resp, http.StatusOK)
	if !strings.Contains(readBody(t, resp), "API") {
		t.Error("página padrão não trouxe o componente")
	}
}

func TestDefaultPageRespectsDisabled(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.publicar(t, "acme", "api", "https://exemplo.com", "API")

	lista := decode[struct {
		Items []idResp `json:"items"`
	}](t, s.do(t, http.MethodGet, "/api/v1/status-pages", nil))
	id := lista.Items[0].ID

	assertStatus(t, s.do(t, http.MethodPut, "/api/v1/status-pages/"+itoa(id)+"/default", nil),
		http.StatusOK)
	assertStatus(t, s.do(t, http.MethodPut, "/api/v1/status-pages/"+itoa(id), map[string]any{
		"slug": "acme", "title": "Acme", "enabled": false,
	}), http.StatusOK)

	s.cookie = nil

	// Ser a padrão não é passe livre: desligada continua devolvendo 404.
	resp := s.do(t, http.MethodGet, "/api/v1/public", nil)
	assertStatus(t, resp, http.StatusNotFound)
}

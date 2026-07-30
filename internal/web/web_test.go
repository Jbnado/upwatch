package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bernardojoao/upwatch/internal/web"
)

func get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	return rec
}

// Descobrir que falta construir a interface olhando uma página em branco
// custa tempo demais; a resposta precisa dizer o que fazer.
func TestUnbuiltInterfaceExplainsItself(t *testing.T) {
	rec := get(t, "/")

	// Quando a interface está construída, a resposta é a aplicação; quando
	// não está, precisa ser a instrução. Em nenhum caso pode ser vazia.
	if rec.Body.Len() == 0 {
		t.Fatal("the handler returned an empty body")
	}
	if rec.Code == http.StatusServiceUnavailable {
		if !strings.Contains(rec.Body.String(), "make web") {
			t.Errorf("the not-built page does not say how to build it: %s", rec.Body.String())
		}
	}
}

// As rotas da interface vivem no navegador: recarregar em /monitors/7
// precisa devolver a aplicação, não um erro.
func TestUnknownPathServesTheApplicationShell(t *testing.T) {
	raiz := get(t, "/")
	profunda := get(t, "/monitors/7/edit")

	if profunda.Code != raiz.Code {
		t.Errorf("deep path returned %d but root returned %d; both should serve the shell",
			profunda.Code, raiz.Code)
	}
	if profunda.Body.String() != raiz.Body.String() {
		t.Error("deep path served different content than the root")
	}
}

// O index aponta para os demais arquivos; guardá-lo em cache faria o
// navegador pedir arquivos de uma versão que já saiu do ar.
func TestShellIsNotCached(t *testing.T) {
	rec := get(t, "/")

	if rec.Code != http.StatusOK {
		t.Skip("interface não construída neste ambiente")
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want %q", cc, "no-cache")
	}
}

// A casca é HTML nos dois casos: construída, é a aplicação; não
// construída, é a página que diz como construí-la.
func TestShellIsServedAsHTML(t *testing.T) {
	rec := get(t, "/")

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want HTML", ct)
	}
}

// Caminho com travessia não pode escapar do sistema de arquivos
// embarcado.
func TestTraversalIsContained(t *testing.T) {
	rec := get(t, "/../../etc/passwd")

	// O vazamento é o que importa, e vale construída ou não.
	if strings.Contains(rec.Body.String(), "root:") {
		t.Error("the response leaked a file from outside the embedded filesystem")
	}

	// O código de erro só diz alguma coisa com a interface construída:
	// sem ela, todo caminho responde 503 com a instrução de build, e
	// exigir menos que 500 aqui faria a suíte falhar num clone limpo por
	// um motivo que não é defeito.
	if !construida(t) {
		return
	}
	if rec.Code >= 500 {
		t.Errorf("status = %d, want the traversal to be contained rather than crash", rec.Code)
	}
}

// construida informa se a interface foi construída neste ambiente.
func construida(t *testing.T) bool {
	t.Helper()
	return get(t, "/").Code != http.StatusServiceUnavailable
}

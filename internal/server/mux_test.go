package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Jbnado/upwatch/internal/api"
	"github.com/Jbnado/upwatch/internal/server"
)

// Toda rota da API precisa chegar até ela.
//
// Este teste existe por causa de um defeito real: a composição tinha uma
// lista fixa de prefixos, e /metrics — registrado na API — caía no
// atendimento da interface. O sintoma é traiçoeiro porque nada falha
// alto: quem chama recebe o HTML da aplicação com status 200, e só
// descobre pelo erro de parse do outro lado.
func TestEveryAPIRouteReachesTheAPI(t *testing.T) {
	handler := api.New(api.Options{})

	rotas, ok := handler.(chi.Routes)
	if !ok {
		t.Fatal("o handler da API não expõe suas rotas")
	}

	err := chi.Walk(rotas, func(_, rota string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		caminho := concreto(rota)
		if !server.Owns(caminho) {
			t.Errorf("rota %q cairia na interface em vez da API", caminho)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("percorrendo rotas: %v", err)
	}
}

func TestUnknownPathGoesToTheInterface(t *testing.T) {
	mux := server.New(marcador("api"), marcador("interface"))

	// Qualquer caminho desconhecido é da aplicação de página única: ela
	// precisa devolver o HTML para o navegador resolver a rota.
	for _, caminho := range []string{"/", "/monitors/7", "/status/acme", "/qualquer-coisa"} {
		t.Run(caminho, func(t *testing.T) {
			if corpo := chamar(t, mux, caminho); corpo != "interface" {
				t.Fatalf("%q foi para %q", caminho, corpo)
			}
		})
	}
}

func TestInfrastructureEndpointsStayAtTheRoot(t *testing.T) {
	mux := server.New(marcador("api"), marcador("interface"))

	// Orquestrador consulta /healthz e Prometheus raspa /metrics; nenhum
	// dos dois aceita um prefixo inventado.
	for _, caminho := range []string{"/healthz", "/metrics", "/api/v1/monitors"} {
		t.Run(caminho, func(t *testing.T) {
			if corpo := chamar(t, mux, caminho); corpo != "api" {
				t.Fatalf("%q foi para %q", caminho, corpo)
			}
		})
	}
}

func TestSimilarPathsDoNotLeakToTheAPI(t *testing.T) {
	mux := server.New(marcador("api"), marcador("interface"))

	// "/metricas" e "/healthzzz" são caminhos da interface, não da API:
	// comparar por prefixo solto os capturaria por engano.
	for _, caminho := range []string{"/metricas", "/healthzzz", "/apidocs"} {
		t.Run(caminho, func(t *testing.T) {
			if corpo := chamar(t, mux, caminho); corpo != "interface" {
				t.Fatalf("%q foi para %q", caminho, corpo)
			}
		})
	}
}

// concreto troca os parâmetros da rota por valores, para o caminho poder
// ser testado como uma requisição de verdade.
func concreto(rota string) string {
	partes := strings.Split(strings.TrimSuffix(rota, "/*"), "/")
	for i, p := range partes {
		if strings.HasPrefix(p, "{") {
			partes[i] = "1"
		}
	}
	return strings.Join(partes, "/")
}

func marcador(nome string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(nome))
	})
}

func chamar(t *testing.T, h http.Handler, caminho string) string {
	t.Helper()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, caminho, nil))
	return rec.Body.String()
}

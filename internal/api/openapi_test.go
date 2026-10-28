package api_test

import (
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/Jbnado/upwatch/internal/api"
	"github.com/Jbnado/upwatch/internal/notifier"
)

// spec é a parte da especificação que o teste examina.
type spec struct {
	OpenAPI string                               `yaml:"openapi"`
	Info    struct{ Title, Version string }      `yaml:"info"`
	Paths   map[string]map[string]map[string]any `yaml:"paths"`
	Comps   struct{ Schemas map[string]any }     `yaml:"components"`
}

// fetchSpec baixa e interpreta a especificação servida pela API.
func fetchSpec(t *testing.T, s *server) spec {
	t.Helper()

	resp := s.do(t, http.MethodGet, "/api/v1/openapi.yaml", nil)
	assertStatus(t, resp, http.StatusOK)

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the spec returned unexpected error: %v", err)
	}

	var parsed spec
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("the served spec is not valid YAML: %v", err)
	}
	return parsed
}

// Descobrir como autenticar é justamente o que se procura na
// documentação; exigir credencial para lê-la criaria um ciclo.
func TestOpenAPISpecIsPublic(t *testing.T) {
	s := newServer(t)

	resp := s.do(t, http.MethodGet, "/api/v1/openapi.yaml", nil)

	assertStatus(t, resp, http.StatusOK)
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "yaml") {
		t.Errorf("Content-Type = %q, want a YAML media type", ct)
	}
}

func TestOpenAPISpecIsWellFormed(t *testing.T) {
	s := newServer(t)

	parsed := fetchSpec(t, s)

	if !strings.HasPrefix(parsed.OpenAPI, "3.") {
		t.Errorf("openapi = %q, want a 3.x version", parsed.OpenAPI)
	}
	if parsed.Info.Title == "" || parsed.Info.Version == "" {
		t.Errorf("info is incomplete: %+v", parsed.Info)
	}
	if len(parsed.Paths) == 0 {
		t.Error("the spec documents no paths")
	}
}

// routePattern normaliza uma rota do chi para comparação com a
// especificação. Os dois usam a mesma notação de parâmetro, então basta
// remover a barra final que o roteador acrescenta em grupos.
func routePattern(p string) string {
	p = strings.TrimSuffix(p, "/*")
	if p != "/" {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

// registeredRoutes enumera o que o roteador de fato atende.
func registeredRoutes(t *testing.T) map[string][]string {
	t.Helper()

	handler := api.New(api.Options{})
	router, ok := handler.(chi.Routes)
	if !ok {
		t.Fatal("the API handler does not expose its routes")
	}

	routes := map[string][]string{}
	err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = routePattern(route)
		routes[route] = append(routes[route], strings.ToLower(method))
		return nil
	})
	if err != nil {
		t.Fatalf("walking the routes returned unexpected error: %v", err)
	}
	return routes
}

// Uma especificação que descreve rota inexistente é pior que nenhuma:
// quem a segue escreve um cliente que falha em produção.
func TestOpenAPIDocumentsOnlyRealRoutes(t *testing.T) {
	s := newServer(t)
	parsed := fetchSpec(t, s)
	routes := registeredRoutes(t)

	for path, operations := range parsed.Paths {
		methods, exists := routes[path]
		if !exists {
			t.Errorf("the spec documents %q, which the router does not serve", path)
			continue
		}
		for method := range operations {
			if !contains(methods, method) {
				t.Errorf("the spec documents %s %s, but the router only serves %v",
					strings.ToUpper(method), path, methods)
			}
		}
	}
}

// O inverso importa igual: endpoint sem documentação é endpoint que
// ninguém descobre, e a API existe para ser consumida por scripts.
func TestOpenAPIDocumentsEveryRoute(t *testing.T) {
	s := newServer(t)
	parsed := fetchSpec(t, s)
	routes := registeredRoutes(t)

	var undocumented []string
	for path, methods := range routes {
		operations, documented := parsed.Paths[path]
		if !documented {
			undocumented = append(undocumented, path)
			continue
		}
		for _, method := range methods {
			if _, ok := operations[method]; !ok {
				undocumented = append(undocumented,
					strings.ToUpper(method)+" "+path)
			}
		}
	}

	sort.Strings(undocumented)
	if len(undocumented) > 0 {
		t.Errorf("these routes are served but not documented: %v", undocumented)
	}
}

// Os esquemas centrais precisam existir para o cliente gerar tipos.
func TestOpenAPIDefinesCoreSchemas(t *testing.T) {
	s := newServer(t)
	parsed := fetchSpec(t, s)

	for _, name := range []string{"Error", "User", "APIToken", "Monitor", "MonitorInput", "Heartbeat", "Rollup"} {
		if _, ok := parsed.Comps.Schemas[name]; !ok {
			t.Errorf("the spec has no %q schema", name)
		}
	}
}

// O tipo de canal está enumerado em três lugares da especificação, e quem
// gera cliente a partir dela recusa o valor que não estiver lá. Canal novo
// no notificador sem a especificação acompanhar seria um tipo que a API
// aceita e o cliente gerado não consegue mandar.
func TestOpenAPIChannelTypesFollowTheNotifier(t *testing.T) {
	s := newServer(t)

	resp := s.do(t, http.MethodGet, "/api/v1/openapi.yaml", nil)
	raw, _ := io.ReadAll(resp.Body)

	var doc struct {
		Paths map[string]struct {
			Get struct {
				Responses map[string]struct {
					Content map[string]struct {
						Schema struct {
							Properties map[string]struct {
								Items struct {
									Enum []string `yaml:"enum"`
								} `yaml:"items"`
							} `yaml:"properties"`
						} `yaml:"schema"`
					} `yaml:"content"`
				} `yaml:"responses"`
			} `yaml:"get"`
		} `yaml:"paths"`
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum []string `yaml:"enum"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the served spec is not valid YAML: %v", err)
	}

	want := append([]string(nil), notifier.Types()...)
	sort.Strings(want)

	enums := map[string][]string{
		"GET /channel-types": doc.Paths["/api/v1/channel-types"].Get.Responses["200"].
			Content["application/json"].Schema.Properties["items"].Items.Enum,
		"Channel.type":      doc.Components.Schemas["Channel"].Properties["type"].Enum,
		"ChannelInput.type": doc.Components.Schemas["ChannelInput"].Properties["type"].Enum,
	}
	for onde, got := range enums {
		got = append([]string(nil), got...)
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s enum = %v, want %v", onde, got, want)
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

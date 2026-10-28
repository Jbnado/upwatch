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
	"github.com/Jbnado/upwatch/internal/domain"
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

func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

// O enum de tipos é decidido em dois lugares, o domínio e a
// especificação. Um tipo novo que esquece a especificação faz o cliente
// gerado recusar monitores que o servidor aceita.
func TestOpenAPIMonitorTypesMatchTheDomain(t *testing.T) {
	s := newServer(t)
	parsed := fetchSpec(t, s)

	input, _ := parsed.Comps.Schemas["MonitorInput"].(map[string]any)
	props, _ := input["properties"].(map[string]any)
	typ, _ := props["type"].(map[string]any)
	var documented []string
	for _, v := range typ["enum"].([]any) {
		documented = append(documented, v.(string))
	}

	var known []string
	for i := 1; i < 256; i++ {
		if mt := domain.MonitorType(i); mt.Valid() {
			known = append(known, mt.String())
		}
	}

	sort.Strings(documented)
	sort.Strings(known)
	if strings.Join(documented, ",") != strings.Join(known, ",") {
		t.Errorf("MonitorInput.type enum = %v, want the domain types %v", documented, known)
	}
}

// A senha dos checkers de protocolo é aceita na escrita e nunca devolvida;
// a especificação precisa dizer isso, ou o cliente gerado a esperaria na
// leitura.
func TestOpenAPIProtocolConfigPasswordsAreWriteOnly(t *testing.T) {
	s := newServer(t)
	parsed := fetchSpec(t, s)

	for _, name := range []string{"RedisConfig", "PostgresConfig", "MySQLConfig"} {
		schema, ok := parsed.Comps.Schemas[name].(map[string]any)
		if !ok {
			t.Errorf("the spec has no %q schema", name)
			continue
		}
		props, _ := schema["properties"].(map[string]any)
		password, _ := props["password"].(map[string]any)
		if password["writeOnly"] != true {
			t.Errorf("%s.password is not writeOnly: %v", name, password)
		}
		if _, ok := props["password_set"]; !ok {
			t.Errorf("%s does not document password_set", name)
		}
	}
}

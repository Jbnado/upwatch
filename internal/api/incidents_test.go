package api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
)

// seedIncidents grava n quedas já encerradas do monitor e devolve os ids
// do mais recente para o mais antigo, a ordem da listagem.
func (s *server) seedIncidents(t *testing.T, monitorID int64, n int) []int64 {
	t.Helper()
	ctx := context.Background()

	ids := make([]int64, n)
	for k := range n {
		inicio := epoch.Add(time.Duration(k) * time.Hour)
		i := domain.Incident{MonitorID: monitorID, StartedAt: inicio}
		if err := s.store.Incidents().Open(ctx, &i); err != nil {
			t.Fatalf("Open returned unexpected error: %v", err)
		}
		if err := s.store.Incidents().Resolve(ctx, monitorID, inicio.Add(time.Minute)); err != nil {
			t.Fatalf("Resolve returned unexpected error: %v", err)
		}
		ids[n-1-k] = i.ID
	}
	return ids
}

type incidentPage struct {
	Items []struct {
		ID int64 `json:"id"`
	} `json:"items"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor"`
}

// Quem lê o histórico pela API segue o next_cursor até has_more ficar
// falso, e precisa ver cada incidente uma vez. O cursor antigo
// (next_after_id) apontava de novo para os mais recentes: a segunda
// página repetia a primeira, e um cliente que pagina ficava em laço.
func TestIncidentsPageThroughTheWholeHistory(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, newMonitorBody("api"))
	want := s.seedIncidents(t, id, 7)

	var got []int64
	cursor := ""
	for voltas := 0; ; voltas++ {
		if voltas > 20 {
			t.Fatalf("still paging after %d pages, ids so far %v", voltas, got)
		}
		q := url.Values{"limit": {"3"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		resp := s.do(t, http.MethodGet, "/api/v1/incidents?"+q.Encode(), nil)
		assertStatus(t, resp, http.StatusOK)
		page := decode[incidentPage](t, resp)

		for _, i := range page.Items {
			got = append(got, i.ID)
		}
		if !page.HasMore {
			if page.NextCursor != "" {
				t.Errorf("last page carries next_cursor %q, want none", page.NextCursor)
			}
			break
		}
		if page.NextCursor == "" {
			t.Fatal("has_more is true but next_cursor is missing")
		}
		cursor = page.NextCursor
	}

	if len(got) != len(want) {
		t.Fatalf("paging returned %v, want %v", got, want)
	}
	for k := range want {
		if got[k] != want[k] {
			t.Fatalf("paging returned %v, want %v", got, want)
		}
	}
}

// Cursor que não veio da API é recusado, e não tratado como "do começo":
// voltar à primeira página em silêncio é exatamente o laço que o cursor
// existe para evitar.
func TestIncidentsRejectAMalformedCursor(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	for _, cursor := range []string{"abc", "-1", "0"} {
		resp := s.do(t, http.MethodGet, "/api/v1/incidents?cursor="+url.QueryEscape(cursor), nil)
		assertStatus(t, resp, http.StatusBadRequest)
	}
}

// Quem já integra pelo after_id não pode quebrar: ele continua aceito,
// agora com o sentido corrigido (mais antigos que o id), e a resposta
// segue trazendo next_after_id junto do next_cursor. Assim o cliente
// antigo, que só conhece os nomes velhos, passa a percorrer o histórico
// em vez de ficar preso na primeira página.
func TestIncidentsKeepAfterIDAsDeprecatedAlias(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, newMonitorBody("api"))
	want := s.seedIncidents(t, id, 7)

	var got []int64
	after := int64(0)
	for voltas := 0; ; voltas++ {
		if voltas > 20 {
			t.Fatalf("still paging after %d pages, ids so far %v", voltas, got)
		}
		q := url.Values{"limit": {"3"}}
		if after != 0 {
			q.Set("after_id", strconv.FormatInt(after, 10))
		}
		resp := s.do(t, http.MethodGet, "/api/v1/incidents?"+q.Encode(), nil)
		assertStatus(t, resp, http.StatusOK)
		page := decode[struct {
			incidentPage
			NextAfterID int64 `json:"next_after_id"`
		}](t, resp)

		for _, i := range page.Items {
			got = append(got, i.ID)
		}
		if !page.HasMore {
			if page.NextAfterID != 0 {
				t.Errorf("last page carries next_after_id %d, want none", page.NextAfterID)
			}
			break
		}
		if strconv.FormatInt(page.NextAfterID, 10) != page.NextCursor {
			t.Fatalf("next_after_id = %d and next_cursor = %q, want the same position", page.NextAfterID, page.NextCursor)
		}
		after = page.NextAfterID
	}

	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("paging by after_id returned %v, want %v", got, want)
	}
}

// Os dois nomes juntos e discordando não têm leitura certa: escolher um
// em silêncio devolveria a página que o cliente não pediu.
func TestIncidentsRefuseConflictingCursorAndAfterID(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodGet, "/api/v1/incidents?cursor=5&after_id=7", nil)
	assertStatus(t, resp, http.StatusBadRequest)

	// Iguais não é conflito: é o cliente em transição mandando os dois.
	resp = s.do(t, http.MethodGet, "/api/v1/incidents?cursor=5&after_id=5", nil)
	assertStatus(t, resp, http.StatusOK)
}

package domain_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bernardojoao/upwatch/internal/domain"
)

// O fio de comunicação de um incidente.
//
// As páginas de referência têm duas camadas distintas: as barras, que a
// máquina preenche, e o relato, que uma pessoa escreve. Confundir as duas
// é o erro que vaza informação — a causa detectada por sonda diz "dial
// tcp 10.0.3.7:5432: connect: connection refused", e isso não vai para
// uma página pública.

func TestAnnouncementValidateAceitaRelatoCompleto(t *testing.T) {
	a := domain.Announcement{
		Title:  "Lentidão na API de pagamentos",
		Impact: domain.ImpactMajor,
		Phase:  domain.PhaseInvestigating,
	}

	if err := a.Validate(); err != nil {
		t.Fatalf("relato válido reprovado: %v", err)
	}
}

func TestAnnouncementValidateExigeTitulo(t *testing.T) {
	// Sem título não há o que listar em "incidentes anteriores": a página
	// mostraria uma linha vazia com um horário.
	a := domain.Announcement{Impact: domain.ImpactMinor, Phase: domain.PhaseInvestigating}

	err := a.Validate()
	if err == nil {
		t.Fatal("relato sem título passou pela validação")
	}
	assertCampo(t, err, "title")
}

func TestAnnouncementValidateRecusaFaseDesconhecida(t *testing.T) {
	a := domain.Announcement{Title: "Falha", Impact: domain.ImpactMinor, Phase: domain.IncidentPhase(99)}

	err := a.Validate()
	if err == nil {
		t.Fatal("fase inválida passou pela validação")
	}
	assertCampo(t, err, "phase")
}

func TestAnnouncementValidateRecusaImpactoDesconhecido(t *testing.T) {
	a := domain.Announcement{
		Title:  "Falha",
		Impact: domain.IncidentImpact(99),
		Phase:  domain.PhaseInvestigating,
	}

	err := a.Validate()
	if err == nil {
		t.Fatal("impacto inválido passou pela validação")
	}
	assertCampo(t, err, "impact")
}

func TestAnnouncementResolvedSegueAFase(t *testing.T) {
	// "Resolvido" é a fase, não um campo separado que alguém precisa
	// lembrar de marcar junto: dois lugares para o mesmo fato divergem.
	emCurso := domain.Announcement{Title: "Falha", Phase: domain.PhaseMonitoring}
	fechado := domain.Announcement{Title: "Falha", Phase: domain.PhaseResolved}

	if emCurso.Resolved() {
		t.Error("relato em monitoramento não deveria estar resolvido")
	}
	if !fechado.Resolved() {
		t.Error("relato na fase resolvida deveria estar resolvido")
	}
}

func TestIncidentPhaseIdaEVolta(t *testing.T) {
	// Os nomes vão para o JSON e para o banco. Se a ida e a volta
	// divergirem, um relato gravado hoje é lido errado amanhã.
	for _, fase := range []domain.IncidentPhase{
		domain.PhaseInvestigating,
		domain.PhaseIdentified,
		domain.PhaseMonitoring,
		domain.PhaseResolved,
	} {
		t.Run(fase.String(), func(t *testing.T) {
			volta, err := domain.ParseIncidentPhase(fase.String())
			if err != nil {
				t.Fatalf("nome %q não voltou: %v", fase, err)
			}
			if volta != fase {
				t.Fatalf("ida e volta divergiram: %v -> %v", fase, volta)
			}
		})
	}

	if _, err := domain.ParseIncidentPhase("resolvendo"); err == nil {
		t.Error("nome desconhecido foi aceito")
	}
}

func TestIncidentImpactIdaEVolta(t *testing.T) {
	for _, impacto := range []domain.IncidentImpact{
		domain.ImpactNone,
		domain.ImpactMinor,
		domain.ImpactMajor,
		domain.ImpactCritical,
	} {
		t.Run(impacto.String(), func(t *testing.T) {
			volta, err := domain.ParseIncidentImpact(impacto.String())
			if err != nil {
				t.Fatalf("nome %q não voltou: %v", impacto, err)
			}
			if volta != impacto {
				t.Fatalf("ida e volta divergiram: %v -> %v", impacto, volta)
			}
		})
	}

	if _, err := domain.ParseIncidentImpact("catastrofico"); err == nil {
		t.Error("nome desconhecido foi aceito")
	}
}

func TestAnnouncementUpdateExigeCorpo(t *testing.T) {
	// Uma atualização sem texto é uma linha do tempo com um horário e
	// nada — pior que não publicar, porque parece que houve notícia.
	u := domain.AnnouncementUpdate{Phase: domain.PhaseIdentified, Body: "  "}

	err := u.Validate()
	if err == nil {
		t.Fatal("atualização sem corpo passou pela validação")
	}
	assertCampo(t, err, "body")
}

func TestPublicAnnouncementNaoRevelaCausaDetectada(t *testing.T) {
	// A causa vem de sonda e cita host e porta internos. O relato público
	// carrega o que a pessoa escreveu, e o tipo não tem onde guardar a
	// outra coisa.
	a := domain.PublicAnnouncement{
		Title:      "Lentidão na API",
		Impact:     domain.ImpactMinor,
		Phase:      domain.PhaseResolved,
		Components: []string{"API"},
		Updates: []domain.PublicAnnouncementUpdate{
			{Phase: domain.PhaseResolved, Body: "Normalizado.", PublishedAt: time.Now()},
		},
	}

	bruto, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("serializando: %v", err)
	}

	for _, proibido := range []string{"cause", "target", "monitor_id", "config"} {
		if strings.Contains(string(bruto), proibido) {
			t.Errorf("JSON público contém %q: %s", proibido, bruto)
		}
	}
}

func TestAnnouncementSemComponenteApareceEmTodaPagina(t *testing.T) {
	// "Migração de banco hoje à noite" vale para a plataforma inteira, e
	// exigir que o operador marque os quarenta monitores um a um faria com
	// que ele simplesmente não marcasse nenhum.
	a := domain.Announcement{Title: "Manutenção programada"}

	if !a.ShowsOn([]int64{7, 8}) {
		t.Error("relato sem componente deveria aparecer na página")
	}
	if !a.ShowsOn(nil) {
		t.Error("relato sem componente deveria aparecer até em página sem alvo")
	}
}

func TestAnnouncementApareceOndeUmComponenteEhPublicado(t *testing.T) {
	a := domain.Announcement{Title: "Falha", Components: []int64{3, 9}}

	if !a.ShowsOn([]int64{1, 9}) {
		t.Error("relato deveria aparecer: o componente 9 está na página")
	}
}

func TestAnnouncementNaoVazaParaPaginaDeOutroCliente(t *testing.T) {
	// Duas páginas para dois clientes: a queda de um não pode aparecer na
	// página do outro, que é o pior tipo de vazamento numa página pública
	// — informação sobre terceiro.
	a := domain.Announcement{Title: "Falha no cliente A", Components: []int64{3}}

	if a.ShowsOn([]int64{10, 11}) {
		t.Error("relato apareceu numa página que não publica nenhum componente afetado")
	}
}

func TestAnnouncementImpactoDominaOTopoDaPagina(t *testing.T) {
	// O banner da página precisa refletir o relato aberto de maior
	// impacto, não a média nem o mais recente: um aviso menor publicado
	// depois de uma queda crítica não pode acalmar o topo.
	relatos := []domain.PublicAnnouncement{
		{Title: "Aviso", Impact: domain.ImpactMinor, Phase: domain.PhaseMonitoring},
		{Title: "Queda", Impact: domain.ImpactCritical, Phase: domain.PhaseIdentified},
		{Title: "Antigo", Impact: domain.ImpactMajor, Phase: domain.PhaseResolved},
	}

	if got := domain.WorstOpenImpact(relatos); got != domain.ImpactCritical {
		t.Fatalf("esperava critical, veio %s", got)
	}
}

func TestAnnouncementResolvidoNaoPesaNoTopo(t *testing.T) {
	relatos := []domain.PublicAnnouncement{
		{Title: "Antigo", Impact: domain.ImpactCritical, Phase: domain.PhaseResolved},
	}

	if got := domain.WorstOpenImpact(relatos); got != domain.ImpactNone {
		t.Fatalf("esperava none, veio %s", got)
	}
}

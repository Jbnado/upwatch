package domain

import (
	"fmt"
	"slices"
	"strings"
)

// Etiquetas de monitor.
//
// Servem para separar ambientes e times — homolog e produção é o caso
// que motivou existirem. São digitadas à mão, uma por vez, em momentos
// diferentes, e por isso a normalização importa mais que a validação:
// sem ela, "Produção", "produção" e "produção " viram três grupos para a
// mesma coisa, e quem cadastrou só descobre quando o painel mostra o
// mesmo nome duas vezes.

const (
	// maxTagLength limita uma etiqueta. Etiqueta é rótulo, não frase.
	maxTagLength = 40

	// maxTags por monitor. Dez já é generoso: acima disso elas param de
	// organizar e passam a encher a linha do painel e a lista de filtros.
	maxTags = 10
)

// NormalizeTags põe as etiquetas na forma canônica.
//
// Minúsculas porque é a convenção de rótulo em toda ferramenta vizinha —
// Prometheus, Docker, Kubernetes — e porque comparar por caixa
// preservando a original exigiria guardar duas formas de cada etiqueta,
// com as duas podendo divergir.
//
// Ordenadas porque a ordem de digitação não é a ordem de leitura: sem
// isso, o mesmo conjunto apareceria em ordens diferentes conforme quem
// cadastrou.
func NormalizeTags(tags []string) []string {
	if tags == nil {
		return nil
	}

	vistas := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))

	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if _, repetida := vistas[t]; repetida {
			continue
		}
		vistas[t] = struct{}{}
		out = append(out, t)
	}

	slices.Sort(out)
	return out
}

// validateTags confere as invariantes das etiquetas.
//
// Recusa só aspa e barra invertida, e por um motivo concreto: as
// etiquetas são guardadas como array JSON e localizadas por substring
// entre aspas, então esses dois caracteres quebram a delimitação e fazem
// a busca casar o que não devia.
//
// Curinga de LIKE — "%" e "_" — não entra nesta lista de propósito.
// "api_v2" é etiqueta legítima, e proibi-la para proteger uma consulta
// mal escrita seria contorcer o domínio por causa de um defeito que mora
// no store. Lá a consulta escapa os curingas.
func validateTags(tags []string) error {
	if len(tags) > maxTags {
		return invalid("tags", fmt.Sprintf("no máximo %d etiquetas por monitor", maxTags))
	}

	for _, t := range tags {
		if strings.TrimSpace(t) == "" {
			return invalid("tags", "etiqueta não pode ser vazia")
		}
		if len(t) > maxTagLength {
			return invalid("tags", fmt.Sprintf("etiqueta não pode passar de %d caracteres", maxTagLength))
		}
		if strings.ContainsAny(t, `"\`) {
			return invalid("tags", "etiqueta não pode conter aspas nem barra invertida")
		}
	}
	return nil
}

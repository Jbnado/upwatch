package domain

import (
	"fmt"
	"time"
)

// Resolution é a granularidade de um rollup.
//
// O UpWatch mantém batidas cruas por poucos dias e agrega em buckets
// horários e diários, que sobrevivem por meses. É o que impede o banco
// de crescer linearmente com o tempo.
type Resolution uint8

const (
	// ResolutionHourly agrega em buckets de uma hora.
	ResolutionHourly Resolution = iota + 1
	// ResolutionDaily agrega em buckets de um dia.
	ResolutionDaily
)

var resolutionNames = map[Resolution]string{
	ResolutionHourly: "hourly",
	ResolutionDaily:  "daily",
}

// String devolve o nome canônico da resolução.
func (r Resolution) String() string {
	if name, ok := resolutionNames[r]; ok {
		return name
	}
	return "unknown"
}

// ParseResolution converte o nome canônico de volta em Resolution.
func ParseResolution(name string) (Resolution, error) {
	for res, n := range resolutionNames {
		if n == name {
			return res, nil
		}
	}
	return 0, fmt.Errorf("domain: resolução inválida %q", name)
}

// Duration é o tamanho do bucket.
func (r Resolution) Duration() time.Duration {
	if r == ResolutionDaily {
		return 24 * time.Hour
	}
	return time.Hour
}

// Truncate devolve o início do bucket que contém t.
//
// O resultado é sempre UTC: uma amostra num fuso negativo que já cruzou a
// meia-noite UTC pertence ao dia UTC seguinte, e truncar no fuso local a
// colocaria no bucket errado.
func (r Resolution) Truncate(t time.Time) time.Time {
	return t.UTC().Truncate(r.Duration())
}

// BucketClosed informa se o bucket iniciado em bucketStart já terminou.
//
// O worker de rollup só agrega buckets encerrados — agregar o bucket
// corrente gravaria estatística parcial como se fosse definitiva.
func (r Resolution) BucketClosed(bucketStart, now time.Time) bool {
	return !now.Before(bucketStart.Add(r.Duration()))
}

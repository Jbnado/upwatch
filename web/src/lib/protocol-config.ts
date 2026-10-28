/**
 * Montagem da configuração dos monitores de protocolo: Redis, PostgreSQL
 * e MySQL.
 *
 * Fica fora do formulário porque é onde estão as decisões — o que vira
 * campo, o que é descartado, o que é recusado antes de sair da tela — e a
 * mais delicada delas é a senha: o servidor nunca a devolve, só diz se há
 * uma guardada (password_set). Deixar o campo em branco mantém a guardada;
 * remover é uma escolha explícita.
 *
 * A validação do servidor continua sendo a que vale; recusar aqui serve
 * para o erro aparecer embaixo do campo certo.
 */

import type { MonitorType } from "../api/types";

export type TipoProtocolo = "redis" | "postgres" | "mysql";

export function ehProtocolo(tipo: MonitorType): tipo is TipoProtocolo {
  return tipo === "redis" || tipo === "postgres" || tipo === "mysql";
}

/** Comandos de leitura aceitos nas asserções, espelhando o servidor. */
export const COMANDOS_REDIS = ["SCARD", "LLEN", "GET", "EXISTS", "HGET", "ZCARD", "DBSIZE", "INFO"];

export const OPERADORES: { value: string; label: string }[] = [
  { value: "gte", label: ">=" },
  { value: "gt", label: ">" },
  { value: "eq", label: "==" },
  { value: "ne", label: "!=" },
  { value: "lte", label: "<=" },
  { value: "lt", label: "<" },
];

/** O que cada comando pede além do operador e do valor. */
export function argumentosDe(comando: string): { chave: boolean; campo: boolean; secao: boolean } {
  switch (comando) {
    case "DBSIZE":
      return { chave: false, campo: false, secao: false };
    case "INFO":
      return { chave: false, campo: true, secao: true };
    case "HGET":
      return { chave: true, campo: true, secao: false };
    default:
      return { chave: true, campo: false, secao: false };
  }
}

export type Assercao = {
  comando: string;
  chave: string;
  campo: string;
  secao: string;
  op: string;
  /** Texto do campo; vira número na montagem. */
  valor: string;
};

export type EntradaProtocolo = {
  usuario: string;
  senha: string;
  /** O servidor tem uma senha guardada para este monitor. */
  senhaGuardada: boolean;
  removerSenha: boolean;
  /** Nome do banco (SQL) ou número do banco (Redis). */
  banco: string;
  /** sslmode no PostgreSQL, tls no MySQL. */
  tls: string;
  consulta: string;
  // Só Redis.
  redisTLS: boolean;
  ignorarCertificado: boolean;
  memoriaMaximaMB: string;
  assercoes: Assercao[];
};

export function protocoloVazio(): EntradaProtocolo {
  return {
    usuario: "",
    senha: "",
    senhaGuardada: false,
    removerSenha: false,
    banco: "",
    tls: "",
    consulta: "",
    redisTLS: false,
    ignorarCertificado: false,
    memoriaMaximaMB: "",
    assercoes: [],
  };
}

export function assercaoVazia(): Assercao {
  return { comando: "SCARD", chave: "", campo: "", secao: "", op: "gte", valor: "" };
}

export type ResultadoProtocolo = {
  config?: Record<string, unknown>;
  erro?: { campo: string; msg: string };
};

const MB = 1024 * 1024;

export function montarProtocolo(tipo: TipoProtocolo, e: EntradaProtocolo): ResultadoProtocolo {
  const config: Record<string, unknown> = {};

  const usuario = e.usuario.trim();
  if (usuario) config.username = usuario;

  if (e.senha !== "") {
    config.password = e.senha;
  } else if (e.removerSenha && e.senhaGuardada) {
    config.password = "";
  }
  // Em branco sem pedido de remoção: o campo não vai, e o servidor mantém
  // a senha guardada.

  if (tipo === "redis") return montarRedis(config, e);

  if (!usuario) {
    return { erro: { campo: "usuario", msg: "Informe o usuário que o monitor usa para entrar." } };
  }
  const banco = e.banco.trim();
  if (banco) config.database = banco;
  if (e.tls) config[tipo === "postgres" ? "sslmode" : "tls"] = e.tls;
  const consulta = e.consulta.trim();
  if (consulta) config.query = consulta;
  return { config };
}

function montarRedis(config: Record<string, unknown>, e: EntradaProtocolo): ResultadoProtocolo {
  const banco = e.banco.trim();
  if (banco) {
    const n = Number(banco);
    if (!Number.isInteger(n) || n < 0) {
      return { erro: { campo: "banco", msg: "O banco do Redis é um número a partir de 0." } };
    }
    if (n > 0) config.db = n;
  }
  if (e.redisTLS) config.tls = true;
  if (e.redisTLS && e.ignorarCertificado) config.skip_tls_verify = true;

  const memoria = e.memoriaMaximaMB.trim();
  if (memoria) {
    const n = Number(memoria);
    if (!Number.isFinite(n) || n < 0) {
      return { erro: { campo: "memoria", msg: "O limite de memória é um número de MB." } };
    }
    if (n > 0) config.max_used_memory_bytes = Math.round(n * MB);
  }

  const assertions: Record<string, unknown>[] = [];
  for (const [i, a] of e.assercoes.entries()) {
    // Linha sem comando é a linha em branco que o formulário oferece.
    if (!a.comando) continue;

    const montada = montarAssercao(a);
    if (typeof montada === "string") {
      return { erro: { campo: "assercoes", msg: `Condição ${i + 1}: ${montada}` } };
    }
    assertions.push(montada);
  }
  if (assertions.length > 0) config.assertions = assertions;

  return { config };
}

function montarAssercao(a: Assercao): Record<string, unknown> | string {
  const pede = argumentosDe(a.comando);
  const saida: Record<string, unknown> = { command: a.comando };

  if (pede.chave) {
    if (!a.chave.trim()) return `${a.comando} precisa de uma chave.`;
    saida.key = a.chave.trim();
  }
  if (pede.secao) {
    if (!a.secao.trim()) return "INFO precisa de uma seção, como memory.";
    saida.section = a.secao.trim();
  }
  if (pede.campo) {
    if (!a.campo.trim()) return `${a.comando} precisa de um campo.`;
    saida.field = a.campo.trim();
  }

  // Number("") é 0, e "SCARD >= 0" passa sempre: a condição existiria
  // sem vigiar nada.
  const valor = a.valor.trim();
  const n = Number(valor);
  if (valor === "" || !Number.isFinite(n)) return "o valor esperado precisa ser um número.";

  saida.op = a.op;
  saida.value = n;
  return saida;
}

/** lerProtocolo converte a configuração devolvida pelo servidor. */
export function lerProtocolo(tipo: TipoProtocolo, config: Record<string, unknown> | undefined): EntradaProtocolo {
  const c = config ?? {};
  const texto = (v: unknown) => (v === undefined || v === null ? "" : String(v));
  const e = protocoloVazio();

  e.usuario = texto(c.username);
  e.senhaGuardada = c.password_set === true;

  if (tipo === "redis") {
    e.banco = c.db ? texto(c.db) : "";
    e.redisTLS = c.tls === true;
    e.ignorarCertificado = c.skip_tls_verify === true;
    const bytes = Number(c.max_used_memory_bytes ?? 0);
    e.memoriaMaximaMB = bytes > 0 ? String(bytes / MB) : "";
    const lista = Array.isArray(c.assertions) ? (c.assertions as Record<string, unknown>[]) : [];
    e.assercoes = lista.map((a) => ({
      comando: texto(a.command).toUpperCase(),
      chave: texto(a.key),
      campo: texto(a.field),
      secao: texto(a.section),
      op: texto(a.op),
      valor: texto(a.value),
    }));
    return e;
  }

  e.banco = texto(c.database);
  e.tls = texto(tipo === "postgres" ? c.sslmode : c.tls);
  e.consulta = texto(c.query);
  return e;
}

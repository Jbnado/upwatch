import { describe, expect, it } from "vitest";
import {
  COMANDOS_REDIS,
  ehProtocolo,
  exemploDeChave,
  lerProtocolo,
  montarProtocolo,
  protocoloVazio,
  type Assercao,
} from "./protocol-config";

/**
 * A montagem da configuração dos monitores de Redis, PostgreSQL e MySQL.
 *
 * Fica fora do formulário pelo mesmo motivo da do canal: é onde estão as
 * decisões — principalmente o que fazer com a senha, que o servidor nunca
 * devolve.
 */

const assercao = (a: Partial<Assercao>): Assercao => ({
  comando: "SCARD",
  chave: "",
  campo: "",
  secao: "",
  op: "gte",
  valor: "",
  ...a,
});

describe("ehProtocolo", () => {
  it("reconhece só os tipos de protocolo", () => {
    expect(ehProtocolo("redis")).toBe(true);
    expect(ehProtocolo("postgres")).toBe(true);
    expect(ehProtocolo("mysql")).toBe(true);
    expect(ehProtocolo("tcp")).toBe(false);
  });
});

describe("montarProtocolo — senha", () => {
  // A senha não volta do servidor. Mandar password vazio numa edição
  // apagaria a credencial guardada; omitir o campo é o que a mantém.
  it("omite a senha em branco, para o servidor manter a guardada", () => {
    const entrada = { ...protocoloVazio(), usuario: "monitor", senhaGuardada: true };

    const r = montarProtocolo("postgres", entrada);

    expect(r.erro).toBeUndefined();
    expect(r.config).not.toHaveProperty("password");
  });

  it("manda a senha digitada", () => {
    const r = montarProtocolo("postgres", { ...protocoloVazio(), usuario: "u", senha: "nova" });

    expect(r.config?.password).toBe("nova");
  });

  // Remover a senha é uma escolha explícita, não um efeito colateral de
  // deixar o campo em branco.
  it("manda senha vazia só quando a remoção é pedida", () => {
    const r = montarProtocolo("redis", {
      ...protocoloVazio(),
      senhaGuardada: true,
      removerSenha: true,
    });

    expect(r.config?.password).toBe("");
  });
});

describe("montarProtocolo — banco", () => {
  it("exige usuário em PostgreSQL e MySQL", () => {
    const r = montarProtocolo("mysql", protocoloVazio());

    expect(r.erro?.campo).toBe("usuario");
  });

  it("monta a configuração do PostgreSQL", () => {
    const r = montarProtocolo("postgres", {
      ...protocoloVazio(),
      usuario: "monitor",
      banco: "app",
      tls: "require",
      consulta: "  SELECT 1  ",
    });

    expect(r.config).toEqual({
      username: "monitor",
      database: "app",
      sslmode: "require",
      query: "SELECT 1",
    });
  });

  it("usa o campo tls no MySQL", () => {
    const r = montarProtocolo("mysql", { ...protocoloVazio(), usuario: "m", tls: "skip-verify" });

    expect(r.config).toEqual({ username: "m", tls: "skip-verify" });
  });
});

describe("montarProtocolo — Redis", () => {
  it("converte o limite de memória de MB para bytes", () => {
    const r = montarProtocolo("redis", { ...protocoloVazio(), memoriaMaximaMB: "512" });

    expect(r.config?.max_used_memory_bytes).toBe(512 * 1024 * 1024);
  });

  it("monta as asserções e descarta linha sem comando", () => {
    const r = montarProtocolo("redis", {
      ...protocoloVazio(),
      assercoes: [
        assercao({ chave: "rq:workers:default", op: "gte", valor: "1" }),
        assercao({ comando: "", chave: "" }),
        assercao({ comando: "INFO", secao: "memory", campo: "used_memory", op: "lt", valor: "100" }),
      ],
    });

    expect(r.erro).toBeUndefined();
    expect(r.config?.assertions).toEqual([
      { command: "SCARD", key: "rq:workers:default", op: "gte", value: 1 },
      { command: "INFO", section: "memory", field: "used_memory", op: "lt", value: 100 },
    ]);
  });

  // Um valor vazio viraria 0 em Number(""), e "SCARD >= 0" passa sempre:
  // a asserção existiria sem vigiar nada.
  it("recusa asserção sem valor numérico", () => {
    const r = montarProtocolo("redis", {
      ...protocoloVazio(),
      assercoes: [assercao({ chave: "k", valor: "" })],
    });

    expect(r.erro?.campo).toBe("assercoes");
  });

  it("recusa asserção sem a chave que o comando exige", () => {
    const r = montarProtocolo("redis", {
      ...protocoloVazio(),
      assercoes: [assercao({ comando: "LLEN", valor: "10" })],
    });

    expect(r.erro?.campo).toBe("assercoes");
  });
});

describe("lerProtocolo", () => {
  it("lê o que o servidor devolveu, sabendo que há senha sem conhecê-la", () => {
    const e = lerProtocolo("redis", {
      username: "monitor",
      password_set: true,
      db: 2,
      max_used_memory_bytes: 256 * 1024 * 1024,
      assertions: [{ command: "LLEN", key: "rq:queue:default", op: "lt", value: 500 }],
    });

    expect(e.usuario).toBe("monitor");
    expect(e.senha).toBe("");
    expect(e.senhaGuardada).toBe(true);
    expect(e.banco).toBe("2");
    expect(e.memoriaMaximaMB).toBe("256");
    expect(e.assercoes).toEqual([
      assercao({ comando: "LLEN", chave: "rq:queue:default", op: "lt", valor: "500" }),
    ]);
  });

  it("faz o caminho de volta sem perder nada", () => {
    const config = {
      username: "monitor",
      database: "app",
      sslmode: "verify-full",
      query: "SELECT now()",
    };

    expect(montarProtocolo("postgres", lerProtocolo("postgres", config)).config).toEqual(config);
  });
});

describe("montarProtocolo — WORKERS", () => {
  // O Bull e o BullMQ não registram worker em chave nenhuma; WORKERS conta
  // as conexões que eles nomeiam. Sem o comando na lista, quem vigia fila
  // Bull pela interface fica com EXISTS no stalled-check, que pisca.
  it("oferece WORKERS entre os comandos", () => {
    expect(COMANDOS_REDIS).toContain("WORKERS");
  });

  it("manda prefixo e fila juntos na chave", () => {
    const r = montarProtocolo("redis", {
      ...protocoloVazio(),
      assercoes: [assercao({ comando: "WORKERS", chave: "bull:excel-render", op: "gte", valor: "1" })],
    });

    expect(r.erro).toBeUndefined();
    expect(r.config?.assertions).toEqual([{ command: "WORKERS", key: "bull:excel-render", op: "gte", value: 1 }]);
  });

  // Só o nome da fila não dá para montar o nome da conexão do worker; o
  // servidor recusaria, e o erro aqui aparece embaixo do campo certo.
  it("recusa WORKERS sem o prefixo", () => {
    const r = montarProtocolo("redis", {
      ...protocoloVazio(),
      assercoes: [assercao({ comando: "WORKERS", chave: "excel-render", valor: "1" })],
    });

    expect(r.erro?.campo).toBe("assercoes");
    expect(r.erro?.msg).toContain("bull:");
  });

  it("sugere a chave no formato de cada comando", () => {
    expect(exemploDeChave("WORKERS")).toBe("bull:minha-fila");
    expect(exemploDeChave("SCARD")).toBe("rq:workers:default");
  });
});

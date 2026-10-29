import { describe, expect, it } from "vitest";
import {
  lerConfig,
  montarConfig,
  montarConfigEmail,
  montarConfigNtfy,
  ntfyServidorPublico,
  type EntradaEmail,
  type LinhaCabecalho,
} from "./channel-config";

/**
 * A montagem da configuração do canal.
 *
 * Vive separada do formulário porque é onde estão as decisões: o que vira
 * campo, o que é descartado e o que é recusado antes de sair da tela. Um
 * mapeamento malformado recusado aqui mostra o erro embaixo do campo; o
 * mesmo erro vindo do servidor chega como "config inválida" sem dizer onde.
 */

const cab = (nome: string, valor: string): LinhaCabecalho => ({ nome, valor });

describe("montarConfig", () => {
  it("manda só a url quando nada mais foi preenchido", () => {
    const r = montarConfig({ url: "https://x.exemplo", cabecalhos: [], mapeamento: "" });

    expect(r.erro).toBeUndefined();
    expect(r.config).toEqual({ url: "https://x.exemplo" });
  });

  it("transforma as linhas de cabeçalho num objeto", () => {
    const r = montarConfig({
      url: "https://x.exemplo",
      cabecalhos: [cab("Authorization", "Bearer abc"), cab("X-Origem", "upwatch")],
      mapeamento: "",
    });

    expect(r.config?.headers).toEqual({ Authorization: "Bearer abc", "X-Origem": "upwatch" });
  });

  // O formulário começa com uma linha em branco para ter onde digitar.
  // Mandá-la produziria um cabeçalho de nome vazio, que alguns servidores
  // recusam com 400 — e o erro não teria relação aparente com a causa.
  it("descarta linha de cabeçalho em branco", () => {
    const r = montarConfig({
      url: "https://x.exemplo",
      cabecalhos: [cab("", ""), cab("X-Key", "k"), cab("  ", "sem nome")],
      mapeamento: "",
    });

    expect(r.config?.headers).toEqual({ "X-Key": "k" });
  });

  it("apara espaço em volta do nome do cabeçalho", () => {
    const r = montarConfig({
      url: "https://x.exemplo",
      cabecalhos: [cab("  X-Key  ", "  k  ")],
      mapeamento: "",
    });

    expect(r.config?.headers).toEqual({ "X-Key": "k" });
  });

  // Um cabeçalho sem valor é legítimo; um sem nome não é.
  it("mantém cabeçalho de valor vazio", () => {
    const r = montarConfig({
      url: "https://x.exemplo",
      cabecalhos: [cab("X-Vazio", "")],
      mapeamento: "",
    });

    expect(r.config?.headers).toEqual({ "X-Vazio": "" });
  });

  it("não manda o campo de cabeçalhos quando todas as linhas estão vazias", () => {
    const r = montarConfig({
      url: "https://x.exemplo",
      cabecalhos: [cab("", ""), cab("", "")],
      mapeamento: "",
    });

    expect(r.config).toEqual({ url: "https://x.exemplo" });
  });

  it("aceita mapeamento válido", () => {
    const r = montarConfig({
      url: "https://x.exemplo",
      cabecalhos: [],
      mapeamento: '{"event":"$status"}',
    });

    expect(r.erro).toBeUndefined();
    expect(r.config?.body_template).toEqual({ event: "$status" });
  });

  it("recusa mapeamento que não é JSON, apontando o campo", () => {
    const r = montarConfig({
      url: "https://x.exemplo",
      cabecalhos: [],
      mapeamento: '{"event": }',
    });

    expect(r.config).toBeUndefined();
    expect(r.erro?.campo).toBe("mapeamento");
    expect(r.erro?.msg).toBeTruthy();
  });

  // Só espaço em branco é o mesmo que não ter preenchido.
  it("ignora mapeamento em branco", () => {
    const r = montarConfig({ url: "https://x.exemplo", cabecalhos: [], mapeamento: "   \n " });

    expect(r.erro).toBeUndefined();
    expect(r.config).toEqual({ url: "https://x.exemplo" });
  });

  // O corpo precisa ser um objeto ou uma lista. Um número solto é JSON
  // válido e nenhum destino espera receber "7" como corpo de alerta —
  // recusar aqui evita um canal que só falha na hora da queda.
  it("recusa mapeamento que não é objeto nem lista", () => {
    for (const bruto of ["7", '"texto"', "true", "null"]) {
      const r = montarConfig({ url: "https://x.exemplo", cabecalhos: [], mapeamento: bruto });
      expect(r.erro?.campo, `aceitou ${bruto}`).toBe("mapeamento");
    }
  });

  it("mantém a ordem quando dois cabeçalhos têm o mesmo nome, o último vence", () => {
    const r = montarConfig({
      url: "https://x.exemplo",
      cabecalhos: [cab("X-Key", "primeiro"), cab("X-Key", "segundo")],
      mapeamento: "",
    });

    expect(r.config?.headers).toEqual({ "X-Key": "segundo" });
  });
});

const email = (parcial: Partial<EntradaEmail> = {}): EntradaEmail => ({
  host: "smtp.exemplo.com",
  porta: "",
  seguranca: "starttls",
  usuario: "",
  senha: "",
  remetente: "UpWatch <alertas@exemplo.com>",
  destinatarios: "plantao@exemplo.com",
  ...parcial,
});

describe("montarConfigEmail", () => {
  it("monta o mínimo sem campos opcionais vazios", () => {
    const r = montarConfigEmail(email());

    expect(r.erro).toBeUndefined();
    expect(r.config).toEqual({
      host: "smtp.exemplo.com",
      security: "starttls",
      from: "UpWatch <alertas@exemplo.com>",
      to: ["plantao@exemplo.com"],
    });
  });

  // Lista de distribuição costuma ser colada de outro lugar, com vírgula,
  // ponto e vírgula ou uma por linha. Exigir um formato só faria a pessoa
  // reformatar à mão a lista que já tem.
  it("aceita destinatários separados por vírgula, ponto e vírgula ou linha", () => {
    const r = montarConfigEmail(
      email({ destinatarios: " a@exemplo.com, b@exemplo.com;c@exemplo.com\n\n d@exemplo.com ," }),
    );

    expect(r.config?.to).toEqual(["a@exemplo.com", "b@exemplo.com", "c@exemplo.com", "d@exemplo.com"]);
  });

  it("recusa sem destinatário, apontando o campo", () => {
    const r = montarConfigEmail(email({ destinatarios: " , ; " }));

    expect(r.config).toBeUndefined();
    expect(r.erro?.campo).toBe("destinatarios");
  });

  // Porta em branco segue o modo (587, 465 ou 25); mandar 0 ou "" faria
  // o servidor tentar interpretar algo que ninguém escolheu.
  it("só manda a porta quando preenchida, como número", () => {
    expect(montarConfigEmail(email()).config).not.toHaveProperty("port");
    expect(montarConfigEmail(email({ porta: "2525" })).config?.port).toBe(2525);
  });

  it("recusa porta que não é número de porta", () => {
    for (const porta of ["abc", "0", "70000", "25.5"]) {
      const r = montarConfigEmail(email({ porta }));
      expect(r.erro?.campo, `aceitou ${porta}`).toBe("porta");
    }
  });

  it("manda usuário e senha quando preenchidos", () => {
    const r = montarConfigEmail(email({ usuario: "alertas", senha: "s3nha" }));

    expect(r.config?.username).toBe("alertas");
    expect(r.config?.password).toBe("s3nha");
  });

  // Senha com espaço nas pontas é senha válida; aparar trocaria a
  // credencial e o erro de autenticação não teria explicação aparente.
  it("não apara a senha", () => {
    const r = montarConfigEmail(email({ usuario: "u", senha: " com espaço " }));

    expect(r.config?.password).toBe(" com espaço ");
  });
});

describe("montarConfigNtfy", () => {
  it("monta só o tópico quando o resto está vazio", () => {
    const r = montarConfigNtfy({ servidor: "", topico: "upwatch-alertas", token: "" });

    expect(r.erro).toBeUndefined();
    expect(r.config).toEqual({ topic: "upwatch-alertas" });
  });

  it("manda servidor e token quando preenchidos", () => {
    const r = montarConfigNtfy({
      servidor: " https://ntfy.exemplo.com ",
      topico: " infra ",
      token: "tk_abc",
    });

    expect(r.config).toEqual({ server: "https://ntfy.exemplo.com", topic: "infra", token: "tk_abc" });
  });

  // O celular assina o tópico pelo nome exato. Barra ou espaço publicariam
  // em outro caminho, e o aviso não chegaria a ninguém.
  it("recusa tópico com caractere que o ntfy não aceita", () => {
    for (const topico of ["", "a/b", "meu topico", "ação"]) {
      const r = montarConfigNtfy({ servidor: "", topico, token: "" });
      expect(r.erro?.campo, `aceitou ${JSON.stringify(topico)}`).toBe("topico");
    }
  });
});

describe("ntfyServidorPublico", () => {
  // No servidor público o tópico é a única credencial, e quem cadastra
  // precisa saber disso antes de escolher "alertas" como nome. O aviso
  // aparece em branco também, porque é o servidor que vale sem nada
  // preenchido.
  it("reconhece o servidor público, inclusive em branco", () => {
    for (const servidor of ["", "  ", "https://ntfy.sh", "https://NTFY.sh/", "http://ntfy.sh"]) {
      expect(ntfyServidorPublico(servidor), `servidor ${JSON.stringify(servidor)}`).toBe(true);
    }
  });

  it("não confunde servidor próprio com o público", () => {
    for (const servidor of ["https://ntfy.exemplo.com", "https://ntfy.sh.exemplo.com", "http://localhost:2586"]) {
      expect(ntfyServidorPublico(servidor), `servidor ${JSON.stringify(servidor)}`).toBe(false);
    }
  });
});

describe("edição", () => {
  it("sem a URL digitada, não manda url quando há uma guardada", () => {
    const r = montarConfig({ url: "  ", cabecalhos: [], mapeamento: "", urlGuardada: true });

    expect(r.config).toEqual({});
  });

  it("cabeçalho guardado em branco pede o valor guardado; preenchido, troca", () => {
    const r = montarConfig({
      url: "",
      urlGuardada: true,
      cabecalhos: [
        { nome: "Authorization", valor: "", mantido: true },
        { nome: "X-Api-Key", valor: "nova", mantido: true },
        cab("X-Vazio", ""),
      ],
      mapeamento: "",
    });

    expect(r.config?.headers).toEqual({ Authorization: "***", "X-Api-Key": "nova", "X-Vazio": "" });
  });

  it("devolve o modelo de mensagem que a tela não edita", () => {
    const r = montarConfig({ url: "https://x.exemplo", cabecalhos: [], mapeamento: "", template: "t" });

    expect(r.config).toEqual({ url: "https://x.exemplo", template: "t" });
  });

  it("lerConfig preenche o formulário sem segredo e marca o que está guardado", () => {
    const e = lerConfig({
      url_set: true,
      headers: { Authorization: "***" },
      body_template: { event: "$status" },
      password_set: true,
      token_set: false,
    });

    expect(e.cabecalhos).toEqual([
      { nome: "Authorization", valor: "", mantido: true },
      { nome: "", valor: "" },
    ]);
    expect(JSON.parse(e.mapeamento)).toEqual({ event: "$status" });
    expect(e.guardados).toEqual({ url: true, senha: true, token: false });
    expect(e.email.senha).toBe("");
  });
});

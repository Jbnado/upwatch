// Interface da sonda. Sem framework e sem build: a página é embarcada no
// binário e precisa funcionar do jeito que está.
"use strict";

const $ = (id) => document.getElementById(id);
let token = sessionStorage.getItem("upwatch-probe-token") || "";

function mostrarErro(msg) { $("erro").textContent = msg || ""; }

async function api(method, path, body) {
  const opts = { method, headers: { Authorization: "Bearer " + token } };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const resp = await fetch(path, opts);
  if (resp.status === 401) {
    sessionStorage.removeItem("upwatch-probe-token");
    token = "";
    $("painel").hidden = true;
    $("entrar").hidden = false;
    throw new Error("token de acesso inválido");
  }
  if (!resp.ok) {
    const data = await resp.json().catch(() => ({}));
    throw new Error(data.error || "erro " + resp.status);
  }
  return resp.status === 204 || resp.status === 201 ? null : resp.json();
}

function celula(texto, detalhe, classe) {
  const td = document.createElement("td");
  const span = document.createElement("span");
  span.textContent = texto;
  if (classe) span.className = classe;
  td.append(span);
  if (detalhe) {
    const small = document.createElement("small");
    small.textContent = detalhe;
    td.append(small);
  }
  return td;
}

function quando(iso) { return iso ? new Date(iso).toLocaleString() : ""; }

function linha(c, removivel) {
  const tr = document.createElement("tr");
  tr.append(celula(c.name, c.type ? c.type + " a cada " + c.interval : "a cada " + c.interval));
  tr.append(celula(c.target || "", c.summary || c.push_url));
  const r = c.result;
  tr.append(r ? celula(r.status, (r.msg ? r.msg + " · " : "") + r.latency_ms + "ms · " + quando(r.at), r.status)
              : celula("aguardando"));
  const d = c.delivery;
  tr.append(d ? celula(d.ok ? "enviado" : "falhou", (d.error ? d.error + " · " : "") + quando(d.at), d.ok ? "up" : "down")
              : celula("—"));
  const td = document.createElement("td");
  if (removivel) {
    const b = document.createElement("button");
    b.textContent = "Remover";
    b.addEventListener("click", async () => {
      if (!confirm("Remover " + c.name + "?")) return;
      try { await api("DELETE", "/api/checks/" + encodeURIComponent(c.name)); carregar(); }
      catch (e) { mostrarErro(e.message); }
    });
    td.append(b);
  }
  tr.append(td);
  return tr;
}

async function carregar() {
  try {
    const data = await api("GET", "/api/checks");
    mostrarErro("");
    $("entrar").hidden = true;
    $("painel").hidden = false;
    $("sonda").textContent = data.probe ? "· " + data.probe : "";
    const lista = $("lista");
    lista.replaceChildren();
    if (data.heartbeat) lista.append(linha(Object.assign({}, data.heartbeat, { name: "sinal de vida" }), false));
    for (const c of data.checks) lista.append(linha(c, true));
  } catch (e) {
    mostrarErro(e.message);
  }
}

$("form-token").addEventListener("submit", (ev) => {
  ev.preventDefault();
  token = $("token").value.trim();
  sessionStorage.setItem("upwatch-probe-token", token);
  carregar();
});

$("form-add").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = new FormData(ev.target);
  const body = {
    name: f.get("name"), type: f.get("type"), target: f.get("target"),
    interval: f.get("interval"), push_url: f.get("push_url"),
    assert: String(f.get("assert") || "").split("\n").map((s) => s.trim()).filter(Boolean),
    expect_status: String(f.get("expect_status") || "").split(",").map((s) => s.trim()).filter(Boolean).map(Number),
  };
  try { await api("POST", "/api/checks", body); ev.target.reset(); carregar(); }
  catch (e) { mostrarErro(e.message); }
});

if (token) carregar();
setInterval(() => { if (token) carregar(); }, 10000);

// Package web serve a interface embarcada no binário.
//
// Embarcar é o que permite distribuir uma imagem só, sem nginx ao lado e
// sem volume com arquivos estáticos que podem ficar defasados em relação
// ao servidor que os acompanha.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist recebe o build da interface.
//
// O prefixo all: faz o .gitkeep entrar no conjunto, de modo que o pacote
// compile mesmo antes de a interface ter sido construída — rodar os
// testes do servidor não deve exigir ter passado pelo pnpm.
//
//go:embed all:dist
var dist embed.FS

// BuildDir é onde o vite escreve, dentro do diretório embarcado.
//
// Ele é um nível abaixo de dist/ por um motivo específico: o vite apaga o
// diretório de saída inteiro antes de cada build. Com a saída em dist/, a
// âncora versionada que faz o go:embed casar era apagada junto, sumia do
// commit sem ninguém notar, e o clone limpo deixava de compilar — com uma
// mensagem que não diz o que fazer. Separando os dois, o vite manda em
// dist/app e o versionamento manda em dist/, sem se atropelarem.
//
// Exportado porque vite.config.ts precisa concordar com este valor, e um
// teste guarda essa concordância.
const BuildDir = "dist/app"

// assetsMaxAge é o cache dos arquivos versionados por hash. Como o nome
// muda a cada build, guardá-los por um ano é seguro e poupa a rede em
// instalações acessadas de fora.
const assetsMaxAge = "public, max-age=31536000, immutable"

// Handler devolve o servidor de arquivos da interface.
//
// Quando a interface não foi construída, responde com a instrução em vez
// de página em branco: descobrir que falta rodar o build olhando um fundo
// branco custa tempo demais.
func Handler() http.Handler {
	root, err := fs.Sub(dist, BuildDir)
	if err != nil {
		return notBuilt()
	}
	if _, err := fs.Stat(root, "index.html"); err != nil {
		return notBuilt()
	}

	files := http.FileServer(http.FS(root))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nome := strings.TrimPrefix(path.Clean(r.URL.Path), "/")

		// Arquivo inexistente devolve o index: as rotas da interface vivem
		// no navegador, e um recarregamento em /monitors/7 precisa
		// entregar a aplicação em vez de 404.
		if nome == "" || !exists(root, nome) {
			serveIndex(w, r, root)
			return
		}

		if strings.HasPrefix(nome, "assets/") {
			w.Header().Set("Cache-Control", assetsMaxAge)
		}
		files.ServeHTTP(w, r)
	})
}

func exists(root fs.FS, nome string) bool {
	info, err := fs.Stat(root, nome)
	return err == nil && !info.IsDir()
}

// serveIndex entrega a casca da aplicação.
//
// Sem cache: é o arquivo que aponta para os demais, e guardá-lo faria o
// navegador continuar pedindo arquivos de uma versão que já saiu do ar
// depois de uma atualização.
func serveIndex(w http.ResponseWriter, r *http.Request, root fs.FS) {
	conteudo, err := fs.ReadFile(root, "index.html")
	if err != nil {
		notBuilt().ServeHTTP(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(conteudo)
}

func notBuilt() http.Handler {
	const aviso = `<!doctype html>
<meta charset="utf-8">
<title>UpWatch</title>
<body style="font-family:system-ui;max-width:34rem;margin:4rem auto;padding:0 1rem;line-height:1.6">
<h1 style="font-size:1.1rem">A interface não foi construída</h1>
<p>Este binário foi compilado sem os arquivos da interface. A API continua
respondendo normalmente em <code>/api/v1</code>.</p>
<p>Para construir a interface, rode <code>make web</code> antes de
<code>make build</code>.</p>
`

	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(aviso))
	})
}

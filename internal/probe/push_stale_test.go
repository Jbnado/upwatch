package probe_test

import (
	"context"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/probe"
)

// blackholeProxy repassa TCP até freeze; daí em diante as conexões abertas
// engolem tudo sem fechar, como uma NAT ou firewall que esqueceu o fluxo.
// Conexões novas passam normalmente.
type blackholeProxy struct {
	ln     net.Listener
	target string
	mu     sync.Mutex
	frozen []*atomic.Bool
}

func newBlackholeProxy(t *testing.T, target string) *blackholeProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &blackholeProxy{ln: ln, target: target}
	t.Cleanup(func() { ln.Close() })
	go p.serve()
	return p
}

func (p *blackholeProxy) serve() {
	for {
		in, err := p.ln.Accept()
		if err != nil {
			return
		}
		out, err := net.Dial("tcp", p.target)
		if err != nil {
			in.Close()
			continue
		}
		frozen := new(atomic.Bool)
		p.mu.Lock()
		p.frozen = append(p.frozen, frozen)
		p.mu.Unlock()
		go pipe(out, in, frozen)
		go pipe(in, out, frozen)
	}
}

func pipe(dst io.WriteCloser, src io.Reader, frozen *atomic.Bool) {
	defer dst.Close()
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 && !frozen.Load() {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *blackholeProxy) freeze() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, f := range p.frozen {
		f.Store(true)
	}
}

// Uma conexão HTTP/2 que morre em silêncio no caminho não pode prender a
// sonda: depois de um envio que estoura o tempo, o seguinte tem de abrir
// conexão nova e chegar. Em 29/10 a sonda de RPA prod ficou 13 minutos
// calada assim, com a rede de pé, até alguém reiniciar o container.
func TestSenderRecoversFromSilentlyDeadConnection(t *testing.T) {
	var proto atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proto.Store(int32(r.ProtoMajor))
		w.WriteHeader(http.StatusOK)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)

	proxy := newBlackholeProxy(t, srv.Listener.Addr().String())
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	s := probe.NewSenderForTest("teste", roots, 500*time.Millisecond)
	url := "https://" + proxy.ln.Addr().String() + "/api/v1/push/" + token

	if err := s.Send(context.Background(), url, probe.Signal{Status: "up"}); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	if proto.Load() != 2 {
		t.Fatalf("server saw HTTP/%d; the test needs HTTP/2 to reproduce the stuck connection", proto.Load())
	}

	proxy.freeze()
	if err := s.Send(context.Background(), url, probe.Signal{Status: "up"}); err == nil {
		t.Fatal("Send over the dead connection succeeded; the proxy did not freeze it")
	}

	if err := s.Send(context.Background(), url, probe.Signal{Status: "up"}); err != nil {
		t.Fatalf("Send after the timeout still fails: %v; the dead connection stayed in the pool", err)
	}
}

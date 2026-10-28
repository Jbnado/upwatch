package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Jbnado/upwatch/internal/clock"
	"github.com/Jbnado/upwatch/internal/probe"
)

func (a *app) cmdRun(ctx context.Context, cfgPath string, args []string) error {
	fs := a.newFlags("run", "[--ui 127.0.0.1:8090] [--dry-run]")
	uiAddr := fs.String("ui", "", "liga a interface web neste endereço (desligada por padrão)")
	uiAny := fs.Bool("ui-listen-any", false, "permite a interface fora do loopback")
	dryRun := fs.Bool("dry-run", false, "imprime cada envio em vez de fazê-lo")
	if _, err := parse(fs, args); err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(a.stderr, nil))
	send := probe.NewSender(a.version).Send
	if *dryRun {
		send = a.dryRunSend()
	}

	d := probe.NewDaemon(probe.DaemonOptions{
		Path: cfgPath, Clock: clock.Real(), Send: send, Backoff: probe.DefaultBackoff,
		Registry: probe.NewRegistry(), Logger: logger,
	})

	var ui *http.Server
	if *uiAddr != "" {
		srv, err := a.startUI(cfgPath, *uiAddr, *uiAny, d, logger)
		if err != nil {
			return err
		}
		ui = srv
	}

	logger.Info("upwatch-probe no ar", "versao", a.version, "configuracao", cfgPath,
		"dry_run", *dryRun, "interface", *uiAddr)

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)

	err := d.Run(ctx, hup)
	if ui != nil {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = ui.Shutdown(shutdown)
	}
	logger.Info("upwatch-probe encerrada")
	return err
}

// dryRunSend imprime o envio em vez de fazê-lo: o corpo exato, byte a
// byte, e o endereço com o token escondido.
func (a *app) dryRunSend() probe.SendFunc {
	var mu sync.Mutex
	return func(_ context.Context, pushURL string, sig probe.Signal) error {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(a.stdout, "dry-run POST %s %s\n", probe.MaskURL(pushURL), sig.Body())
		return nil
	}
}

func (a *app) startUI(cfgPath, addr string, listenAny bool, d *probe.Daemon, logger *slog.Logger) (*http.Server, error) {
	if err := probe.ValidateUIListen(addr, listenAny); err != nil {
		return nil, err
	}
	cfg, err := probe.Load(cfgPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if cfg.UIToken == "" {
		return nil, errors.New("a interface exige um token e não há nenhum; gere com upwatch-probe ui-token")
	}

	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("interface: %w", err)
	}
	srv := &http.Server{
		Handler:           probe.NewUI(cfgPath, d),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("interface parou", "erro", err)
		}
	}()
	return srv, nil
}

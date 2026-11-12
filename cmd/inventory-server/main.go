package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/reflection"

	pb "libratrack/gen/inventory"
	"libratrack/internal/auth"
	"libratrack/internal/middleware"
	"libratrack/internal/server"
	"libratrack/internal/store"
	"libratrack/internal/tlsconf"
)

func main() {
	addr := flag.String("addr", ":50051", "listen address")
	useTLS := flag.Bool("tls", false, "enable mTLS (needs certs from go run ./cmd/gencerts)")
	certDir := flag.String("cert-dir", "certs", "certificate directory")
	useAuth := flag.Bool("auth", false, "require JWT in metadata")
	secret := flag.String("jwt-secret", envOr("JWT_SECRET", auth.DefaultDemoSecret), "HMAC secret for JWT")
	simulate := flag.Bool("simulate", false, "simulate status changes")
	simEvery := flag.Duration("sim-interval", 2*time.Second, "simulation interval")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// interceptor chain: recovery -> logging -> auth
	unary := []grpc.UnaryServerInterceptor{middleware.UnaryRecovery(log), middleware.UnaryLogging(log)}
	stream := []grpc.StreamServerInterceptor{middleware.StreamRecovery(log), middleware.StreamLogging(log)}
	if *useAuth {
		a := auth.NewAuthenticator([]byte(*secret), auth.DefaultPolicy())
		unary = append(unary, a.Unary())
		stream = append(stream, a.Stream())
	}

	opts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(unary...),
		grpc.ChainStreamInterceptor(stream...),
	}

	if *useTLS {
		cfg, err := tlsconf.ServerConfig(*certDir)
		if err != nil {
			log.Error("tls config", "err", err)
			os.Exit(1)
		}
		opts = append(opts, grpc.Creds(credentials.NewTLS(cfg)))
	}

	st := store.New()
	srv := grpc.NewServer(opts...)
	pb.RegisterInventoryServiceServer(srv, server.New(st, log))
	reflection.Register(srv)

	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Error("listen", "addr", *addr, "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *simulate {
		go st.Simulate(ctx, *simEvery)
	}

	log.Info("inventory server started",
		"addr", lis.Addr().String(), "mtls", *useTLS, "auth", *useAuth, "simulate", *simulate)
	if !*useTLS {
		log.Warn("TLS disabled — plaintext only for local dev")
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(lis) }()

	select {
	case err := <-serveErr:
		log.Error("serve", "err", err)
		os.Exit(1)
	case <-ctx.Done():
	}

	log.Info("shutting down...")
	done := make(chan struct{})
	go func() { srv.GracefulStop(); close(done) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		log.Warn("graceful stop timed out, forcing")
		srv.Stop()
	}
	log.Info("bye")
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

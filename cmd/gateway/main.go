package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"

	pb "libratrack/gen/inventory"
	"libratrack/internal/tlsconf"
)

func main() {
	httpAddr := flag.String("http-addr", ":8080", "REST gateway listen address")
	grpcAddr := flag.String("grpc-addr", "localhost:50051", "upstream gRPC server address")
	useTLS := flag.Bool("tls", false, "connect to gRPC server via mTLS")
	certDir := flag.String("cert-dir", "certs", "certificate directory")
	clientCert := flag.String("client-cert", "client", "client certificate name for gateway")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	var creds credentials.TransportCredentials = insecure.NewCredentials()
	if *useTLS {
		cfg, err := tlsconf.ClientConfig(*certDir, *clientCert)
		if err != nil {
			log.Error("tls config", "err", err)
			os.Exit(1)
		}
		creds = credentials.NewTLS(cfg)
	}

	conn, err := grpc.NewClient(*grpcAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		log.Error("grpc client", "err", err)
		os.Exit(1)
	}
	defer conn.Close()

	mux := runtime.NewServeMux(
		runtime.WithMarshalerOption(runtime.MIMEWildcard, &runtime.JSONPb{
			MarshalOptions:   protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true},
			UnmarshalOptions: protojson.UnmarshalOptions{DiscardUnknown: true},
		}),
	)

	if err := pb.RegisterInventoryServiceHandler(context.Background(), mux, conn); err != nil {
		log.Error("register handler", "err", err)
		os.Exit(1)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Info("rest request", "method", r.Method, "path", r.URL.RequestURI())
		mux.ServeHTTP(w, r)
	})
	srv := &http.Server{Addr: *httpAddr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			_ = srv.Close()
		}
	}()

	log.Info("gateway started", "http", *httpAddr, "upstream_grpc", *grpcAddr, "mtls", *useTLS)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("http server", "err", err)
		os.Exit(1)
	}
	log.Info("bye")
}

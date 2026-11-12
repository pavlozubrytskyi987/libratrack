package bench

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	pb "libratrack/gen/inventory"
	"libratrack/internal/server"
	"libratrack/internal/store"
)

type restCopy struct {
	ID              string `json:"id"`
	BookID          string `json:"book_id"`
	InventoryNumber string `json:"inventory_number"`
	Status          string `json:"status"`
}

type env struct {
	grpcClient pb.InventoryServiceClient
	restURL    string
	gwURL      string
	httpClient *http.Client
}

func setup(tb testing.TB) *env {
	tb.Helper()
	st := store.New()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	grpcSrv := grpc.NewServer()
	pb.RegisterInventoryServiceServer(grpcSrv, server.New(st, slog.New(slog.NewTextHandler(io.Discard, nil))))
	go func() { _ = grpcSrv.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		tb.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /copies/{id}", func(w http.ResponseWriter, r *http.Request) {
		c, err := st.Get(r.PathValue("id"))
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(restCopy{
			ID: c.Id, BookID: c.BookId, InventoryNumber: c.InventoryNumber, Status: c.Status.String(),
		})
	})
	restSrv := httptest.NewServer(mux)

	gwMux := runtime.NewServeMux(runtime.WithMarshalerOption(runtime.MIMEWildcard, &runtime.JSONPb{
		MarshalOptions: protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true},
	}))
	if err := pb.RegisterInventoryServiceHandler(context.Background(), gwMux, conn); err != nil {
		tb.Fatal(err)
	}
	gwSrv := httptest.NewServer(gwMux)

	tb.Cleanup(func() {
		gwSrv.Close()
		restSrv.Close()
		conn.Close()
		grpcSrv.Stop()
	})

	return &env{
		grpcClient: pb.NewInventoryServiceClient(conn),
		restURL:    restSrv.URL + "/copies/copy-1",
		gwURL:      gwSrv.URL + "/v1/copies/copy-1",
		httpClient: &http.Client{Transport: &http.Transport{
			MaxIdleConns:        512,
			MaxIdleConnsPerHost: 512,
		}},
	}
}

func (e *env) httpGet(tb testing.TB, url string) {
	resp, err := e.httpClient.Get(url)
	if err != nil {
		tb.Error(err)
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		tb.Errorf("HTTP %d", resp.StatusCode)
	}
}

// --- Sequential ---

func BenchmarkGetCopy_gRPC(b *testing.B) {
	e := setup(b)
	ctx := context.Background()
	req := &pb.GetCopyRequest{Id: "copy-1"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := e.grpcClient.GetCopy(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGetCopy_REST(b *testing.B) {
	e := setup(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.httpGet(b, e.restURL)
	}
}

func BenchmarkGetCopy_RESTviaGateway(b *testing.B) {
	e := setup(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.httpGet(b, e.gwURL)
	}
}

// --- Parallel ---

func BenchmarkGetCopyParallel_gRPC(b *testing.B) {
	e := setup(b)
	ctx := context.Background()
	req := &pb.GetCopyRequest{Id: "copy-1"}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb_ *testing.PB) {
		for pb_.Next() {
			if _, err := e.grpcClient.GetCopy(ctx, req); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

func BenchmarkGetCopyParallel_REST(b *testing.B) {
	e := setup(b)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb_ *testing.PB) {
		for pb_.Next() {
			e.httpGet(b, e.restURL)
		}
	})
}

// --- Serialization only (no network) ---

func BenchmarkSerialize_Protobuf(b *testing.B) {
	c := &pb.Copy{Id: "copy-1", BookId: "book-1", InventoryNumber: "INV-0001", Status: pb.Copy_AVAILABLE}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		data, err := proto.Marshal(c)
		if err != nil {
			b.Fatal(err)
		}
		var out pb.Copy
		if err := proto.Unmarshal(data, &out); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSerialize_JSON(b *testing.B) {
	c := restCopy{ID: "copy-1", BookID: "book-1", InventoryNumber: "INV-0001", Status: "AVAILABLE"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		data, err := json.Marshal(c)
		if err != nil {
			b.Fatal(err)
		}
		var out restCopy
		if err := json.Unmarshal(data, &out); err != nil {
			b.Fatal(err)
		}
	}
}

// --- Payload size comparison ---

func TestPayloadSize(t *testing.T) {
	c := &pb.Copy{Id: "copy-1", BookId: "book-1", InventoryNumber: "INV-0001", Status: pb.Copy_AVAILABLE}

	protoBytes, err := proto.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	jsonBytes, err := json.Marshal(restCopy{
		ID: c.Id, BookID: c.BookId, InventoryNumber: c.InventoryNumber, Status: c.Status.String(),
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("Protobuf: %3d bytes", len(protoBytes))
	t.Logf("JSON:     %3d bytes", len(jsonBytes))
	t.Logf("JSON is %.1fx larger", float64(len(jsonBytes))/float64(len(protoBytes)))

	if len(protoBytes) >= len(jsonBytes) {
		t.Fatalf("expected Protobuf to be smaller than JSON")
	}
}

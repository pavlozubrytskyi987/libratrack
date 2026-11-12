package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	pb "libratrack/gen/inventory"
	"libratrack/internal/auth"
	"libratrack/internal/tlsconf"
)

var (
	addr       = flag.String("addr", "localhost:50051", "gRPC server address")
	useTLS     = flag.Bool("tls", false, "connect via mTLS")
	certDir    = flag.String("cert-dir", "certs", "certificate directory")
	clientCert = flag.String("client-cert", "client", "client cert name: client | rogue-client | none")
	token      = flag.String("token", os.Getenv("INVENTORY_TOKEN"), "JWT token")
	timeout    = flag.Duration("timeout", 5*time.Second, "timeout for unary calls")
	maxUpdates = flag.Int("max", 0, "watch: stop after N updates (0 = unlimited)")
)

func main() {
	flag.Usage = usage
	flag.Parse()
	os.Exit(run(flag.Args()))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}

	conn, err := dial()
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		return 1
	}
	defer conn.Close()
	client := pb.NewInventoryServiceClient(conn)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "get":
		err = needArgs(rest, 1, "get <id>")
		if err == nil {
			err = cmdGet(ctx, client, rest[0])
		}
	case "register":
		err = needArgs(rest, 2, "register <book_id> <inventory_number>")
		if err == nil {
			err = cmdRegister(ctx, client, rest[0], rest[1])
		}
	case "set-status":
		err = needArgs(rest, 2, "set-status <id> <AVAILABLE|BORROWED|LOST>")
		if err == nil {
			err = cmdSetStatus(ctx, client, rest[0], rest[1])
		}
	case "watch":
		err = needArgs(rest, 1, "watch <id>")
		if err == nil {
			err = cmdWatch(ctx, client, rest[0])
		}
	case "bulk":
		err = needArgs(rest, 2, "bulk <book_id> <count>")
		if err == nil {
			err = cmdBulk(ctx, client, rest[0], rest[1])
		}
	case "sync":
		err = cmdSync(ctx, client)
	default:
		usage()
		return 2
	}

	if err != nil {
		printRPCError(err)
		return 1
	}
	return 0
}

// --- Unary ---

func cmdGet(ctx context.Context, c pb.InventoryServiceClient, id string) error {
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	cp, err := c.GetCopy(ctx, &pb.GetCopyRequest{Id: id})
	if err != nil {
		return err
	}
	printMsg(cp)
	return nil
}

func cmdRegister(ctx context.Context, c pb.InventoryServiceClient, bookID, inv string) error {
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	cp, err := c.RegisterCopy(ctx, &pb.RegisterCopyRequest{BookId: bookID, InventoryNumber: inv})
	if err != nil {
		return err
	}
	printMsg(cp)
	return nil
}

func cmdSetStatus(ctx context.Context, c pb.InventoryServiceClient, id, statusName string) error {
	v, ok := pb.Copy_Status_value[strings.ToUpper(statusName)]
	if !ok {
		return fmt.Errorf("unknown status %q (AVAILABLE, BORROWED, LOST)", statusName)
	}

	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	cp, err := c.UpdateCopyStatus(ctx, &pb.UpdateCopyStatusRequest{Id: id, Status: pb.Copy_Status(v)})
	if err != nil {
		return err
	}
	printMsg(cp)
	return nil
}

// --- Server Streaming ---

func cmdWatch(ctx context.Context, c pb.InventoryServiceClient, id string) error {
	stream, err := c.WatchCopyStatus(ctx, &pb.WatchRequest{Id: id, MaxUpdates: int32(*maxUpdates)})
	if err != nil {
		return err
	}

	fmt.Printf("watching %s (Ctrl+C to quit)...\n", id)
	for {
		update, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			fmt.Println("stream ended by server (io.EOF)")
			return nil
		}
		if err != nil {
			if status.Code(err) == codes.Canceled {
				fmt.Println("stream cancelled by client (Ctrl+C)")
				return nil
			}
			return err
		}
		fmt.Printf("%s  %s -> %s\n",
			update.GetChangedAt().AsTime().Local().Format("15:04:05.000"),
			update.GetCopyId(), update.GetStatus())
	}
}

// --- Client Streaming ---

func cmdBulk(ctx context.Context, c pb.InventoryServiceClient, bookID, countStr string) error {
	var n int
	if _, err := fmt.Sscanf(countStr, "%d", &n); err != nil || n <= 0 {
		return fmt.Errorf("count must be a positive number, got %q", countStr)
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	stream, err := c.BulkRegisterCopies(ctx)
	if err != nil {
		return err
	}

	for i := 1; i <= n; i++ {
		req := &pb.RegisterCopyRequest{
			BookId:          bookID,
			InventoryNumber: fmt.Sprintf("BULK-%s-%03d", bookID, i),
		}
		if err := stream.Send(req); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
	}

	summary, err := stream.CloseAndRecv()
	if err != nil {
		return err
	}
	printMsg(summary)
	return nil
}

// --- Bidirectional Streaming ---

func cmdSync(ctx context.Context, c pb.InventoryServiceClient) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	stream, err := c.SyncInventory(ctx)
	if err != nil {
		return err
	}

	events := []*pb.SyncEvent{
		{BranchId: "branch-A", CopyId: "copy-1", Status: pb.Copy_BORROWED, Sequence: 1},
		{BranchId: "branch-A", CopyId: "copy-1", Status: pb.Copy_AVAILABLE, Sequence: 2},
		{BranchId: "branch-A", CopyId: "copy-999", Status: pb.Copy_BORROWED, Sequence: 3},
	}

	sendErr := make(chan error, 1)
	go func() {
		for _, ev := range events {
			if err := stream.Send(ev); err != nil {
				sendErr <- err
				return
			}
			fmt.Printf("-> sent seq=%d copy=%s status=%s\n", ev.Sequence, ev.CopyId, ev.Status)
			time.Sleep(300 * time.Millisecond)
		}
		sendErr <- stream.CloseSend()
	}()

	for {
		ack, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		fmt.Printf("<- ack   seq=%d copy=%s applied=%v note=%q\n",
			ack.GetSequence(), ack.GetCopyId(), ack.GetApplied(), ack.GetNote())
	}
	return <-sendErr
}

// --- connection & helpers ---

func dial() (*grpc.ClientConn, error) {
	var opts []grpc.DialOption

	if *useTLS {
		cfg, err := tlsconf.ClientConfig(*certDir, *clientCert)
		if err != nil {
			return nil, err
		}
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	if *token != "" {
		opts = append(opts, grpc.WithPerRPCCredentials(auth.NewPerRPCToken(*token, *useTLS)))
	}

	return grpc.NewClient(*addr, opts...)
}

func needArgs(args []string, n int, usageLine string) error {
	if len(args) != n {
		return fmt.Errorf("usage: inventory-client %s", usageLine)
	}
	return nil
}

func printMsg(m proto.Message) {
	fmt.Println(protojson.MarshalOptions{Multiline: true, UseProtoNames: true, EmitUnpopulated: true}.Format(m))
}

func printRPCError(err error) {
	if st, ok := status.FromError(err); ok {
		fmt.Fprintf(os.Stderr, "RPC error: code=%s message=%q\n", st.Code(), st.Message())
		return
	}
	fmt.Fprintln(os.Stderr, "error:", err)
}

func usage() {
	fmt.Fprint(os.Stderr, `inventory-client [flags] <command> [args]

Commands:
  get <id>                              unary: get a copy
  register <book_id> <inventory_number> unary: register a copy
  set-status <id> <STATUS>              unary: change status (AVAILABLE|BORROWED|LOST)
  watch <id>                            server streaming: live status updates
  bulk <book_id> <count>                client streaming: bulk register
  sync                                  bidi streaming: branch sync

Flags:
`)
	flag.PrintDefaults()
}

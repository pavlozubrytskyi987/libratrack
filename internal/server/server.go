package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "libratrack/gen/inventory"
	"libratrack/internal/store"
)

type Server struct {
	pb.UnimplementedInventoryServiceServer
	store *store.Store
	log   *slog.Logger
}

func New(st *store.Store, log *slog.Logger) *Server {
	return &Server{store: st, log: log}
}

// --- Unary RPCs ---

func (s *Server) GetCopy(_ context.Context, req *pb.GetCopyRequest) (*pb.Copy, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	c, err := s.store.Get(req.GetId())
	if err != nil {
		return nil, toStatus(err, req.GetId())
	}
	return c, nil
}

func (s *Server) RegisterCopy(_ context.Context, req *pb.RegisterCopyRequest) (*pb.Copy, error) {
	return s.register(req)
}

func (s *Server) UpdateCopyStatus(_ context.Context, req *pb.UpdateCopyStatusRequest) (*pb.Copy, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	if !validStatus(req.GetStatus()) {
		return nil, status.Errorf(codes.InvalidArgument,
			"status must be AVAILABLE, BORROWED or LOST (got %s)", req.GetStatus())
	}

	c, err := s.store.SetStatus(req.GetId(), req.GetStatus())
	if err != nil {
		return nil, toStatus(err, req.GetId())
	}
	return c, nil
}

// --- Server Streaming (1 request -> N responses) ---

func (s *Server) WatchCopyStatus(req *pb.WatchRequest, stream pb.InventoryService_WatchCopyStatusServer) error {
	ctx := stream.Context()
	id := req.GetId()
	if id == "" {
		return status.Error(codes.InvalidArgument, "id is required")
	}

	// subscribe before reading snapshot so we don't miss changes in between
	changes, cancel := s.store.Subscribe(id)
	defer cancel()

	current, err := s.store.Get(id)
	if err != nil {
		return toStatus(err, id)
	}

	var sent int32
	send := func(st pb.Copy_Status, at time.Time) error {
		if err := stream.Send(&pb.StatusUpdate{
			CopyId:    id,
			Status:    st,
			ChangedAt: timestamppb.New(at),
		}); err != nil {
			return err
		}
		sent++
		return nil
	}
	done := func() bool { return req.GetMaxUpdates() > 0 && sent >= req.GetMaxUpdates() }

	if err := send(current.GetStatus(), time.Now()); err != nil {
		return err
	}
	if done() {
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		case ch, ok := <-changes:
			if !ok {
				return nil
			}
			if err := send(ch.Status, ch.At); err != nil {
				return err
			}
			if done() {
				return nil
			}
		}
	}
}

// --- Client Streaming (N requests -> 1 response) ---

func (s *Server) BulkRegisterCopies(stream pb.InventoryService_BulkRegisterCopiesServer) error {
	sum := &pb.BulkRegisterSummary{}

	for {
		req, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			s.log.Info("bulk register done", "registered", sum.Registered, "failed", sum.Failed)
			return stream.SendAndClose(sum)
		}
		if err != nil {
			return err
		}

		c, err := s.register(req)
		if err != nil {
			sum.Failed++
			s.log.Warn("bulk register: item rejected",
				"inventory_number", req.GetInventoryNumber(), "err", err)
			continue
		}
		sum.Registered++
		sum.CopyIds = append(sum.CopyIds, c.GetId())
	}
}

// --- Bidirectional Streaming (N <-> N) ---

func (s *Server) SyncInventory(stream pb.InventoryService_SyncInventoryServer) error {
	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := stream.Send(s.applyEvent(event)); err != nil {
			return err
		}
	}
}

func (s *Server) applyEvent(ev *pb.SyncEvent) *pb.SyncEvent {
	ack := &pb.SyncEvent{
		BranchId: ev.GetBranchId(),
		CopyId:   ev.GetCopyId(),
		Status:   ev.GetStatus(),
		Sequence: ev.GetSequence(),
	}

	if !validStatus(ev.GetStatus()) {
		ack.Note = "rejected: invalid status"
		return ack
	}
	if _, err := s.store.SetStatus(ev.GetCopyId(), ev.GetStatus()); err != nil {
		ack.Note = "rejected: " + err.Error()
		return ack
	}

	ack.Applied = true
	ack.Note = "applied"
	return ack
}

func (s *Server) register(req *pb.RegisterCopyRequest) (*pb.Copy, error) {
	if req.GetBookId() == "" || req.GetInventoryNumber() == "" {
		return nil, status.Error(codes.InvalidArgument, "book_id and inventory_number are required")
	}
	c, err := s.store.Create(req.GetBookId(), req.GetInventoryNumber())
	if err != nil {
		return nil, toStatus(err, req.GetInventoryNumber())
	}
	return c, nil
}

func validStatus(st pb.Copy_Status) bool {
	switch st {
	case pb.Copy_AVAILABLE, pb.Copy_BORROWED, pb.Copy_LOST:
		return true
	}
	return false
}

func toStatus(err error, subject string) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return status.Errorf(codes.NotFound, "copy %s not found", subject)
	case errors.Is(err, store.ErrDuplicateNumber):
		return status.Errorf(codes.AlreadyExists, "inventory number %s already registered", subject)
	default:
		return status.Errorf(codes.Internal, "internal error")
	}
}

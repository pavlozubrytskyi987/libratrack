package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	pb "libratrack/gen/inventory"
)

var (
	ErrNotFound        = errors.New("copy not found")
	ErrDuplicateNumber = errors.New("inventory number already registered")
)

type Change struct {
	CopyID string
	Status pb.Copy_Status
	At     time.Time
}

type Store struct {
	mu     sync.RWMutex
	copies map[string]*pb.Copy
	byInv  map[string]string // inventory_number -> id
	nextID int
	subs   map[string]map[chan Change]struct{}
}

func New() *Store {
	s := &Store{
		copies: make(map[string]*pb.Copy),
		byInv:  make(map[string]string),
		nextID: 1,
		subs:   make(map[string]map[chan Change]struct{}),
	}

	// seed some test data
	_, _ = s.Create("book-1", "INV-0001")
	_, _ = s.Create("book-1", "INV-0002")
	_, _ = s.Create("book-2", "INV-0003")
	_, _ = s.SetStatus("copy-2", pb.Copy_BORROWED)
	_, _ = s.SetStatus("copy-3", pb.Copy_LOST)
	return s
}

func (s *Store) Get(id string) (*pb.Copy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	c, ok := s.copies[id]
	if !ok {
		return nil, ErrNotFound
	}
	return proto.Clone(c).(*pb.Copy), nil
}

func (s *Store) Create(bookID, inventoryNumber string) (*pb.Copy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, dup := s.byInv[inventoryNumber]; dup {
		return nil, fmt.Errorf("%w: %s", ErrDuplicateNumber, inventoryNumber)
	}

	id := fmt.Sprintf("copy-%d", s.nextID)
	s.nextID++

	c := &pb.Copy{
		Id:              id,
		BookId:          bookID,
		InventoryNumber: inventoryNumber,
		Status:          pb.Copy_AVAILABLE,
	}
	s.copies[id] = c
	s.byInv[inventoryNumber] = id

	return proto.Clone(c).(*pb.Copy), nil
}

func (s *Store) SetStatus(id string, st pb.Copy_Status) (*pb.Copy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.copies[id]
	if !ok {
		return nil, ErrNotFound
	}

	if c.Status != st {
		c.Status = st
		s.publishLocked(Change{CopyID: id, Status: st, At: time.Now()})
	}
	return proto.Clone(c).(*pb.Copy), nil
}

func (s *Store) Subscribe(id string) (<-chan Change, func()) {
	ch := make(chan Change, 16)

	s.mu.Lock()
	if s.subs[id] == nil {
		s.subs[id] = make(map[chan Change]struct{})
	}
	s.subs[id][ch] = struct{}{}
	s.mu.Unlock()

	cancel := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if set, ok := s.subs[id]; ok {
			if _, exists := set[ch]; exists {
				delete(set, ch)
				close(ch)
				if len(set) == 0 {
					delete(s.subs, id)
				}
			}
		}
	}
	return ch, cancel
}

func (s *Store) SubscriberCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, set := range s.subs {
		n += len(set)
	}
	return n
}

func (s *Store) publishLocked(ch Change) {
	for sub := range s.subs[ch.CopyID] {
		select {
		case sub <- ch:
		default:
		}
	}
}

// Simulate toggles statuses on a timer to generate live events for WatchCopyStatus.
func (s *Store) Simulate(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.mu.RLock()
			ids := make([]string, 0, len(s.copies))
			for id := range s.copies {
				ids = append(ids, id)
			}
			s.mu.RUnlock()

			sort.Strings(ids)
			for _, id := range ids {
				cur, err := s.Get(id)
				if err != nil {
					continue
				}
				next := pb.Copy_BORROWED
				if cur.Status == pb.Copy_BORROWED || cur.Status == pb.Copy_LOST {
					next = pb.Copy_AVAILABLE
				}
				_, _ = s.SetStatus(id, next)
			}
		}
	}
}

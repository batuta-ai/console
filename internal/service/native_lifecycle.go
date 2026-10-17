package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"

	"github.com/batuta-ai/console/internal/domain"
)

type nativeExecutions struct {
	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	closing   bool
	jobs      map[string]*nativeExecution
	active    sync.WaitGroup
	drained   chan struct{}
	closeOnce sync.Once
	drainErr  error
	completed []domain.ExecutionStopResult
}

type nativeExecution struct {
	request    domain.ExecutionRequest
	runID      string
	outcomeMu  sync.Mutex
	coreState  string
	runErr     error
	cleanupErr error
	mission    string
	workspace  string
	delivery   string
	ctx        context.Context
	cancel     context.CancelFunc
	ready      chan nativeStartOutcome
	readyOnce  sync.Once
}

type nativeStartOutcome struct {
	result domain.ExecutionResult
	err    error
}

func newNativeExecutions() *nativeExecutions {
	ctx, cancel := context.WithCancel(context.Background())
	return &nativeExecutions{ctx: ctx, cancel: cancel, jobs: map[string]*nativeExecution{}, drained: make(chan struct{})}
}

func (n *nativeExecutions) admit(mission, workspace, delivery string) (*nativeExecution, error) {
	return n.admitOwned(mission, workspace, delivery, domain.ExecutionRequest{})
}

func (n *nativeExecutions) admitExecution(req domain.ExecutionRequest, delivery string) (*nativeExecution, error) {
	return n.admitOwned(req.Target.MissionID, req.Target.Binding.CanonicalRoot, delivery, req)
}

func (n *nativeExecutions) admitOwned(mission, workspace, delivery string, req domain.ExecutionRequest) (*nativeExecution, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closing {
		return nil, domain.ErrExecutionClosing
	}
	for _, job := range n.jobs {
		if job.mission == mission || job.workspace == workspace {
			return nil, domain.ErrExecutionExists
		}
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(n.ctx)
	job := &nativeExecution{request: req, runID: hex.EncodeToString(nonce[:]), mission: mission, workspace: workspace, delivery: delivery, ctx: ctx, cancel: cancel, ready: make(chan nativeStartOutcome, 1)}
	n.jobs[delivery] = job
	n.active.Add(1)
	return job, nil
}

func (n *nativeExecutions) finish(job *nativeExecution) {
	job.cancel()
	n.mu.Lock()
	if n.jobs[job.delivery] == job {
		delete(n.jobs, job.delivery)
	}
	n.mu.Unlock()
	n.active.Done()
}

func (job *nativeExecution) signal(result domain.ExecutionResult, err error) {
	if result.Effect != domain.ExecutionRejected {
		result.RunID = job.runID
	}
	job.readyOnce.Do(func() { job.ready <- nativeStartOutcome{result, err} })
}

// ShutdownNative closes admission and joins every admitted prepare/run/finalize
// operation before callers may close the catalog. HTTP lifetime does not own Run.
func (s *Service) ShutdownNative(ctx context.Context) error {
	if s.native == nil {
		return nil
	}
	n := s.native
	n.closeOnce.Do(func() {
		n.mu.Lock()
		n.closing = true
		n.cancel()
		n.mu.Unlock()
		go func() { n.active.Wait(); close(n.drained) }()
	})
	select {
	case <-n.drained:
		n.mu.Lock()
		defer n.mu.Unlock()
		return n.drainErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) recordNativeCleanup(err error) {
	if err == nil || s.native == nil {
		return
	}
	s.native.mu.Lock()
	defer s.native.mu.Unlock()
	if s.native.drainErr == nil {
		s.native.drainErr = err
	}
}

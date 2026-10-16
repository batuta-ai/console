package session

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/batuta-ai/console/internal/domain"
)

type stopFailure struct {
	snapshot  domain.Session
	err       error
	retryable bool
}

// StopPersistenceError distinguishes failed catalog work from a definitive
// missing/unowned result. It preserves the underlying transaction error.
type StopPersistenceError struct{ Cause error }

func (e *StopPersistenceError) Error() string { return e.Cause.Error() }
func (e *StopPersistenceError) Unwrap() error { return e.Cause }

type stopFinalization struct {
	live        *live
	nativeReady chan struct{}
	done        chan struct{}
	mu          sync.Mutex
	snapshot    domain.Session
	err         error
}

// StopAndFinalize joins a single stop and its required catalog finalization.
// Admission shares Shutdown's lock and wait group, so cancellation cannot let
// the owner close callback resources while the finalizer is still using them.
// finalize must not call Stop or Shutdown on this manager.
func (m *Manager) StopAndFinalize(ctx context.Context, id string, finalize func(string, domain.Session, error) (domain.Session, error)) (domain.Session, error) {
	m.mu.Lock()
	if job := m.stops[id]; job != nil {
		m.mu.Unlock()
		return job.wait(ctx, id)
	}
	failed, hasFailure := m.stopFailures[id]
	if hasFailure && (!failed.retryable || context.Cause(ctx) != nil || m.draining) {
		m.mu.Unlock()
		return failed.snapshot, failed.err
	}
	l := m.live[id]
	if err := context.Cause(ctx); err != nil {
		m.mu.Unlock()
		if l != nil {
			return l.stopContext(ctx)
		}
		return domain.Session{}, err
	}
	if m.draining {
		m.mu.Unlock()
		return domain.Session{}, ErrProbeClosing
	}
	job := &stopFinalization{live: l, nativeReady: make(chan struct{}), done: make(chan struct{})}
	job.snapshot, job.err = failed.snapshot, failed.err
	if m.stops == nil {
		m.stops = make(map[string]*stopFinalization)
	}
	m.stops[id] = job
	m.active.Add(1)
	m.mu.Unlock()
	go m.finalizeStop(id, job, finalize)
	return job.wait(ctx, id)
}

func (m *Manager) finalizeStop(id string, job *stopFinalization, finalize func(string, domain.Session, error) (domain.Session, error)) {
	defer m.active.Done()
	var snapshot domain.Session
	stopErr := error(os.ErrNotExist)
	var complete bool
	if job.live != nil {
		stopErr, complete = job.live.stopNative()
		snapshot, stopErr = job.live.stopResult(stopErr)
		job.mu.Lock()
		job.snapshot, job.err = snapshot, stopErr
		job.mu.Unlock()
		close(job.nativeReady)
		if complete {
			snapshot, stopErr = job.live.awaitFinalization(context.Background(), stopErr)
		}
	} else {
		close(job.nativeReady)
	}
	snapshot, err := finalize(id, snapshot, stopErr)
	job.mu.Lock()
	job.snapshot, job.err = snapshot, err
	job.mu.Unlock()
	m.mu.Lock()
	var persistenceErr *StopPersistenceError
	persistenceFailed := errors.As(err, &persistenceErr)
	if m.draining && (stopErr == nil || persistenceFailed) {
		m.shutdownErr = errors.Join(m.shutdownErr, err)
	}
	if err != nil && job.live != nil {
		if complete {
			m.rememberStopFailure(snapshot, err)
		}
	} else {
		if persistenceFailed {
			if m.stopFailures == nil {
				m.stopFailures = make(map[string]stopFailure)
			}
			m.stopFailures[id] = stopFailure{snapshot: snapshot, err: err, retryable: true}
		} else {
			delete(m.stopFailures, id)
		}
	}
	delete(m.stops, id)
	close(job.done)
	m.mu.Unlock()
}

// Successful completions leave no history. Only unresolved failures retain a
// snapshot/error: discarding them would let a retry mistake an exited row for
// durable success. No process handles or running jobs are retained here.
func (m *Manager) rememberStopFailure(snapshot domain.Session, err error) {
	if m.stopFailures == nil {
		m.stopFailures = make(map[string]stopFailure)
	}
	if prior, ok := m.stopFailures[snapshot.ID]; ok {
		err = errors.Join(prior.err, err)
	}
	m.stopFailures[snapshot.ID] = stopFailure{snapshot: snapshot, err: err}
}

func (job *stopFinalization) wait(ctx context.Context, id string) (domain.Session, error) {
	// Native cleanup always keeps its own budgets, even for canceled joiners.
	<-job.nativeReady
	select {
	case <-job.done:
		return job.result()
	case <-ctx.Done():
		select {
		case <-job.done:
			return job.result()
		default:
		}
		snapshot, err := job.result()
		if job.live != nil {
			snapshot, err = job.live.stopResult(err)
		}
		return snapshot, errors.Join(err, &FinalizationPendingError{SessionID: id, Cause: context.Cause(ctx)})
	}
}

func (job *stopFinalization) result() (domain.Session, error) {
	job.mu.Lock()
	defer job.mu.Unlock()
	return job.snapshot, job.err
}

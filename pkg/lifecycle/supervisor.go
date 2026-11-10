package lifecycle

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// Config configures process lifecycle monitoring and interrupt handling.
type Config struct {
	// DoubleInterruptDuration is the window in which a second interrupt
	// bypasses active-operation cancellation and begins shutdown.
	DoubleInterruptDuration time.Duration
	// Signals specifies the OS signals to intercept. Defaults to SIGINT and SIGTERM.
	Signals []os.Signal
	// ExitFunc specifies the function called to terminate the process. Defaults to os.Exit.
	ExitFunc func(code int)
	// ForceExitFunc bypasses blocked graceful hooks on a repeated signal.
	ForceExitFunc func(code int)
}

// Supervisor monitors process signals and coordinates graceful application termination.
type Supervisor struct {
	ctx              context.Context
	cancel           context.CancelFunc
	config           Config
	sigChan          chan os.Signal
	doneChan         chan struct{}
	mu               sync.Mutex
	cancelHandler    func() bool
	shutdownHooks    []func()
	lastInterrupt    time.Time
	running          bool
	shutdownStarted  bool
	shutdownFinished bool
	shutdownDone     chan struct{}
	pendingInterrupt *interruptAttempt
	stopped          bool
	exitCode         int
}

type interruptAttempt struct{ _ byte }

var defaultSupervisor = NewSupervisor(Config{})

// Default returns the default process lifecycle supervisor.
func Default() *Supervisor {
	return defaultSupervisor
}

// NewSupervisor creates an application lifecycle supervisor.
func NewSupervisor(cfg Config) *Supervisor {
	if cfg.DoubleInterruptDuration <= 0 {
		cfg.DoubleInterruptDuration = 1500 * time.Millisecond
	}
	if len(cfg.Signals) == 0 {
		cfg.Signals = []os.Signal{os.Interrupt, syscall.SIGTERM}
	}
	cfg.Signals = append([]os.Signal(nil), cfg.Signals...)
	if cfg.ExitFunc == nil {
		cfg.ExitFunc = os.Exit
	}
	if cfg.ForceExitFunc == nil {
		cfg.ForceExitFunc = cfg.ExitFunc
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Supervisor{
		ctx:      ctx,
		cancel:   cancel,
		config:   cfg,
		sigChan:  make(chan os.Signal, 1),
		doneChan: make(chan struct{}),
	}
}

// Context returns the root application context, which is canceled when shutdown begins.
func (s *Supervisor) Context() context.Context {
	return s.ctx
}

// ExitCode reports the requested shutdown status even if cleanup is still running.
func (s *Supervisor) ExitCode() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitCode
}

// OnCancel registers a callback executed when a single interrupt signal occurs.
// If the callback returns true, the signal is considered consumed and execution continues.
// If it returns false, normal graceful shutdown proceeds.
func (s *Supervisor) OnCancel(handler func() bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelHandler = handler
}

// SetExitFunc replaces the termination function. Installing one that returns instead
// of calling os.Exit lets the caller run its own deferred cleanup: os.Exit inside
// executeShutdown skips every defer in main, so terminal state (alt-screen, cursor)
// can be left dirty on the signal path.
func (s *Supervisor) SetExitFunc(fn func(code int)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fn != nil {
		s.config.ExitFunc = fn
	}
}

// SetForceExitFunc installs the last-resort exit path. It must not wait on
// application locks or graceful hooks, which may be the reason for forcing exit.
func (s *Supervisor) SetForceExitFunc(fn func(int)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fn != nil {
		s.config.ForceExitFunc = fn
	}
}

// Wait waits for graceful shutdown hooks if shutdown has started.
func (s *Supervisor) Wait(ctx context.Context) error {
	s.mu.Lock()
	done := s.shutdownDone
	s.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// OnShutdown registers cleanup in LIFO order, including during shutdown. Hooks
// registered after cleanup has finished run immediately on the calling goroutine.
func (s *Supervisor) OnShutdown(hook func()) {
	if hook == nil {
		return
	}
	s.mu.Lock()
	if s.shutdownFinished {
		s.mu.Unlock()
		hook()
		return
	}
	s.shutdownHooks = append(s.shutdownHooks, hook)
	s.mu.Unlock()
}

// Start begins listening for process signals in the background.
func (s *Supervisor) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running || s.stopped {
		return
	}
	// Subscription and state publication are one operation with respect to Stop.
	signal.Notify(s.sigChan, s.config.Signals...)
	s.running = true
	go s.eventLoop()
}

// Stop releases signal subscriptions and prevents a later Start from reviving them.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.running = false
	s.stopped = true
	s.pendingInterrupt = nil
	signal.Stop(s.sigChan)
	close(s.doneChan)
}

func (s *Supervisor) eventLoop() {
	for {
		select {
		case <-s.doneChan:
			return
		case sig, ok := <-s.sigChan:
			if !ok {
				return
			}
			s.handleSignal(sig)
		}
	}
}

func (s *Supervisor) handleSignal(sig os.Signal) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	shuttingDown := s.shutdownStarted
	if shuttingDown {
		s.mu.Unlock()
		code := 0
		if sig == os.Interrupt {
			code = 130
		}
		s.executeShutdown(code)
		return
	}
	if sig == os.Interrupt {
		now, lastInt := time.Now(), s.lastInterrupt
		s.lastInterrupt = now
		// A quick second interrupt bypasses active-operation cancellation.
		if s.pendingInterrupt != nil || (!lastInt.IsZero() && now.Sub(lastInt) < s.config.DoubleInterruptDuration) {
			s.mu.Unlock()
			s.executeShutdown(130)
			return
		}
		if handler := s.cancelHandler; handler != nil {
			attempt := &interruptAttempt{}
			s.pendingInterrupt = attempt
			s.mu.Unlock()
			// An operation may be blocked on a lock. Keep the signal loop free
			// to begin shutdown or force exit while its cancel callback runs.
			go s.cancelOperation(attempt, handler)
			return
		}
		s.mu.Unlock()
		s.executeShutdown(130)
		return
	}
	s.mu.Unlock()

	// SIGTERM or other signals trigger clean exit
	s.executeShutdown(0)
}

func (s *Supervisor) cancelOperation(attempt *interruptAttempt, handler func() bool) {
	consumed := handler()
	s.mu.Lock()
	if s.pendingInterrupt != attempt || s.shutdownStarted || s.stopped {
		s.mu.Unlock()
		return
	}
	s.pendingInterrupt = nil
	s.mu.Unlock()
	if !consumed {
		s.requestShutdown(130, false)
	}
}

func (s *Supervisor) executeShutdown(exitCode int) {
	s.requestShutdown(exitCode, true)
}

func (s *Supervisor) requestShutdown(exitCode int, force bool) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	if s.shutdownStarted {
		if !force {
			s.mu.Unlock()
			return
		}
		s.exitCode = exitCode
		forceExit := s.config.ForceExitFunc
		s.mu.Unlock()
		if forceExit != nil {
			forceExit(exitCode)
		}
		return
	}
	s.exitCode = exitCode
	s.shutdownStarted = true
	s.pendingInterrupt = nil
	s.shutdownDone = make(chan struct{})
	done := s.shutdownDone
	exitFunc := s.config.ExitFunc
	s.mu.Unlock()
	s.cancel()

	// Keep the signal loop available while graceful hooks execute.
	go func() {
		defer close(done)
		for {
			s.mu.Lock()
			if len(s.shutdownHooks) == 0 {
				s.shutdownFinished = true
				s.mu.Unlock()
				break
			}
			last := len(s.shutdownHooks) - 1
			hook := s.shutdownHooks[last]
			s.shutdownHooks[last] = nil
			s.shutdownHooks = s.shutdownHooks[:last]
			s.mu.Unlock()
			hook()
		}
		if exitFunc != nil {
			exitFunc(exitCode)
		}
	}()
}

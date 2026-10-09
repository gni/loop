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
	// DoubleInterruptDuration specifies the window in which two interrupts force exit.
	DoubleInterruptDuration time.Duration
	// Signals specifies the OS signals to intercept. Defaults to SIGINT and SIGTERM.
	Signals []os.Signal
	// ExitFunc specifies the function called to terminate the process. Defaults to os.Exit.
	ExitFunc func(code int)
}

// Supervisor monitors process signals and coordinates graceful application termination.
type Supervisor struct {
	ctx           context.Context
	cancel        context.CancelFunc
	config        Config
	sigChan       chan os.Signal
	doneChan      chan struct{}
	mu            sync.Mutex
	cancelHandler func() bool
	shutdownHooks []func()
	lastInterrupt time.Time
	running       bool
}

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
	if cfg.ExitFunc == nil {
		cfg.ExitFunc = os.Exit
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

// OnCancel registers a callback executed when a single interrupt signal occurs.
// If the callback returns true, the signal is considered consumed and execution continues.
// If it returns false, normal graceful shutdown proceeds.
func (s *Supervisor) OnCancel(handler func() bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelHandler = handler
}

// OnShutdown registers a cleanup hook to be executed on shutdown in LIFO order.
func (s *Supervisor) OnShutdown(hook func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shutdownHooks = append(s.shutdownHooks, hook)
}

// Start begins listening for process signals in the background.
func (s *Supervisor) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	signal.Notify(s.sigChan, s.config.Signals...)
	go s.eventLoop()
}

// Stop stops the background signal monitor and releases signal subscriptions.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	s.mu.Unlock()

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
	now := time.Now()
	lastInt := s.lastInterrupt
	s.lastInterrupt = now
	handler := s.cancelHandler
	s.mu.Unlock()

	if sig == os.Interrupt {
		// Double Ctrl+C within duration triggers immediate forced exit
		if !lastInt.IsZero() && now.Sub(lastInt) < s.config.DoubleInterruptDuration {
			s.executeShutdown(130)
			return
		}

		if handler != nil && handler() {
			return
		}

		s.executeShutdown(130)
		return
	}

	// SIGTERM or other signals trigger clean exit
	s.executeShutdown(0)
}

func (s *Supervisor) executeShutdown(exitCode int) {
	s.cancel()

	s.mu.Lock()
	hooks := make([]func(), len(s.shutdownHooks))
	copy(hooks, s.shutdownHooks)
	s.mu.Unlock()

	// Execute hooks in LIFO order
	for i := len(hooks) - 1; i >= 0; i-- {
		if hooks[i] != nil {
			hooks[i]()
		}
	}

	s.config.ExitFunc(exitCode)
}

// TriggerSignal simulates an incoming OS signal for testing or programmatic shutdown.
func (s *Supervisor) TriggerSignal(sig os.Signal) {
	s.handleSignal(sig)
}

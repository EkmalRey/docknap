package main

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/docker/docker/client"
)

type Event struct {
	Time    time.Time              `json:"time"`
	Type    string                 `json:"type"`
	Message string                 `json:"message,omitempty"`
	Fields  map[string]interface{} `json:"fields,omitempty"`
}

const maxEventsPerService = 100

type Metrics struct {
	Proxy      *Counter
	Starts     *Counter
	Stops      *Counter
	IdleStop   *Counter
	StartFail  *Counter
	AuthFail   *Counter
	StartDur   *Histogram
	ProxyDur   *Histogram
	Registered *Gauge
	State      *Gauge
}

type serviceState struct {
	State string
}

type Docknap struct {
	cli                 *client.Client
	configs             map[string]*Config
	idleTimers          map[string]*time.Timer
	bootStarts          map[string]time.Time
	startedAt           map[string]time.Time
	events              map[string][]Event
	startLocks          map[string]*sync.Mutex
	mu                  sync.RWMutex
	listenAddr          string
	writeTimeout        time.Duration
	idleDefault         time.Duration
	startTimeoutDefault time.Duration
	logger              *Logger
	metrics             *Registry
	m                   *Metrics
	adminUser           string
	adminUserHash       []byte
	adminPassHash       []byte
	adminHost           string
	networkName         string
	tldCount            int
	trustedProxies      []cidr
	rootCtx             context.Context
	rootCancel          context.CancelFunc
	sessions            *sessionStore
	rateLimiter         *loginRateLimiter
	waitLimiter         *loginRateLimiter
	notifier            notifier
	eventsOK            atomicBool
	pollFallback        atomic.Bool

	states           map[string]*serviceState
	ipCache          map[string]string
	ipCacheAt        map[string]time.Time
	lastState        map[string]string
	dockerStartedAt  map[string]string
	dockerID         map[string]string
	startupTimedOut  map[string]bool
	readinessWorkers map[string]*readinessAttempt
}

type readinessAttempt struct {
	started time.Time
	ctx     context.Context
	cancel  context.CancelFunc
}

func (s *Docknap) eventsHealthy() bool { return s.eventsOK.get() }

func newDocknap(cli *client.Client, logger *Logger, reg *Registry) *Docknap {
	m := &Metrics{
		Proxy:     reg.Counter("docknap_proxy_requests_total", "Proxied requests by subdomain and HTTP status", []string{"subdomain", "status"}),
		Starts:    reg.Counter("docknap_container_starts_total", "Container starts triggered by docknap", []string{"subdomain"}),
		Stops:     reg.Counter("docknap_container_stops_total", "Container stops triggered by docknap", []string{"subdomain", "reason"}),
		IdleStop:  reg.Counter("docknap_idle_timeouts_total", "Idle timeouts that stopped a container", []string{"subdomain"}),
		StartFail: reg.Counter("docknap_startup_failures_total", "Startup failures (timeout or error)", []string{"subdomain", "reason"}),
		AuthFail:  reg.Counter("docknap_admin_auth_failures_total", "Admin auth failures", []string{"path", "reason"}),
		StartDur: reg.Histogram("docknap_start_duration_seconds", "Time from wake to ready port", []string{"subdomain"},
			[]float64{0.5, 1, 2, 5, 10, 15, 30, 60, 120, 300}),
		ProxyDur: reg.Histogram("docknap_proxy_duration_seconds", "Duration of proxied requests", []string{"subdomain"},
			[]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}),
		Registered: reg.Gauge("docknap_registered_containers", "Number of registered containers", nil),
		State:      reg.Gauge("docknap_container_state", "Current container state (1 for active state)", []string{"subdomain", "state"}),
	}
	return &Docknap{
		cli:              cli,
		configs:          make(map[string]*Config),
		idleTimers:       make(map[string]*time.Timer),
		bootStarts:       make(map[string]time.Time),
		startedAt:        make(map[string]time.Time),
		events:           make(map[string][]Event),
		startLocks:       make(map[string]*sync.Mutex),
		states:           make(map[string]*serviceState),
		ipCache:          make(map[string]string),
		ipCacheAt:        make(map[string]time.Time),
		lastState:        make(map[string]string),
		dockerStartedAt:  make(map[string]string),
		dockerID:         make(map[string]string),
		startupTimedOut:  make(map[string]bool),
		readinessWorkers: make(map[string]*readinessAttempt),
		logger:           logger,
		metrics:          reg,
		m:                m,
		sessions:         newSessionStore(12 * time.Hour),
		rateLimiter:      newLoginRateLimiter(5, time.Minute),
		waitLimiter:      newLoginRateLimiter(30, time.Minute),
	}
}

func (s *Docknap) recordEvent(sub, eventType, message string, fields map[string]interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := Event{Time: time.Now(), Type: eventType, Message: message, Fields: fields}
	hist := s.events[sub]
	hist = append(hist, ev)
	if len(hist) > maxEventsPerService {
		hist = hist[len(hist)-maxEventsPerService:]
	}
	s.events[sub] = hist
}

// markBootStart atomically records the boot start time for a service if one
// is not already set, and returns the (possibly existing) value. Used by
// handleWait so concurrent waiters see a single, consistent boot origin.
func (s *Docknap) markBootStart(sub string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.bootStarts[sub]; ok {
		return t
	}
	now := time.Now()
	s.bootStarts[sub] = now
	return now
}

func (s *Docknap) claimReadinessWorker(sub string) (*readinessAttempt, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readinessWorkers == nil {
		s.readinessWorkers = make(map[string]*readinessAttempt)
	}
	if attempt := s.readinessWorkers[sub]; attempt != nil {
		return attempt, false
	}
	started, ok := s.bootStarts[sub]
	if !ok {
		started = time.Now()
		s.bootStarts[sub] = started
	}
	rootCtx := s.rootCtx
	if rootCtx == nil {
		rootCtx = context.Background()
	}
	ctx, cancel := context.WithCancel(rootCtx)
	attempt := &readinessAttempt{started: started, ctx: ctx, cancel: cancel}
	s.readinessWorkers[sub] = attempt
	return attempt, true
}

func (s *Docknap) finishReadinessAttempt(sub string, attempt *readinessAttempt) bool {
	s.mu.Lock()
	if s.readinessWorkers[sub] != attempt {
		s.mu.Unlock()
		return false
	}
	delete(s.readinessWorkers, sub)
	delete(s.bootStarts, sub)
	s.mu.Unlock()
	attempt.cancel()
	return true
}

func (s *Docknap) clearBootStart(sub string) {
	s.mu.Lock()
	attempt := s.readinessWorkers[sub]
	delete(s.bootStarts, sub)
	delete(s.readinessWorkers, sub)
	s.mu.Unlock()
	if attempt != nil {
		attempt.cancel()
	}
}

func (s *Docknap) setStateIP(sub, ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ip == "" {
		delete(s.ipCache, sub)
		delete(s.ipCacheAt, sub)
		return
	}
	s.ipCache[sub] = ip
	s.ipCacheAt[sub] = time.Now()
}

func (s *Docknap) cachedIP(sub string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ip, ok := s.ipCache[sub]
	if !ok {
		return "", false
	}
	if time.Since(s.ipCacheAt[sub]) > 30*time.Second {
		return "", false
	}
	return ip, true
}

package main

import (
	"context"
	"sync"
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
	State      string
	LastSeen   time.Time
	StartedAt  time.Time
	IP         string
	ReadyChans []chan struct{}
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
	trustedProxies      []*cidr
	rootCtx             context.Context
	rootCancel          context.CancelFunc
	sessions            *sessionStore
	rateLimiter         *loginRateLimiter
	notifier            notifier
	eventsOK            atomicBool

	states    map[string]*serviceState
	ipCache   map[string]string
	ipCacheAt map[string]time.Time
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
		cli:         cli,
		configs:     make(map[string]*Config),
		idleTimers:  make(map[string]*time.Timer),
		bootStarts:  make(map[string]time.Time),
		startedAt:   make(map[string]time.Time),
		events:      make(map[string][]Event),
		startLocks:  make(map[string]*sync.Mutex),
		states:      make(map[string]*serviceState),
		ipCache:     make(map[string]string),
		ipCacheAt:   make(map[string]time.Time),
		logger:      logger,
		metrics:     reg,
		m:           m,
		sessions:    newSessionStore(12 * time.Hour),
		rateLimiter: newLoginRateLimiter(5, time.Minute),
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

func (s *Docknap) clearBootStart(sub string) {
	s.mu.Lock()
	delete(s.bootStarts, sub)
	s.mu.Unlock()
}

func (s *Docknap) snapshotServices() (map[string]*Config, map[string]time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfgs := make(map[string]*Config, len(s.configs))
	for k, v := range s.configs {
		cfgs[k] = v
	}
	starts := make(map[string]time.Time, len(s.startedAt))
	for k, v := range s.startedAt {
		starts[k] = v
	}
	return cfgs, starts
}

func (s *Docknap) serviceStateCopy(sub string) *serviceState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if st, ok := s.states[sub]; ok {
		cp := *st
		cp.ReadyChans = nil
		return &cp
	}
	return &serviceState{State: "unknown"}
}

func (s *Docknap) updateState(sub, state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.states[sub]
	if !ok {
		st = &serviceState{State: state}
		s.states[sub] = st
	} else {
		st.State = state
	}
	st.LastSeen = time.Now()
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

func (s *Docknap) subscribeReady(sub string) chan struct{} {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	if st, ok := s.states[sub]; ok {
		st.ReadyChans = append(st.ReadyChans, ch)
	} else {
		s.states[sub] = &serviceState{ReadyChans: []chan struct{}{ch}}
	}
	s.mu.Unlock()
	return ch
}

func (s *Docknap) broadcastReady(sub string) {
	s.mu.Lock()
	st, ok := s.states[sub]
	if !ok {
		s.mu.Unlock()
		return
	}
	chans := st.ReadyChans
	st.ReadyChans = nil
	s.mu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

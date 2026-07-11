package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/client"
)

func dockerListServer(t *testing.T, body string) *client.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	cli, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	return cli
}

func dockerTestDocknap(t *testing.T, body string) *Docknap {
	t.Helper()
	s := newAuthTestDocknap(t)
	s.cli = dockerListServer(t, body)
	s.networkName = "test"
	s.lastState = make(map[string]string)
	s.dockerStartedAt = make(map[string]string)
	s.dockerID = make(map[string]string)
	s.startupTimedOut = make(map[string]bool)
	s.rootCtx = context.Background()
	s.startTimeoutDefault = time.Minute
	return s
}

func TestSyncContainersRegistersCanonicalContainerName(t *testing.T) {
	s := dockerTestDocknap(t, `[{"Id":"abc","Names":["/demo-1"],"State":"exited","Labels":{"docknap.enable":"true","docknap.subdomain":"demo","docknap.target_port":"80"}}]`)
	if err := s.syncContainers(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := s.configs["demo"].Container; got != "demo-1" {
		t.Fatalf("container = %q, want demo-1", got)
	}
}

func TestReadinessWorkerHasSingleOwnerPerBootAttempt(t *testing.T) {
	s := newAuthTestDocknap(t)
	const workers = 20
	var wg sync.WaitGroup
	owners := make(chan bool, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, owner := s.claimReadinessWorker("demo")
			owners <- owner
		}()
	}
	wg.Wait()
	close(owners)
	count := 0
	for owner := range owners {
		if owner {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("readiness owners = %d, want 1", count)
	}
}

func TestAlreadyRunningUnhealthyServiceReusesReadinessWorker(t *testing.T) {
	s := newAuthTestDocknap(t)
	first, firstOwner := s.claimReadinessWorker("demo")
	second, secondOwner := s.claimReadinessWorker("demo")
	if !firstOwner || secondOwner || second != first {
		t.Fatalf("claims = (%p,%v), (%p,%v); want same attempt and one owner", first, firstOwner, second, secondOwner)
	}
}

func TestReadinessAttemptUsesExistingCanonicalBootStart(t *testing.T) {
	s := newAuthTestDocknap(t)
	start := time.Now().Add(-time.Second)
	s.bootStarts["demo"] = start
	attempt, owner := s.claimReadinessWorker("demo")
	if !owner || !attempt.started.Equal(start) {
		t.Fatalf("claim = (%v,%v), want canonical %v and owner", attempt.started, owner, start)
	}
}

func TestClearBootStartCancelsWorkerBeforeRetry(t *testing.T) {
	s := newAuthTestDocknap(t)
	s.rootCtx = context.Background()
	old, _ := s.claimReadinessWorker("demo")
	s.clearBootStart("demo")
	select {
	case <-old.ctx.Done():
	default:
		t.Fatal("cleared readiness worker was not cancelled")
	}
	newer, owner := s.claimReadinessWorker("demo")
	if !owner || newer == old || newer.ctx.Err() != nil {
		t.Fatal("retry did not get a live replacement worker")
	}
}

func TestRootCancellationCancelsReadinessAttempt(t *testing.T) {
	s := newAuthTestDocknap(t)
	s.rootCtx, s.rootCancel = context.WithCancel(context.Background())
	attempt, _ := s.claimReadinessWorker("demo")
	s.rootCancel()
	select {
	case <-attempt.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("root cancellation did not reach readiness attempt")
	}
}

func TestStaleReadinessAttemptCannotClearRetry(t *testing.T) {
	s := newAuthTestDocknap(t)
	old, _ := s.claimReadinessWorker("demo")
	s.clearBootStart("demo")
	newer, _ := s.claimReadinessWorker("demo")
	s.finishReadinessAttempt("demo", old)
	if s.readinessWorkers["demo"] != newer {
		t.Fatal("stale attempt cleared newer retry")
	}
}

func TestStaleReadinessAttemptCannotTimeoutRetry(t *testing.T) {
	s := newAuthTestDocknap(t)
	old, _ := s.claimReadinessWorker("demo")
	s.clearBootStart("demo")
	newer, _ := s.claimReadinessWorker("demo")
	cfg := &Config{Subdomain: "demo", Container: "demo", StartupTimeout: time.Second}
	s.markStartupTimedOut(cfg, old)
	if s.startupTimedOut["demo"] || s.readinessWorkers["demo"] != newer {
		t.Fatal("stale attempt timed out or cleared newer retry")
	}
}

func TestCancellationClearsOnlyCurrentReadinessAttempt(t *testing.T) {
	s := newAuthTestDocknap(t)
	attempt, _ := s.claimReadinessWorker("demo")
	s.finishReadinessAttempt("demo", attempt)
	if _, ok := s.bootStarts["demo"]; ok || s.readinessWorkers["demo"] != nil {
		t.Fatal("current cancelled attempt remains")
	}
}

func TestSyncContainersSkipsLabeledContainerWithoutName(t *testing.T) {
	for _, names := range []string{"null", "[]"} {
		t.Run(names, func(t *testing.T) {
			var logs strings.Builder
			s := dockerTestDocknap(t, `[{"Id":"abc","Names":`+names+`,"State":"exited","Labels":{"docknap.enable":"true","docknap.subdomain":"demo","docknap.target_port":"80"}}]`)
			s.logger = NewLogger(&logs, false)
			if err := s.syncContainers(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(s.configs) != 0 {
				t.Fatalf("registered configs = %d, want 0", len(s.configs))
			}
			if !strings.Contains(logs.String(), "nameless") {
				t.Fatalf("warning missing from logs: %s", logs.String())
			}
		})
	}
}

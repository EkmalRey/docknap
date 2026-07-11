package main

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/docker/docker/api/types/container"
)

type blockingLogsClient struct {
	mu    sync.Mutex
	calls []string
}

func (c *blockingLogsClient) ContainerLogs(ctx context.Context, name string, _ container.LogsOptions) (io.ReadCloser, error) {
	c.mu.Lock()
	c.calls = append(c.calls, name)
	c.mu.Unlock()
	return &contextReadCloser{ctx: ctx}, nil
}

type contextReadCloser struct{ ctx context.Context }

func (r *contextReadCloser) Read([]byte) (int, error) { <-r.ctx.Done(); return 0, r.ctx.Err() }
func (*contextReadCloser) Close() error               { return nil }

func TestObsoleteLogOwnerCannotFinishReplacement(t *testing.T) {
	lt := &logTailer{cancels: map[string]context.CancelFunc{}, refs: map[string]int{}, owners: map[string]*logOwner{}}
	old := &logOwner{container: "old"}
	newer := &logOwner{container: "new"}
	lt.refs["demo"] = 1
	lt.owners["demo"] = newer
	lt.finish("demo", old)
	if lt.owners["demo"] != newer || lt.refs["demo"] != 1 {
		t.Fatal("obsolete owner removed replacement")
	}
}

func TestLogTailerReplacesOwnerWhenContainerChanges(t *testing.T) {
	lt := &logTailer{cancels: map[string]context.CancelFunc{}, refs: map[string]int{}, owners: map[string]*logOwner{}}
	cli := &blockingLogsClient{}
	lt.start(cli, "demo", "old")
	lt.mu.Lock()
	old := lt.owners["demo"]
	lt.mu.Unlock()
	lt.start(cli, "demo", "new")
	lt.mu.Lock()
	newer := lt.owners["demo"]
	refs := lt.refs["demo"]
	lt.mu.Unlock()
	lt.stopAll()
	if newer == old || newer.container != "new" || refs != 2 {
		t.Fatalf("owner=%#v refs=%d; want replacement for new container with both subscribers", newer, refs)
	}
}

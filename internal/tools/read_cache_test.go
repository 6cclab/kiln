package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
)

func TestReadCacheFreshnessAndBounds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	cache := NewReadCache(2, 1024)
	read := ReadToolWithCache(execenv.New(dir), cache)
	call := func(ctx context.Context) (tool.Result, error) {
		return read.Execute(ctx, json.RawMessage(`{"path":"a.txt"}`), nil, tool.Invocation{})
	}
	first, err := call(context.Background())
	if err != nil || resultText(first) != "first" {
		t.Fatalf("initial read = %q, %v", resultText(first), err)
	}
	if err := os.WriteFile(path, []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	second, err := call(context.Background())
	if err != nil || resultText(second) != "other" {
		t.Fatalf("same-size same-mtime edit = %q, %v", resultText(second), err)
	}
	second.Content[0] = msg.Text("poison")
	third, err := call(context.Background())
	if err != nil || resultText(third) != "other" {
		t.Fatalf("cache isolation = %q, %v", resultText(third), err)
	}
	if cache.usedBytes > cache.maxBytes || len(cache.items) > cache.maxEntries {
		t.Fatalf("cache bounds: bytes=%d entries=%d", cache.usedBytes, len(cache.items))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := call(ctx); err != context.Canceled {
		t.Fatalf("canceled read error = %v", err)
	}
}

func TestReadCachePermissionRechecked(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod semantics differ")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	read := ReadToolWithCache(execenv.New(dir), NewReadCache(2, 1024))
	args := json.RawMessage(`{"path":"a.txt"}`)
	if first, err := read.Execute(context.Background(), args, nil, tool.Invocation{}); err != nil || resultText(first) != "secret" {
		t.Fatalf("first read = %v, %v", first, err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if _, err := os.Open(path); err == nil {
		t.Skip("current user bypasses file mode permission")
	}
	second, err := read.Execute(context.Background(), args, nil, tool.Invocation{})
	if err != nil || !second.IsError {
		t.Fatalf("denied read = %v, %v", second, err)
	}
}

func TestReadCacheEviction(t *testing.T) {
	cache := NewReadCache(2, 150)
	for _, content := range []string{"a", "b", "c"} {
		cache.store(content, []byte(content), tool.Text(content))
	}
	if len(cache.items) != 2 || cache.usedBytes > 150 {
		t.Fatalf("unbounded cache: %d items, %d bytes", len(cache.items), cache.usedBytes)
	}
	if _, ok := cache.lookup("a", []byte("a")); ok {
		t.Fatal("oldest result remained cached")
	}
}

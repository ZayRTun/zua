package catalog

import (
	"math"
	"testing"
)

// TestLookupGLM53Flash pins the transcription of pi-ai's generated OpenCode
// Go catalog entry for glm-5.3-flash (issue #13): context window, max output,
// per-1M rates, and the supported thinking levels.
func TestLookupGLM53Flash(t *testing.T) {
	entry, ok := Lookup("glm-5.3-flash")
	if !ok {
		t.Fatal("catalog must contain glm-5.3-flash")
	}
	if entry.ContextWindow != 1_000_000 {
		t.Fatalf("contextWindow = %d, want 1000000", entry.ContextWindow)
	}
	if entry.MaxTokens != 131_072 {
		t.Fatalf("maxTokens = %d, want 131072", entry.MaxTokens)
	}
	if entry.InputPerM != 0.15 || entry.OutputPerM != 0.50 {
		t.Fatalf("input/output rates = %v/%v, want 0.15/0.50", entry.InputPerM, entry.OutputPerM)
	}
	if entry.CacheReadPerM != 0.03 {
		t.Fatalf("cacheRead rate = %v, want 0.03", entry.CacheReadPerM)
	}
	if entry.CacheWritePerM != 0 {
		t.Fatalf("cacheWrite rate = %v, want 0", entry.CacheWritePerM)
	}
	wantThinking := []string{"low", "high", "max"}
	if len(entry.Thinking) != len(wantThinking) {
		t.Fatalf("thinking levels = %v, want %v", entry.Thinking, wantThinking)
	}
	for index, level := range wantThinking {
		if entry.Thinking[index] != level {
			t.Fatalf("thinking levels = %v, want %v", entry.Thinking, wantThinking)
		}
	}
}

// TestLookupMissingModel pins that unknown ids return ok=false — the Usage
// Line omits catalog-derived segments for them instead of inventing values.
func TestLookupMissingModel(t *testing.T) {
	if _, ok := Lookup("not-in-the-catalog"); ok {
		t.Fatal("unknown model must not resolve")
	}
	if _, ok := Lookup(""); ok {
		t.Fatal("empty id must not resolve")
	}
}

// TestCost pins the cost math: buckets × per-1M rates, with the cache-read
// and cache-write buckets carved out of the (inclusive) input total.
func TestCost(t *testing.T) {
	entry := Model{
		InputPerM: 0.15, OutputPerM: 0.50,
		CacheReadPerM: 0.03, CacheWritePerM: 0,
	}
	// in=500_000 (of which cached=400_000, cacheWrite=50_000), out=10_000:
	// uncached input 50k×$0.15 + 400k×$0.03 + 50k×$0 + 10k×$0.50 per 1M
	// = 0.0075 + 0.012 + 0 + 0.005 = 0.0245
	cost := entry.Cost(Usage{Input: 500_000, Cached: 400_000, CacheWrite: 50_000, Output: 10_000})
	if math.Abs(cost-0.0245) > 1e-9 {
		t.Fatalf("cost = %v, want 0.0245", cost)
	}
	// No cache reported: the whole input is charged at the input rate.
	plain := entry.Cost(Usage{Input: 100_000})
	if math.Abs(plain-0.015) > 1e-9 {
		t.Fatalf("cost = %v, want 0.015", plain)
	}
}

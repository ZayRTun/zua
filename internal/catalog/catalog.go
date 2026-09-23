// Package catalog is zua's tiny in-repo model catalog: the data the Usage
// Line needs that the wire format does not carry — context window, max
// output tokens, and per-1M cost rates. Entries are transcribed from
// pi-ai's generated OpenCode Go catalog; only models verified against it
// belong here. Callers must treat a missing entry as "no catalog data":
// catalog-derived segments are omitted, never invented.
package catalog

// Model is one catalog entry.
type Model struct {
	ID string
	// DisplayName is the prettified name for chrome, e.g. "GLM-5.3-Flash"
	// (the Usage Line always keeps the raw id, matching pi).
	DisplayName   string
	ContextWindow int64
	MaxTokens     int64

	// Per-1M-token rates in dollars.
	InputPerM      float64
	OutputPerM     float64
	CacheReadPerM  float64
	CacheWritePerM float64

	// Thinking levels the model supports, low to high.
	Thinking []string
}

// models holds the catalog. The glm-5.3-flash entry is transcribed from
// pi-ai's generated OpenCode Go catalog (issue #13).
var models = map[string]Model{
	"glm-5.3-flash": {
		ID:            "glm-5.3-flash",
		DisplayName:   "GLM-5.3-Flash",
		ContextWindow: 1_000_000,
		MaxTokens:     131_072,
		InputPerM:     0.15,
		OutputPerM:    0.50,
		CacheReadPerM: 0.03,
		Thinking:      []string{"low", "high", "max"},
	},
}

// Lookup resolves a model id to its catalog entry.
func Lookup(id string) (Model, bool) {
	entry, ok := models[id]
	return entry, ok
}

// Prettify resolves a model id to its display name for chrome. Prettified
// names are catalog data, so catalog-missing ids render raw — never
// invented.
func Prettify(id string) string {
	entry, ok := models[id]
	if !ok || entry.DisplayName == "" {
		return id
	}
	return entry.DisplayName
}

// Usage is one response's token buckets. Input is inclusive — it already
// contains Cached and CacheWrite, matching the harness's semantics.
type Usage struct {
	Input      int64
	Cached     int64 // cache-read
	CacheWrite int64 // cache-write
	Output     int64
}

// Cost computes the dollar cost of one response from its token buckets.
// Input is inclusive (it already contains the cache-read and cache-write
// buckets), so those are carved back out before applying the plain input
// rate. Rates are per 1M tokens.
func (m Model) Cost(usage Usage) float64 {
	uncached := usage.Input - usage.Cached - usage.CacheWrite
	if uncached < 0 {
		uncached = 0
	}
	return (float64(uncached)*m.InputPerM +
		float64(usage.Cached)*m.CacheReadPerM +
		float64(usage.CacheWrite)*m.CacheWritePerM +
		float64(usage.Output)*m.OutputPerM) / 1_000_000
}

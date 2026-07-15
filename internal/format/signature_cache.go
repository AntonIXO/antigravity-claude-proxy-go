package format

import (
	"sync"
	"time"
)

const signatureCacheTTL = 2 * time.Hour

type signatureEntry struct {
	signature string
	family    ModelFamily
	createdAt time.Time
}

// SignatureCache retains Gemini tool-call signatures and records the model
// family that produced thinking signatures. It is process-local for two hours
// because Claude clients may strip these fields.
type SignatureCache struct {
	mu       sync.RWMutex
	now      func() time.Time
	tools    map[string]signatureEntry
	thinking map[string]signatureEntry
}

func NewSignatureCache() *SignatureCache {
	return &SignatureCache{
		now:      time.Now,
		tools:    make(map[string]signatureEntry),
		thinking: make(map[string]signatureEntry),
	}
}

func (cache *SignatureCache) CacheTool(toolID, signature string) {
	if cache == nil || toolID == "" || signature == "" {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.tools[toolID] = signatureEntry{signature: signature, createdAt: cache.now()}
}

func (cache *SignatureCache) Tool(toolID string) string {
	if cache == nil || toolID == "" {
		return ""
	}
	cache.mu.RLock()
	entry, ok := cache.tools[toolID]
	cache.mu.RUnlock()
	if !ok {
		return ""
	}
	if cache.now().Sub(entry.createdAt) > signatureCacheTTL {
		cache.mu.Lock()
		delete(cache.tools, toolID)
		cache.mu.Unlock()
		return ""
	}
	return entry.signature
}

func (cache *SignatureCache) CacheThinking(signature string, family ModelFamily) {
	if cache == nil || len(signature) < MinSignatureLength {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.thinking[signature] = signatureEntry{family: family, createdAt: cache.now()}
}

func (cache *SignatureCache) ThinkingFamily(signature string) ModelFamily {
	if cache == nil || signature == "" {
		return FamilyUnknown
	}
	cache.mu.RLock()
	entry, ok := cache.thinking[signature]
	cache.mu.RUnlock()
	if !ok {
		return FamilyUnknown
	}
	if cache.now().Sub(entry.createdAt) > signatureCacheTTL {
		cache.mu.Lock()
		delete(cache.thinking, signature)
		cache.mu.Unlock()
		return FamilyUnknown
	}
	return entry.family
}

func (cache *SignatureCache) Clear() {
	if cache == nil {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	clear(cache.tools)
	clear(cache.thinking)
}

package providers

import "sort"

// Registry maps provider kinds to adapters.
type Registry struct {
	adapters map[string]Adapter
}

// NewRegistry returns every built-in adapter sharing env. The local worker is
// not an HTTP adapter (the ai service re-dispatches those jobs to workers).
func NewRegistry(env Env) *Registry {
	r := &Registry{adapters: map[string]Adapter{}}
	for _, a := range []Adapter{
		Placeholder{}, OpenAI{Env: env}, OpenRouter{Env: env}, Fal{Env: env}, Replicate{Env: env}, Together{Env: env},
		Runware{Env: env}, HuggingFace{Env: env}, Google{Env: env}, XAI{Env: env}, Anthropic{Env: env}, Ollama{Env: env},
		OpenAICompatible{Env: env}, ComfyUI{Env: env}, HTTPAdapter{Env: env},
	} {
		r.adapters[a.Kind()] = a
	}
	return r
}

// Register adds or replaces an adapter (plugins, tests).
func (r *Registry) Register(a Adapter) { r.adapters[a.Kind()] = a }

// Get returns the adapter of kind.
func (r *Registry) Get(kind string) (Adapter, bool) {
	a, ok := r.adapters[kind]
	return a, ok
}

// Kinds lists registered kinds.
func (r *Registry) Kinds() []string {
	out := make([]string, 0, len(r.adapters))
	for k := range r.adapters {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Can reports whether kind supports a capability: "image", "text", "media" or "models".
func (r *Registry) Can(kind, capability string) bool {
	if kind == KindLocalWorker {
		return capability == "image" || capability == "media"
	}
	a, ok := r.adapters[kind]
	if !ok {
		return false
	}
	switch capability {
	case "image":
		_, ok = a.(ImageGenerator)
	case "text":
		_, ok = a.(TextGenerator)
	case "media":
		_, ok = a.(MediaGenerator)
	case "models":
		_, ok = a.(ModelLister)
	default:
		ok = false
	}
	return ok
}

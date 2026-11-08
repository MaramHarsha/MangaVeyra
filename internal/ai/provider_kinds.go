package ai

import "strings"

// Provider kinds added in Phase 4 (ADR 0013 §9). Every adapter is tested only
// against fake transports; none has been verified with a live key (see
// providerVerification). The fixed kinds have frozen endpoints, models and
// tariffs like ADR 0005–0009; the custom kinds are set up in G12 and keep
// their endpoint and description in the connection's config.
const (
	providerKindGoogle      = "google"
	providerKindXAI         = "xai"
	providerKindTogether    = "together"
	providerKindReplicate   = "replicate"
	providerKindRunware     = "runware"
	providerKindHuggingFace = "huggingface"
	providerKindHTTP        = "http"
	providerKindComfyUI     = "comfyui"
)

// managedProviderKinds is every kind the store lists, routes and dispatches.
// A row of any other kind is invisible to routing and the pages.
var managedProviderKinds = []string{
	providerKindOpenRouter, providerKindOpenAI, providerKindFal, providerKindLocalText,
	providerKindGoogle, providerKindXAI, providerKindTogether, providerKindReplicate, providerKindRunware,
	providerKindHuggingFace, providerKindHTTP, providerKindComfyUI,
}

// keyFormKinds use the single-page key form of G2 "Add provider" (fixed
// endpoint, one encrypted key).
var keyFormKinds = []string{
	providerKindOpenRouter, providerKindOpenAI, providerKindFal,
	providerKindGoogle, providerKindXAI, providerKindTogether, providerKindReplicate, providerKindRunware,
}

// customKinds are connected through the G12 wizard and store their endpoint
// in base_url and their description in config.
var customKinds = []string{providerKindLocalText, providerKindHuggingFace, providerKindHTTP, providerKindComfyUI}

func kindIn(kind string, kinds []string) bool {
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}

func customKind(kind string) bool { return kindIn(kind, customKinds) }

// keylessKind connections may be saved without a key: servers on this
// machine or network (ADR 0007's local text rules).
func keylessKind(kind string) bool {
	return kind == providerKindLocalText || kind == providerKindHTTP || kind == providerKindComfyUI
}

// privateServerKind connections only ever dial loopback or private-network
// addresses through the private-endpoint gate (ADR 0007), have no remote
// spend reservation and are blocked by local_only like custom text.
func privateServerKind(kind string) bool { return keylessKind(kind) }

// adminOnlyKind connections are a server the admin names (ADR 0013 §9): a
// ComfyUI workflow or a generic HTTP endpoint sends an arbitrary path and
// JSON body (or arbitrary custom nodes) to a machine on the server's
// network, so only an admin connects one, always for everyone (server
// scope). Members use them through routing like any server provider.
func adminOnlyKind(kind string) bool { return kind == providerKindHTTP || kind == providerKindComfyUI }

// testedKind connections run only after a passed connection test for their
// current endpoint, key and description (G12 step 2, G2 Test connection).
func testedKind(kind string) bool {
	return kind == providerKindHuggingFace || kind == providerKindHTTP || kind == providerKindComfyUI
}

// imageOnlyKind connections have no text route; the text table omits them.
func imageOnlyKind(kind string) bool {
	switch kind {
	case providerKindFal, providerKindReplicate, providerKindRunware, providerKindHTTP, providerKindComfyUI:
		return true
	}
	return false
}

// capabilityKind connections report Text and Images from one model-list
// check (ADR 0006's OpenAI pattern), stored with the connection's last test.
func capabilityKind(kind string) bool {
	return kind == providerKindOpenAI || kind == providerKindGoogle || kind == providerKindTogether
}

// kindPlaceholders returns "?, ?, …" and the arguments for a kind IN list.
func kindPlaceholders(kinds []string) (string, []any) {
	args := make([]any, len(kinds))
	for i, k := range kinds {
		args[i] = k
	}
	return strings.TrimSuffix(strings.Repeat("?, ", len(kinds)), ", "), args
}

// Verification status of a route, shown in G2 and recorded in the catalogue.
const (
	verifiedLive = "live"
	verifiedFake = "fake"
)

// providerVerification is how a kind's adapter has been verified. Only the
// routes the owner ran with a real key (ADR 0006/0009 live QA) are "live";
// every Phase 4 adapter is tested against fake transports only.
func providerVerification(kind string) string {
	switch kind {
	case providerKindOpenAI:
		return verifiedLive
	case providerKindGoogle, providerKindXAI, providerKindTogether, providerKindReplicate, providerKindRunware,
		providerKindHuggingFace, providerKindHTTP, providerKindComfyUI:
		return verifiedFake
	}
	return ""
}

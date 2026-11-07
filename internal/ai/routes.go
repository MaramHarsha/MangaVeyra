package ai

import "context"

// remoteRoute is one frozen remote text route: a provider kind, its single
// model, its hard per-call reservation and its context fit check. Planning,
// dispatch, reservation and connection tests all derive from this table, so a
// plan can never pair a provider with another kind's model or price.
type remoteRoute struct {
	Kind       string
	Model      string
	ReserveUSD float64 // one outbound call
	PlanUSD    float64 // one claimed attempt: initial call + one repair
	Label      string  // for routing explanations
	fits       func(ScriptInput) bool
}

var remoteRoutes = []remoteRoute{
	{Kind: providerKindOpenRouter, Model: openRouterModel, ReserveUSD: openRouterReserveUSD, PlanUSD: openRouterPlanUSD,
		Label: "OpenRouter", fits: openRouterInputFits},
	{Kind: providerKindOpenAI, Model: openAIModel, ReserveUSD: openAIReserveUSD, PlanUSD: openAIPlanUSD,
		Label: "OpenAI", fits: openAIInputFits},
}

func routeForKind(kind string) (remoteRoute, bool) {
	for _, r := range remoteRoutes {
		if r.Kind == kind {
			return r, true
		}
	}
	return remoteRoute{}, false
}

func routeForModel(model string) (remoteRoute, bool) {
	for _, r := range remoteRoutes {
		if r.Model == model {
			return r, true
		}
	}
	return remoteRoute{}, false
}

func validProviderKind(kind string) bool {
	_, ok := routeForKind(kind)
	return ok || kind == providerKindFal || kind == providerKindLocalText
}

// remoteTextClient sends one structured script request with a decrypted key.
type remoteTextClient interface {
	Complete(ctx context.Context, key string, in ScriptInput, attempt scriptAttempt) (textCall, error)
}

// remoteKeyTester checks a key without generating or sending user content.
type remoteKeyTester interface {
	TestKey(ctx context.Context, key string) (string, error)
}

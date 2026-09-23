// Package views renders the models and routing screens: G2 Models &
// providers (desktop and the G2m mobile layout from the same DOM), G11
// Routing, G12 Add custom provider (connect → test → describe → route), the
// worker token page and the local model set. Handlers in internal/ai build
// the view models; every string is translated there or here with i18n.T.
package views

import (
	"github.com/a-h/templ"

	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// ProviderCard is one provider card on G2.
type ProviderCard struct {
	ID   string
	Href string
	Icon string
	Name string
	Sub  string
	// State is "ok", "warn" or "off" (shown as a dot plus a word for screen readers).
	State     string
	StateText string
}

// ModelRow is one row of the models table.
type ModelRow struct {
	ID       string
	Name     string
	Maker    string
	Provider string
	Uses     string
	// Status is ready, licence, disabled, not_connected, not_installed, vram, placeholder.
	Status     string
	StatusText string
	Cost       string
	Enabled    bool
	CanToggle  bool
	Note       string
	Custom     bool
}

// ChangeRow is one manifest change for admins.
type ChangeRow struct {
	Change string
	Text   string
	Name   string
	When   string
	New    bool
}

// ModelsView is G2.
type ModelsView struct {
	Host      string
	IsAdmin   bool
	CanAdd    bool
	Cards     []ProviderCard
	Tabs      []ui.SegLink
	Kind      string
	Rows      []ModelRow
	ShowAll   bool
	AllHref   string
	AllCount  int
	Changes   []ChangeRow
	NewCount  int
	LocalOnly bool
	ModeLabel string
	// LimitLabel is "$10" or "Off".
	LimitLabel string
	Problems   []string
	Empty      bool
	// OnlyPlaceholder is true while nothing but the built-in placeholder can run.
	OnlyPlaceholder bool
}

// DecisionRow is one routing decision on G11.
type DecisionRow struct {
	Context  string
	Pick     string
	Why      string
	Status   string
	When     string
	Skipped  []string
	Alts     []string
	Warnings []string
}

// PinRow is one pin.
type PinRow struct {
	ID        string
	Scope     string
	Target    string
	CanDelete bool
}

// ModelOption is a model for the pin form.
type ModelOption struct {
	Value string
	Label string
}

// RoutingView is G11.
type RoutingView struct {
	IsAdmin bool
	Mode    string
	// ModeNote explains whose mode the control sets ("Server default" / "Your own mode").
	ModeNote     string
	ModeAction   string
	Decisions    []DecisionRow
	Pins         []PinRow
	CanPin       bool
	PinScopes    []ui.Option
	PinTasks     []ui.Option
	PinModels    []ui.Option
	PinModes     []ui.Option
	LicenceGuard bool
	LimitOn      bool
	LimitUSD     string
	UserLimitUSD string
	Learn        bool
	UsersMayAdd  bool
	SpentLine    string
	Errors       map[string]string
}

// WizardStep names.
const (
	StepConnect  = "connect"
	StepTest     = "test"
	StepDescribe = "describe"
	StepRoute    = "route"
)

// Steps lists the wizard steps.
var Steps = []string{StepConnect, StepTest, StepDescribe, StepRoute}

// KindChoice is a provider kind on the chooser and the connection-type control.
type KindChoice struct {
	Kind    string
	Name    string
	Desc    string
	Icon    string
	Href    string
	Current bool
	Custom  bool
}

// MapRow is one ComfyUI input mapping select.
type MapRow struct {
	Field   string
	Label   string
	Value   string
	Options []ui.Option
	// More rows are folded under "More inputs".
	More bool
}

// TestView is the test result card.
type TestView struct {
	Ran       bool
	OK        bool
	Status    string
	Time      string
	Cost      string
	ImageURL  string
	Text      string
	Caption   string
	Message   string
	Placehold string
}

// CapField is one capability checkbox.
type CapField struct {
	Name    string
	Label   string
	Checked bool
}

// DescribedModel is a model the provider serves (catalogue or discovered).
type DescribedModel struct {
	ID      string
	Name    string
	Detail  string
	InCat   bool
	Checked bool
}

// WizardView is G12.
type WizardView struct {
	Title    string
	Sub      string
	Step     string
	StepIdx  int
	Steps    []ui.Step
	ID       string
	Kind     string
	KindName string
	Custom   bool
	Types    []KindChoice
	Next     string
	Errors   map[string]string
	FormErr  string

	// Connect fields.
	Name         string
	BaseURL      string
	URLHint      string
	NeedsURL     bool
	NeedsKey     bool
	KeyHint      string
	KeyURL       string
	ModelID      string
	ModelHint    string
	ScopeOptions []ui.Option
	Scope        string
	// ComfyUI.
	WorkflowName  string
	WorkflowNodes int
	Mapping       []MapRow
	// HTTP.
	BodyTemplate string
	ResultPath   string
	ResultType   string
	Mode         string
	IDPath       string
	PollURL      string
	StatusPath   string
	DoneValues   string
	AuthHeader   string
	AuthPrefix   string
	// Hugging Face.
	HFModel string

	// Test.
	Test TestView
	// BothModes shows a second "test with an image" button (providers with text and images).
	BothModes bool
	// Manage shows turn off / remove for a finished provider; Enabled is its state.
	Manage        bool
	Enabled       bool
	NeedsApproval bool
	TestPrompt    string
	Summary       [][2]string

	// Describe.
	DescName      string
	DescModelID   string
	DescKind      string
	KindOptions   []ui.Option
	TaskFields    []CapField
	CapFields     []CapField
	MaxRefs       string
	MaxRes        string
	LicenceID     string
	Commercial    bool
	CostPerImage  string
	Manifest      string
	Served        []DescribedModel
	Discovered    []DescribedModel
	DiscoverError string

	// Route.
	RouteModels []string
	PinScopes   []ui.Option
	PinTasks    []ui.Option
	Explain     string
}

// Href builds a wizard link.
func (v WizardView) Href(step string) templ.SafeURL {
	u := "/models/providers/new/" + step + "?id=" + v.ID
	if v.Next != "" {
		u += "&next=" + v.Next
	}
	return templ.SafeURL(u)
}

// WorkerToken is one worker token row.
type WorkerToken struct {
	ID       string
	Name     string
	Prefix   string
	Created  string
	LastUsed string
}

// WorkerRow is one registered worker.
type WorkerRow struct {
	Name    string
	Status  string
	Detail  string
	Models  string
	Version string
}

// WorkersView is "Local worker → Add worker".
type WorkersView struct {
	IsAdmin  bool
	Tokens   []WorkerToken
	Workers  []WorkerRow
	NewToken string
	NewName  string
	Server   string
	Docker   string
	Pip      string
	Compose  string
	Error    string
	Models   string
}

// LocalModel is one local model on the local set page.
type LocalModel struct {
	ID          string
	Name        string
	Detail      string
	Fits        bool
	Recommended bool
	Installed   bool
	Chosen      bool
	Licence     string
}

// LocalView is the recommended local model set.
type LocalView struct {
	IsAdmin    bool
	GPU        string
	Models     []LocalModel
	Command    string
	DownloadGB string
	Saved      bool
	HasWorker  bool
	// Recommended is true when a set was recommended for the detected GPU.
	Recommended bool
}

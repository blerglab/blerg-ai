package daemon

// codexDriver hosts Codex through the CLI's persistent app-server protocol.
// Thread identities are persisted by the host so restarts can resume the
// same conversation; turn/steer delivers messages during active work.
import (
	"context"
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/models"
	"os/exec"
	"slices"
	"sync"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type codexDriver struct {
	CredentialSource  EngineCredentialSource
	managedCredential codexCredentialState
	workDir           string
	engineModel
	emitter agent.Emitter
	env     []string // sanitised env every `codex` turn starts from
	// prefix routes each turn into the session's sandbox container; empty
	// means the turn runs on the host (see sandbox.go's sandboxExec).
	prefix     sandboxExec
	queue      chan queuedMsg
	retryInput *queuedMsg // explicitly rejected steer, retained ahead of later input across app restart

	rejectedSteerWait time.Duration // zero uses the production completion wait

	mu              sync.Mutex
	onThreadID      func(string)
	onEngine        func(int, bool)
	onOpenWork      func([]agent.BackgroundTaskStartedPayload, []OpenSubagent)
	onKnownChildren func([]OpenSubagent)
	bgTasks         map[string]agent.BackgroundTaskStartedPayload
	adoptionNote    string
	children        map[string]*codexChild
	runtimeCaps     *protocol.CapabilitiesPayload
	modelCatalog    map[string]models.Model
	observedTools   map[string]bool
	caps            capEmitter
	heardFromPerson bool
	interruptNext   bool
	stopped         bool
	activeTurn      bool
	lastLimitTurn   string
	usageTurnID     string
	threadID        string // Codex's durable thread identity.
	// RuntimeConfig is passed to Codex thread configuration (never emitted).
	AskUser               func(context.Context, string) (string, error)
	RestrictedRuntime     *codexRestrictedRuntime
	RuntimeConfig         map[string]any
	NativeStateHome       string // trusted pod conversation-only SQLite directory
	CodexHome             string // private managed-plugin home; never used for restricted sessions
	DeveloperInstructions string
	app                   *codexApp
	cmd                   *exec.Cmd // persistent app-server, for process ownership.
	mcpGeneration         uint64
	mcpInputGate          func(context.Context)
	recoveryInputGate     func(context.Context)
	recoveryInputReady    func() bool

	settingsMu         sync.Mutex // serialize settings mutation and publication; acquire before mu
	settingsGeneration uint64     // successful manual settings controls, including same-value selections
}

// newCodexDriver: env is the environment for every `codex` subprocess; nil
// means sanitizedEnviron() (never the raw daemon env).
func newCodexDriver(o AgentDriverOpts) *codexDriver {
	env := o.Env
	if env == nil {
		env = sanitizedEnviron()
	}
	return &codexDriver{
		CredentialSource: o.CredentialSource, workDir: o.WorkDir, engineModel: newEngineModel(o), emitter: o.Emitter, env: env,
		queue: make(chan queuedMsg, 64),
	}
}

func (d *codexDriver) emit(kind string, payload any) {
	d.updateOpenWork(kind, payload)
	d.emitter.Emit(agent.Event{
		ClientEventID: ccUUID(), Ts: time.Now(), Kind: kind, Payload: payload,
	})
}

func (d *codexDriver) useSandbox(prefix sandboxExec) { d.prefix = prefix }

func (d *codexDriver) Start() {}

// Enqueue accepts a plain message (EnqueueInput).
func (d *codexDriver) Enqueue(text, source string) {
	d.EnqueueInput(agent.Input{Text: text, Source: source})
}

func (d *codexDriver) EnqueueInput(in agent.Input) {
	if in.Source == "" {
		in.Source = "chat"
	}
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		d.emit("error", agent.ErrorPayload{Message: "Codex runtime has stopped; start a new session to resume work", Retryable: true, ClientID: in.ClientID})
		return
	}
	if in.ExpectedTurnID != "" && (in.ExpectedTurnID != d.usageTurnID || d.activeTurn || len(d.queue) > 0 || d.retryInput != nil) {
		d.mu.Unlock()
		d.emit("error", staleLimitResumeError())
		return
	}
	defer d.mu.Unlock()
	if in.Source != "system" {
		d.heardFromPerson = true
	}
	select {
	case d.queue <- queuedMsg{Input: in}:
	default:
		d.emit("error", queueFullError(in))
	}
}

// SetModel applies a model/effort change from the next turn on. Invalid
// values are refused (engineModel.set) — AgentHost checks the same rules
// first; this is the driver not trusting its caller.
func (d *codexDriver) SetModel(model, effort, source string) {
	d.settingsMu.Lock()
	defer d.settingsMu.Unlock()
	d.mu.Lock()
	target := model
	if target == "" {
		target = d.model
	}
	known, reported := d.modelCatalog[target]
	validEffort := !reported || effort == "" || slices.Contains(known.Efforts, effort)
	if model != "" && effort == "" {
		effort = d.reconcileEffortLocked(model, d.effort)
	}
	ok := validEffort && d.set(model, effort)
	if ok && model != "" {
		// A model-only change can reconcile to no effort. The shared setter
		// treats empty as unchanged, so apply this model's resolved value here.
		d.effort = effort
	}
	if ok {
		d.settingsGeneration++
	}
	p := agent.ModelChangedPayload{Model: d.model, Effort: d.effort, Source: source}
	d.mu.Unlock()
	if ok {
		d.emit("model_changed", p)
		d.emitRuntimeCapsLocked()
	}
	if !ok {
		d.emit("error", agent.ErrorPayload{Message: fmt.Sprintf("ignored invalid model %q / effort %q", model, effort), Retryable: true})
	}
}

func (d *codexDriver) Interrupt() {
	d.mu.Lock()
	cmd, prefix, app := d.cmd, d.prefix, d.app
	if app == nil && (len(d.queue) > 0 || d.retryInput != nil) {
		d.interruptNext = true
	}
	d.mu.Unlock()
	if app != nil {
		app.interrupt()
		return
	}
	interruptTurn(prefix, cmd)
}

func (d *codexDriver) Run(ctx context.Context) {
	failures := 0
	for ctx.Err() == nil {
		start := time.Now()
		d.runApp(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			failures = 0
		}
		failures++
		if failures >= 3 {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(failures) * time.Second):
		}
	}
	if ctx.Err() != nil {
		return
	}
	d.mu.Lock()
	d.stopped = true
	retry := d.retryInput
	d.retryInput = nil
	d.mu.Unlock()
	if retry != nil {
		d.emit("error", agent.ErrorPayload{Message: "Codex runtime could not restart; message was not delivered", Retryable: true, ClientID: retry.ClientID})
	}
	for {
		select {
		case msg := <-d.queue:
			d.emit("error", agent.ErrorPayload{Message: "Codex runtime could not restart; message was not delivered", Retryable: true, ClientID: msg.ClientID})
		default:
			d.emit("error", agent.ErrorPayload{Message: "Codex app-server repeatedly failed; update Codex and start a new session"})
			return
		}
	}
}

// codexEvent is the legacy exec refusal envelope, retained for the shared
// provider-limit parser and saved fixtures.
type codexEvent struct {
	Type    string     `json:"type"`
	Message string     `json:"message"`
	Error   codexError `json:"error"`
}

func (d *codexDriver) runtimeWorkDir() string {
	if d.prefix.enabled() {
		return sandboxWorkdir
	}
	return d.workDir
}

func (d *codexDriver) continueAdopted(prompt, shown string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.heardFromPerson || d.stopped {
		return false
	}
	select {
	case d.queue <- queuedMsg{Input: agent.Input{Text: prompt, Shown: shown, Source: "system"}, adoptionContinue: true}:
		return true
	default:
		return false
	}
}
func (d *codexDriver) unlessHeardFromPerson(fn func()) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.heardFromPerson {
		return false
	}
	fn()
	return true
}

func (d *codexDriver) addAdoptionWork(line string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.adoptionNote != "" {
		d.adoptionNote += "\n"
	}
	d.adoptionNote += line
}

func (d *codexDriver) codexToolsRestricted() bool {
	return d.RestrictedRuntime != nil && d.RestrictedRuntime.Restricted
}

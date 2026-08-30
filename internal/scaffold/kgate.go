// This file implements `nexler init kgate [-dir <app-dir>]` — adding a
// kgate (klivolks' message broker) client package to an *existing*
// generated app, plus the KGATE_* env vars it reads, and auto-wiring its
// webhook fallback route. Register(mux) — auto-wired by NewKgate below —
// also resumes every channel already recorded in the registry, in the
// background, the moment it runs (see kgate_templates/kgate.go.tmpl's
// Register), so a freshly generated app needs no further manual wiring
// for subscriptions to survive a restart. ensureKgateResumeAll (below)
// is the `nexler update` retrofit that brings an app scaffolded before
// Register did this up to date.
//
// Unlike `nexler init db`, this needs no live network connection at all:
// it's pure local file scaffolding, same as `nexler create <route>`.
// Unlike `nexler init kpass`, it does require the target app to already
// have a core database connection (-db at `nexler create app` time) —
// Subscribe/Unsubscribe/ResumeAll are backed by the core kgate-channel
// registry (core/kgate_channels.go, generated for every -db app — see
// scaffold.go's NewApp), which needs somewhere to live.
package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// KgateConfig holds the parameters for `nexler init kgate`.
type KgateConfig struct {
	// AppDir is the path to the generated app's root (the directory
	// containing its go.mod). Defaults to "." by the caller if unset.
	AppDir string
}

// kgateData is what's available to kgate_templates/kgate.go.tmpl placeholders.
type kgateData struct {
	ModulePath string
}

// kgateEnvVars are appended to the target app's .env (blank, for the user
// to fill in) by NewKgate, in this fixed order.
var kgateEnvVars = []string{"KGATE_CLIENT_ID", "KGATE_WS_SERVER", "KGATE_HTTP_SERVER", "KGATE_ORIGIN", "KGATE_WEBHOOK_SECRET"}

// NewKgate scaffolds kgate/kgate.go inside cfg.AppDir, ensures its .env
// has the KGATE_* vars declared, and auto-wires kgate.Register (its
// /webhooks/kgate fallback route) into routes/public/public.go.
func NewKgate(cfg KgateConfig) error {
	appDir := cfg.AppDir
	if appDir == "" {
		appDir = "."
	}

	modulePath, err := readModulePath(appDir)
	if err != nil {
		return err
	}

	if _, ok := readCoreDBType(appDir); !ok {
		return fmt.Errorf("%s has no core database connection (no _DB_CORE_TYPE in .env) — kgate's channel registry needs one: re-scaffold with -db, or run \"nexler create app\" with -db in the first place", filepath.Join(appDir, ".env"))
	}

	destFile := filepath.Join(appDir, "kgate", "kgate.go")
	if _, err := os.Stat(destFile); err == nil {
		return fmt.Errorf("%s already exists — edit it directly, or remove it first to regenerate", destFile)
	}

	data := kgateData{ModulePath: modulePath}

	raw, err := kgateTemplateFS.ReadFile(kgateTmpl)
	if err != nil {
		return fmt.Errorf("reading embedded template %s: %w", kgateTmpl, err)
	}
	content, err := processFile(kgateTmpl, raw, data)
	if err != nil {
		return fmt.Errorf("rendering %s: %w", kgateTmpl, err)
	}
	if err := os.MkdirAll(filepath.Dir(destFile), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(destFile), err)
	}
	if err := os.WriteFile(destFile, content, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", destFile, err)
	}

	if err := ensureEnvVars(appDir, kgateEnvVars, "kgate — see kgate/kgate.go (Subscribe/Unsubscribe/ResumeAll/Publish)"); err != nil {
		return fmt.Errorf("generated %s, but could not update .env: %w", destFile, err)
	}

	if err := wireAggregator(appDir, "public", modulePath+"/kgate", "kgate"); err != nil {
		return fmt.Errorf("generated %s, but could not wire routes/public/public.go automatically: %w\nAdd manually in that file:\n  import kgate %q\n  kgate.Register(mux)",
			destFile, err, modulePath+"/kgate")
	}

	if err := writeKgateService(appDir, modulePath); err != nil {
		return fmt.Errorf("generated %s, but could not generate services/kgate/kgate.go: %w", destFile, err)
	}

	if err := wireBlankImport(appDir, "public", modulePath+"/services/kgate"); err != nil {
		return fmt.Errorf("generated services/kgate/kgate.go, but could not wire it into routes/public/public.go automatically: %w\nAdd manually in that file:\n  import _ %q",
			err, modulePath+"/services/kgate")
	}

	return nil
}

// writeKgateService renders services/kgate/kgate.go — a one-time,
// hand-editable home for the app's own event-processing and startup-
// subscription logic, wired into kgate/kgate.go's EventHandler/OnStartup
// hooks via this new package's own init() (so kgate/kgate.go never has to
// import it back — see kgate_service.go.tmpl's own doc comment for why
// that avoids an import cycle). Unlike kgate/kgate.go itself, this file is
// never touched again by `nexler update` once it exists. Errors if it
// already exists, same collision guard as kgate/kgate.go above.
func writeKgateService(appDir, modulePath string) error {
	destFile := filepath.Join(appDir, "services", "kgate", "kgate.go")
	if _, err := os.Stat(destFile); err == nil {
		return fmt.Errorf("%s already exists — edit it directly, or remove it first to regenerate", destFile)
	}

	data := kgateData{ModulePath: modulePath}

	raw, err := kgateTemplateFS.ReadFile(kgateServiceTmpl)
	if err != nil {
		return fmt.Errorf("reading embedded template %s: %w", kgateServiceTmpl, err)
	}
	content, err := processFile(kgateServiceTmpl, raw, data)
	if err != nil {
		return fmt.Errorf("rendering %s: %w", kgateServiceTmpl, err)
	}
	if err := os.MkdirAll(filepath.Dir(destFile), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(destFile), err)
	}
	return os.WriteFile(destFile, content, 0o644)
}

// kgateRegisterOriginal is kgate.go.tmpl's Register function exactly as
// it read before ResumeAll was wired into it automatically — the literal
// anchor ensureKgateResumeAll matches against for its surgical patch.
// Deliberately scoped to the function body only, not its doc comment
// above: a hand-tweaked comment shouldn't cause the patch to fail (it's
// left stale on a retrofitted file, cosmetic-only).
const kgateRegisterOriginal = `func Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /webhooks/kgate", HandleWebhook)
}`

// kgateRegisterPatched is what ensureKgateResumeAll replaces
// kgateRegisterOriginal with — same as the current kgate.go.tmpl.
const kgateRegisterPatched = `func Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /webhooks/kgate", HandleWebhook)
	go func() {
		if err := ResumeAll(context.Background()); err != nil {
			log.Printf("kgate: resuming channels: %v", err)
		}
	}()
}`

// kgateRegisterPatchedV2 is what ensureKgateOpenAPIAndTestSubscribe
// replaces kgateRegisterPatched with — same as the current
// kgate.go.tmpl. Adds the openapi.Register call documenting POST
// /webhooks/kgate (previously invisible in /openapi.json/Swagger UI,
// unlike every route `nexler create <route>` generates) and a startup
// Subscribe(ctx, "test") alongside ResumeAll — a built-in smoke test that
// the whole pipeline (core DB registry + WebSocket connectivity) works
// end to end on a fresh app.
const kgateRegisterPatchedV2 = `func Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /webhooks/kgate", HandleWebhook)
	// Protected is left false (the default): this route authenticates via
	// the HMAC X-Signature check in HandleWebhook above, not
	// middleware.RequireAuth, so Protected: true here would be misleading.
	openapi.Register(openapi.Operation{
		Method:         "POST",
		Path:           "/webhooks/kgate",
		OperationID:    "kgateWebhook",
		Summary:        "POST /webhooks/kgate",
		Tags:           []string{"kgate"},
		ReqType:        webhookEvent{},
		ReqContentType: "application/json",
	})
	go func() {
		if err := ResumeAll(context.Background()); err != nil {
			log.Printf("kgate: resuming channels: %v", err)
		}
		// Subscribes to a channel named "test" as a built-in smoke test —
		// verifies the whole pipeline (core DB registry + WebSocket
		// connectivity) works end to end on a fresh app. Subscribe is
		// idempotent for an already-subscribed channel, so this is safe to
		// leave in; remove it once you've confirmed kgate is wired
		// correctly, or if a permanent "test" channel isn't wanted.
		if err := Subscribe(context.Background(), "test"); err != nil {
			log.Printf("kgate: subscribing to test channel: %v", err)
		}
	}()
}`

// ensureKgateOpenAPIAndTestSubscribe brings an app scaffolded before
// Register documented its webhook route with openapi.Register and
// startup-subscribed to a "test" channel up to date. Anchors on
// kgateRegisterPatched (the body ensureKgateResumeAll's own patch
// produces) rather than kgateRegisterOriginal — this must run after
// ensureKgateResumeAll in updateChecks so that anchor is guaranteed to
// already be in place by the time this one runs. A missing kgate/kgate.go
// is a silent no-op, same precedent as ensureKgateResumeAll. Same
// "sanity-check the anchor, error out instead of guessing" precedent too:
// a hand-rewritten Register fails loud, naming the exact snippet to add
// by hand, rather than being silently overwritten.
func ensureKgateOpenAPIAndTestSubscribe(appDir string) (bool, error) {
	path := filepath.Join(appDir, "kgate", "kgate.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	// OperationID: "kgateWebhook" (rather than the startup Subscribe call
	// itself) is the stable already-applied signal: ensureKgateService
	// Extraction (a later check) rewrites Register's body again to call
	// OnStartup instead of inlining Subscribe(context.Background(), "test")
	// directly, which would otherwise make this check misfire as "not yet
	// applied" on a second `nexler update` run against an app that has
	// since had that later check applied. The OperationID line is present
	// in both kgateRegisterPatchedV2 and kgateRegisterPatchedV3.
	if strings.Contains(content, `OperationID:    "kgateWebhook"`) {
		return false, nil
	}
	if !strings.Contains(content, kgateRegisterPatched) {
		return false, fmt.Errorf("%s: Register doesn't match the known original (has it been hand-rewritten?) — add the following manually:\n%s", path, kgateRegisterPatchedV2)
	}
	content = strings.Replace(content, kgateRegisterPatched, kgateRegisterPatchedV2, 1)

	modulePath, err := readModulePath(appDir)
	if err != nil {
		return false, err
	}
	openapiImport := modulePath + "/openapi"
	if !strings.Contains(content, `"`+openapiImport+`"`) {
		content, err = insertImport(content, "", openapiImport)
		if err != nil {
			return false, fmt.Errorf("%s: adding %q import: %w", path, openapiImport, err)
		}
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// kgateSubscriptionOriginal is kgate.go.tmpl's var block + Subscribe/
// Unsubscribe/ResumeAll/startSubscription/subscribeLoop/subscribeOnce
// exactly as they read before every Subscribe call dialed its own
// per-channel WebSocket connection — the literal anchor
// ensureKgateSharedConnection matches against for its surgical patch.
// Register, HandleWebhook, Publish, and handleEvent are untouched by this
// block and never referenced here.
const kgateSubscriptionOriginal = `var (
	activeMu   sync.Mutex
	activeSubs = map[string]context.CancelFunc{}
)

// Subscribe records channel in the core kgate-channel registry (via
// core.AddKgateChannel, so it survives restarts) and starts a background
// goroutine maintaining a live WebSocket subscription for it — see the
// package doc comment. A second Subscribe call for a channel already
// being listened to is a no-op beyond re-recording it (harmless).
func Subscribe(ctx context.Context, channel string) error {
	if err := core.AddKgateChannel(ctx, channel); err != nil {
		return fmt.Errorf("kgate: recording channel %q: %w", channel, err)
	}
	startSubscription(channel)
	return nil
}

// Unsubscribe stops channel's background subscription goroutine (if
// running) and removes it from the registry.
func Unsubscribe(ctx context.Context, channel string) error {
	activeMu.Lock()
	if cancel, running := activeSubs[channel]; running {
		cancel()
		delete(activeSubs, channel)
	}
	activeMu.Unlock()
	return core.RemoveKgateChannel(ctx, channel)
}

// ResumeAll re-subscribes (each in its own background goroutine) to every
// channel already recorded in the registry, so subscriptions persist
// across restarts. Called automatically by Register (in a background
// goroutine, best-effort) — there's normally no need to call this
// directly yourself.
func ResumeAll(ctx context.Context) error {
	channels, err := core.ListKgateChannels(ctx)
	if err != nil {
		return fmt.Errorf("kgate: listing channels: %w", err)
	}
	for _, ch := range channels {
		startSubscription(ch)
	}
	return nil
}

// startSubscription starts channel's background reconnect-loop goroutine,
// unless one is already running for it.
func startSubscription(channel string) {
	activeMu.Lock()
	defer activeMu.Unlock()
	if _, running := activeSubs[channel]; running {
		return
	}
	subCtx, cancel := context.WithCancel(context.Background())
	activeSubs[channel] = cancel
	go subscribeLoop(subCtx, channel)
}

// subscribeLoop keeps a live WebSocket subscription to channel for as
// long as ctx isn't canceled (by Unsubscribe), retrying with capped
// exponential backoff on any connection error instead of giving up after
// one failure.
func subscribeLoop(ctx context.Context, channel string) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for ctx.Err() == nil {
		connected := subscribeOnce(ctx, channel)
		if ctx.Err() != nil {
			return
		}
		if connected {
			backoff = time.Second
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// subscribeOnce dials KGATE_WS_SERVER, subscribes to channel, and reads
// events (dispatching each to handleEvent, then acking it on success)
// until the connection drops or ctx is canceled. connected reports
// whether the dial itself succeeded, so subscribeLoop only resets its
// backoff after an actual successful connection, not a failed dial.
func subscribeOnce(ctx context.Context, channel string) (connected bool) {
	wsURL := os.Getenv("KGATE_WS_SERVER")
	if wsURL == "" {
		return false
	}
	header := http.Header{}
	header.Set("X-Client-Id", os.Getenv("KGATE_CLIENT_ID"))
	header.Set("Origin", os.Getenv("KGATE_ORIGIN"))

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, header)
	if err != nil {
		return false
	}
	defer conn.Close()

	// ReadJSON below blocks with no ctx awareness of its own — closing
	// the connection is what unblocks it once Unsubscribe cancels ctx.
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	if err := conn.WriteJSON(subscribeFrame{Type: "subscribe", Channel: channel}); err != nil {
		return true
	}

	for {
		var frame inboundFrame
		if err := conn.ReadJSON(&frame); err != nil {
			return true
		}
		if frame.Type != "event" {
			continue
		}
		if err := handleEvent(ctx, frame.Channel, frame.Payload); err != nil {
			continue // don't ack — leave it for kgate to redeliver
		}
		_ = conn.WriteJSON(ackFrame{Type: "ack", Channel: frame.Channel, MessageID: frame.MessageID})
	}
}`

// kgateSubscriptionPatched is what ensureKgateSharedConnection replaces
// kgateSubscriptionOriginal with — same as the current kgate.go.tmpl.
// Multiplexes every subscribed channel over a single shared WebSocket
// connection instead of dialing one connection per channel: kgate's
// gateway allows only one live connection per X-Client-Id, so the
// original per-channel design caused each new Subscribe to evict the
// previous connection, an endless reconnect churn.
const kgateSubscriptionPatched = `var (
	// subMu guards channels, the set of channels this app should be
	// subscribed to (mirrors the core_kgate_channels registry).
	subMu    sync.Mutex
	channels = map[string]struct{}{}
	started  bool

	// connMu guards active (the live shared connection, nil when
	// disconnected) and serializes every write to it — gorilla/websocket
	// permits only one concurrent writer per connection.
	connMu sync.Mutex
	active *websocket.Conn
)

// Subscribe records channel in the core kgate-channel registry (via
// core.AddKgateChannel, so it survives restarts) and adds it to the
// shared WebSocket connection — see the package doc comment. A second
// Subscribe call for a channel already being listened to is a no-op
// beyond re-recording it (harmless).
func Subscribe(ctx context.Context, channel string) error {
	if err := core.AddKgateChannel(ctx, channel); err != nil {
		return fmt.Errorf("kgate: recording channel %q: %w", channel, err)
	}

	subMu.Lock()
	_, already := channels[channel]
	channels[channel] = struct{}{}
	subMu.Unlock()

	if !already {
		sendSubscribe(channel)
	}
	ensureLoopStarted()
	return nil
}

// Unsubscribe removes channel from the registry and from the set of
// channels this app subscribes to on (re)connect. It does not tear down
// the shared connection — other channels may still be using it — so any
// event kgate delivers for channel between this call and its next
// reconnect is still read off the wire, just not dispatched to
// Subscribe's caller again (kgate has no per-channel unsubscribe frame to
// send it instead).
func Unsubscribe(ctx context.Context, channel string) error {
	subMu.Lock()
	delete(channels, channel)
	subMu.Unlock()
	return core.RemoveKgateChannel(ctx, channel)
}

// ResumeAll seeds the shared subscription set with every channel already
// recorded in the registry and starts the shared connection loop, so
// subscriptions persist across restarts. Called automatically by Register
// (in a background goroutine, best-effort) — there's normally no need to
// call this directly yourself.
func ResumeAll(ctx context.Context) error {
	recorded, err := core.ListKgateChannels(ctx)
	if err != nil {
		return fmt.Errorf("kgate: listing channels: %w", err)
	}
	if len(recorded) == 0 {
		return nil
	}
	subMu.Lock()
	for _, ch := range recorded {
		channels[ch] = struct{}{}
	}
	subMu.Unlock()
	ensureLoopStarted()
	return nil
}

// ensureLoopStarted starts the single shared reconnect-loop goroutine,
// unless it's already running.
func ensureLoopStarted() {
	subMu.Lock()
	defer subMu.Unlock()
	if started {
		return
	}
	started = true
	go subscribeLoop()
}

// sendSubscribe writes a subscribe frame for channel over the shared
// connection if one is currently up. If not, it's a no-op: subscribeOnce
// sends a subscribe frame for every recorded channel as soon as it
// (re)connects, so channel is picked up on the next connect regardless.
func sendSubscribe(channel string) {
	connMu.Lock()
	defer connMu.Unlock()
	if active == nil {
		return
	}
	if err := active.WriteJSON(subscribeFrame{Type: "subscribe", Channel: channel}); err != nil {
		log.Printf("kgate: %v", err)
	}
}

// subscribeLoop keeps the single shared WebSocket connection up for the
// life of the process, retrying with capped exponential backoff on any
// connection error instead of giving up after one failure.
func subscribeLoop() {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		connected := subscribeOnce()
		if connected {
			backoff = time.Second
		}

		time.Sleep(backoff)
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// subscribeOnce dials KGATE_WS_SERVER, subscribes to every channel
// currently in the shared set, and reads events (dispatching each to
// handleEvent, then acking it on success) until the connection drops.
// connected reports whether the dial itself succeeded, so subscribeLoop
// only resets its backoff after an actual successful connection, not a
// failed dial.
func subscribeOnce() (connected bool) {
	wsURL := os.Getenv("KGATE_WS_SERVER")
	if wsURL == "" {
		return false
	}
	header := http.Header{}
	header.Set("X-Client-Id", os.Getenv("KGATE_CLIENT_ID"))
	header.Set("Origin", os.Getenv("KGATE_ORIGIN"))

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		log.Printf("kgate: %v", err)
		return false
	}
	defer conn.Close()

	subMu.Lock()
	subs := make([]string, 0, len(channels))
	for ch := range channels {
		subs = append(subs, ch)
	}
	subMu.Unlock()

	connMu.Lock()
	active = conn
	for _, ch := range subs {
		if err := conn.WriteJSON(subscribeFrame{Type: "subscribe", Channel: ch}); err != nil {
			connMu.Unlock()
			log.Printf("kgate: %v", err)
			return true
		}
	}
	connMu.Unlock()
	defer func() {
		connMu.Lock()
		if active == conn {
			active = nil
		}
		connMu.Unlock()
	}()

	for {
		var frame inboundFrame
		if err := conn.ReadJSON(&frame); err != nil {
			return true
		}
		if frame.Type != "event" {
			continue
		}
		if err := handleEvent(context.Background(), frame.Channel, frame.Payload); err != nil {
			continue // don't ack — leave it for kgate to redeliver
		}
		connMu.Lock()
		_ = conn.WriteJSON(ackFrame{Type: "ack", Channel: frame.Channel, MessageID: frame.MessageID})
		connMu.Unlock()
	}
}`

// ensureKgateSharedConnection brings an app scaffolded before Subscribe
// multiplexed every channel over one shared WebSocket connection up to
// date. Before this fix, every Subscribe call dialed its own connection —
// harmless with one channel, but kgate's gateway allows only one live
// connection per X-Client-Id, so a second concurrently-subscribed channel
// evicted the first, an endless reconnect churn. A missing kgate/kgate.go
// is a silent no-op, same precedent as every other kgate retrofit here.
//
// Scoped to the var block + Subscribe/Unsubscribe/ResumeAll/
// startSubscription/subscribeLoop/subscribeOnce only — Register,
// HandleWebhook, Publish, and handleEvent (the documented hand-edit
// point) are never touched, so this is safe to run regardless of whether
// handleEvent has been customized. Same "sanity-check the anchor, error
// out instead of guessing" precedent as every other kgate/mongo patch in
// this repo: a block that doesn't match the known original fails loud,
// naming the file and the exact snippet to add by hand, rather than being
// silently overwritten.
func ensureKgateSharedConnection(appDir string) (bool, error) {
	path := filepath.Join(appDir, "kgate", "kgate.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if strings.Contains(content, "ensureLoopStarted") {
		return false, nil
	}
	if !strings.Contains(content, kgateSubscriptionOriginal) {
		return false, fmt.Errorf("%s: Subscribe/Unsubscribe/ResumeAll don't match the known original (has it been hand-rewritten?) — add the following manually:\n%s", path, kgateSubscriptionPatched)
	}
	content = strings.Replace(content, kgateSubscriptionOriginal, kgateSubscriptionPatched, 1)

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// ensureKgateResumeAll brings an app scaffolded before Register started
// auto-resuming recorded channels up to date. A missing kgate/kgate.go
// (the app never ran `init kgate`) is a silent no-op, same precedent as
// every other `ensure*` retrofit skipping a feature the app never had.
//
// Deliberately a narrow, surgical patch — never a full-file regeneration
// like ensureAuthSubjectContext/ensureJWTClaims use for their own
// pure-generated-infra targets: handleEvent in this file is the
// documented, expected hand-edit point (real event-processing business
// logic lives there), so nothing here may risk touching it. A sanity
// check (kgateRegisterOriginal must match verbatim) guards against
// silently overwriting a Register that's been hand-rewritten beyond
// recognition — that case errors out instead, naming the file and the
// exact snippet to add by hand.
func ensureKgateResumeAll(appDir string) (bool, error) {
	path := filepath.Join(appDir, "kgate", "kgate.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if strings.Contains(content, "ResumeAll(context.Background())") {
		return false, nil
	}
	if !strings.Contains(content, kgateRegisterOriginal) {
		return false, fmt.Errorf("%s: Register doesn't match the known original (has it been hand-rewritten?) — add the following manually:\n%s", path, kgateRegisterPatched)
	}
	content = strings.Replace(content, kgateRegisterOriginal, kgateRegisterPatched, 1)

	if !strings.Contains(content, `"log"`) {
		content, err = insertImport(content, "", "log")
		if err != nil {
			return false, fmt.Errorf("%s: adding \"log\" import: %w", path, err)
		}
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// kgatePublishBodyOriginal is kgate.go.tmpl's publishBody struct exactly
// as it read before Publish JSON-encoded its payload before sending — the
// literal anchor ensureKgatePublishEncoding matches against.
const kgatePublishBodyOriginal = `type publishBody struct {
	Channel string ` + "`" + `json:"channel"` + "`" + `
	Payload any    ` + "`" + `json:"payload"` + "`" + `
}
`

// kgatePublishBodyPatched is what ensureKgatePublishEncoding replaces
// kgatePublishBodyOriginal with — same as the current kgate.go.tmpl.
const kgatePublishBodyPatched = `type publishBody struct {
	Channel string ` + "`" + `json:"channel"` + "`" + `
	// Payload is always a JSON-encoded string, per kgate's wire contract:
	// kgate passes it through byte-for-byte to subscribers, which is
	// exactly why unwrapPayload below has to undo this same encoding on
	// the receive side. Passing an unencoded value here instead — a raw
	// struct/map rather than the string form of its JSON encoding — will
	// get rejected by kgate's publish endpoint, not silently accepted in
	// a different shape.
	Payload string ` + "`" + `json:"payload"` + "`" + `
}
`

// kgatePublishFuncOriginal is kgate.go.tmpl's Publish exactly as it read
// before it JSON-encoded its payload and sent an Origin header — the
// literal anchor ensureKgatePublishEncoding matches against. Deliberately
// scoped to the function body only, not its doc comment above.
const kgatePublishFuncOriginal = `func Publish(ctx context.Context, channel string, payload any) error {
	base := os.Getenv("KGATE_HTTP_SERVER")
	if base == "" {
		return fmt.Errorf("kgate: KGATE_HTTP_SERVER is not set")
	}
	headers := map[string]string{"X-Client-Id": os.Getenv("KGATE_CLIENT_ID")}

	resp, err := apiclient.Post(ctx, strings.TrimRight(base, "/")+"/messenger/publish", headers, publishBody{Channel: channel, Payload: payload})
	if err != nil {
		return fmt.Errorf("kgate: publishing to %s: %w", channel, err)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("kgate: publish to %s: unexpected status %d", channel, resp.StatusCode)
	}
	return nil
}
`

// kgatePublishFuncPatched is what ensureKgatePublishEncoding replaces
// kgatePublishFuncOriginal with — same as the current kgate.go.tmpl.
// kgate's gateway requires an Origin header alongside X-Client-Id, and
// publishBody's Payload must be a JSON-encoded string on the wire (see
// unwrapPayload's doc comment for why the receive side has to undo this
// same encoding).
const kgatePublishFuncPatched = `func Publish(ctx context.Context, channel string, payload any) error {
	base := os.Getenv("KGATE_HTTP_SERVER")
	if base == "" {
		return fmt.Errorf("kgate: KGATE_HTTP_SERVER is not set")
	}
	headers := map[string]string{
		"X-Client-Id": os.Getenv("KGATE_CLIENT_ID"),
		"Origin":      os.Getenv("KGATE_ORIGIN"),
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("kgate: encoding payload for %s: %w", channel, err)
	}

	resp, err := apiclient.Post(ctx, strings.TrimRight(base, "/")+"/messenger/publish", headers, publishBody{Channel: channel, Payload: string(encoded)})
	if err != nil {
		return fmt.Errorf("kgate: publishing to %s: %w", channel, err)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("kgate: publish to %s: unexpected status %d", channel, resp.StatusCode)
	}
	return nil
}
`

// ensureKgatePublishEncoding brings an app scaffolded before Publish
// JSON-encoded its payload (and sent an Origin header) up to date —
// kgate's gateway requires Origin alongside X-Client-Id, and publishBody's
// Payload must be a JSON-encoded string on the wire (see unwrapPayload's
// doc comment for why the receive side has to undo this same encoding). A
// missing kgate/kgate.go is a silent no-op, same precedent as every other
// kgate retrofit here.
func ensureKgatePublishEncoding(appDir string) (bool, error) {
	path := filepath.Join(appDir, "kgate", "kgate.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if strings.Contains(content, "encoding payload for") {
		return false, nil
	}

	if !strings.Contains(content, kgatePublishBodyOriginal) {
		return false, fmt.Errorf("%s: publishBody doesn't match the known original (has it been hand-rewritten?) — add the following manually:\n%s", path, kgatePublishBodyPatched)
	}
	content = strings.Replace(content, kgatePublishBodyOriginal, kgatePublishBodyPatched, 1)

	if !strings.Contains(content, kgatePublishFuncOriginal) {
		return false, fmt.Errorf("%s: Publish doesn't match the known original (has it been hand-rewritten?) — add the following manually:\n%s", path, kgatePublishFuncPatched)
	}
	content = strings.Replace(content, kgatePublishFuncOriginal, kgatePublishFuncPatched, 1)

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// kgateResilientPatched is what ensureKgateResilientDelivery replaces
// kgateSubscriptionPatched with — same as the current kgate.go.tmpl (plus
// the package-level logger var, placed here instead of its canonical
// position right after imports — a harmless cosmetic difference, since Go
// doesn't care about package-level declaration order). Adds structured
// slog logging throughout, a bounded WebSocket dial timeout, ping/pong
// keepalive, panic-safe dispatch (runSubscribeOnce/dispatchEvent),
// permanent-vs-transient error handling (permanentError/permanentf/
// isPermanent), and payload unwrapping (unwrapPayload). Register,
// HandleWebhook, Publish, and handleEvent are untouched by this block —
// handleEvent (the documented hand-edit point) is never touched by this
// or any other anchor-based kgate retrofit; ensureKgateWebhookDispatch
// (below) separately upgrades HandleWebhook's own call site once this
// check has run.
const kgateResilientPatched = `// logger is this package's structured logger for connection-lifecycle
// events (see the package doc's Logging section). It writes JSON lines to
// stdout tagged component=kgate — whatever log stack collects this
// process's stdout picks these up with no extra wiring.
var logger = slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("component", "kgate")

var (
	// subMu guards channels, the set of channels this app should be
	// subscribed to (mirrors the core_kgate_channels registry).
	subMu    sync.Mutex
	channels = map[string]struct{}{}
	started  bool

	// connMu guards active (the live shared connection, nil when
	// disconnected) and serializes every write to it — gorilla/websocket
	// permits only one concurrent writer per connection.
	connMu sync.Mutex
	active *websocket.Conn
)

// Subscribe records channel in the core kgate-channel registry (via
// core.AddKgateChannel, so it survives restarts) and adds it to the
// shared WebSocket connection — see the package doc comment. A second
// Subscribe call for a channel already being listened to is a no-op
// beyond re-recording it (harmless).
func Subscribe(ctx context.Context, channel string) error {
	if err := core.AddKgateChannel(ctx, channel); err != nil {
		return fmt.Errorf("kgate: recording channel %q: %w", channel, err)
	}

	subMu.Lock()
	_, already := channels[channel]
	channels[channel] = struct{}{}
	subMu.Unlock()

	if !already {
		sendSubscribe(channel)
	}
	ensureLoopStarted()
	return nil
}

// Unsubscribe removes channel from the registry and from the set of
// channels this app subscribes to on (re)connect. It does not tear down
// the shared connection — other channels may still be using it — so any
// event kgate delivers for channel between this call and its next
// reconnect is still read off the wire, just not dispatched to
// Subscribe's caller again (kgate has no per-channel unsubscribe frame to
// send it instead).
func Unsubscribe(ctx context.Context, channel string) error {
	subMu.Lock()
	delete(channels, channel)
	subMu.Unlock()
	return core.RemoveKgateChannel(ctx, channel)
}

// ResumeAll seeds the shared subscription set with every channel already
// recorded in the registry and starts the shared connection loop, so
// subscriptions persist across restarts. Called automatically by Register
// (in a background goroutine, best-effort) — there's normally no need to
// call this directly yourself.
func ResumeAll(ctx context.Context) error {
	recorded, err := core.ListKgateChannels(ctx)
	if err != nil {
		return fmt.Errorf("kgate: listing channels: %w", err)
	}
	if len(recorded) == 0 {
		return nil
	}
	subMu.Lock()
	for _, ch := range recorded {
		channels[ch] = struct{}{}
	}
	subMu.Unlock()
	ensureLoopStarted()
	return nil
}

// ensureLoopStarted starts the single shared reconnect-loop goroutine,
// unless it's already running.
func ensureLoopStarted() {
	subMu.Lock()
	defer subMu.Unlock()
	if started {
		return
	}
	started = true
	go subscribeLoop()
}

// sendSubscribe writes a subscribe frame for channel over the shared
// connection if one is currently up. If not, it's a no-op: subscribeOnce
// sends a subscribe frame for every recorded channel as soon as it
// (re)connects, so channel is picked up on the next connect regardless.
func sendSubscribe(channel string) {
	connMu.Lock()
	defer connMu.Unlock()
	if active == nil {
		return
	}
	if err := active.WriteJSON(subscribeFrame{Type: "subscribe", Channel: channel}); err != nil {
		logger.Warn("subscribe frame write failed", "channel", channel, "error", err.Error())
	}
}

// subscribeLoop keeps the single shared WebSocket connection up for the
// life of the process. It reconnects unconditionally on every exit from
// runSubscribeOnce — a clean disconnect, an abnormal one, a dial failure,
// or (via the recover below) even a panic somewhere in that call the rest
// of this package didn't anticipate — with capped exponential backoff so
// it never gives up. This loop itself must never return: it's the only
// thing standing between one bad connection attempt and this app going
// permanently silent with nothing in the log to explain why.
func subscribeLoop() {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		connected := runSubscribeOnce()
		if connected {
			backoff = time.Second
		} else {
			logger.Info("reconnecting", "stage", "backoff", "delay", backoff.String())
		}

		time.Sleep(backoff)
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// runSubscribeOnce calls subscribeOnce with a recover() around it, so a
// panic anywhere in connection setup or the read loop — not just inside
// handleEvent, which dispatchEvent already guards — logs and is treated
// as an ordinary disconnect (return false, triggering backoff and retry)
// rather than taking down the goroutine subscribeLoop depends on.
func runSubscribeOnce() (connected bool) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("subscribeOnce panicked", "stage", "connect", "panic", fmt.Sprintf("%v", r), "stack", string(debug.Stack()))
			connected = false
		}
	}()
	return subscribeOnce()
}

// subscribeOnce dials KGATE_WS_SERVER, subscribes to every channel
// currently in the shared set, and reads events (dispatching each to
// handleEvent via dispatchEvent, then acking it on success) until the
// connection drops. connected reports whether the dial itself succeeded,
// so subscribeLoop only resets its backoff after an actual successful
// connection, not a failed dial.
func subscribeOnce() (connected bool) {
	wsURL := os.Getenv("KGATE_WS_SERVER")
	if wsURL == "" {
		return false
	}
	header := http.Header{}
	header.Set("X-Client-Id", os.Getenv("KGATE_CLIENT_ID"))
	header.Set("Origin", os.Getenv("KGATE_ORIGIN"))

	// A bounded dial timeout, separate from gorilla's own
	// HandshakeTimeout, guarantees this call returns within 15s even if
	// something in between (DNS, a half-dead proxy) hangs rather than
	// erroring — so subscribeLoop's reconnect loop can never stall
	// indefinitely on a single dial attempt.
	dialCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, _, err := websocket.DefaultDialer.DialContext(dialCtx, wsURL, header)
	if err != nil {
		logger.Warn("dial failed", "stage", "dial", "error", err.Error())
		return false
	}
	defer conn.Close()

	logger.Info("connected", "stage", "connect")

	// Keep the connection alive. A 5s ping cadence with a 15s read
	// deadline, not the more conventional 20s/60s: measured behavior
	// against a live kgate deployment showed a socket dying after just
	// ~10s of silence following a burst of deliveries — well before a
	// slower cadence would ever get a chance to send anything. Something
	// in the path (a proxy/LB idle timeout, or kgate's own server
	// closing subscribers with nothing pending) times out an idle
	// connection faster than you'd expect. If your deployment's actual
	// idle-timeout value turns out to be different, tune these to match
	// it — this is a safe default, not a guarantee.
	const (
		pingPeriod = 5 * time.Second
		pongWait   = 15 * time.Second
	)

	// Any pong from the server extends the read deadline.
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	// Send WebSocket ping frames periodically.
	done := make(chan struct{})
	defer close(done)

	go func() {
		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				connMu.Lock()
				err := conn.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(5*time.Second))
				connMu.Unlock()

				if err != nil {
					logger.Warn("ping failed", "stage", "ping", "error", err.Error())
					// A ping failure means the write side is already
					// broken, but the read loop below may still be
					// blocked waiting up to pongWait for data that will
					// never come (a write-only failure doesn't always
					// surface as a read error). Force-closing here wakes
					// ReadJSON immediately with an error instead of
					// leaving the connection in a known-dead state for
					// up to pongWait longer than necessary.
					conn.Close()
					return
				}

			case <-done:
				return
			}
		}
	}()

	subMu.Lock()
	subs := make([]string, 0, len(channels))
	for ch := range channels {
		subs = append(subs, ch)
	}
	subMu.Unlock()

	connMu.Lock()
	active = conn
	for _, ch := range subs {
		if err := conn.WriteJSON(subscribeFrame{Type: "subscribe", Channel: ch}); err != nil {
			connMu.Unlock()
			logger.Warn("subscribe frame write failed", "stage", "subscribe", "channel", ch, "error", err.Error())
			return true
		}
	}
	connMu.Unlock()
	logger.Info("subscribed", "stage", "subscribe", "channels", subs)

	defer func() {
		connMu.Lock()
		if active == conn {
			active = nil
		}
		connMu.Unlock()
	}()

	for {
		var frame inboundFrame
		if err := conn.ReadJSON(&frame); err != nil {
			logDisconnect(err)
			return true
		}
		if frame.Type != "event" {
			continue
		}

		logger.Info("event received", "stage", "receive", "channel", frame.Channel, "message_id", frame.MessageID)

		procErr := dispatchEvent(context.Background(), frame.Channel, frame.MessageID, frame.Payload)
		switch {
		case procErr != nil && isPermanent(procErr):
			// Acked anyway, deliberately: see permanentError's doc
			// comment. Still logged at ERROR so it isn't silently
			// dropped.
			logger.Error("event undecodable, acking to stop redelivery", "stage", "process", "channel", frame.Channel, "message_id", frame.MessageID, "error", procErr.Error())
		case procErr != nil:
			// Not acked: leaving it unacked means kgate may redeliver
			// it, giving a transient failure a chance to succeed on retry.
			logger.Error("event processing failed", "stage", "process", "channel", frame.Channel, "message_id", frame.MessageID, "error", procErr.Error())
			continue
		default:
			logger.Info("event processed", "stage", "process", "channel", frame.Channel, "message_id", frame.MessageID)
		}

		connMu.Lock()
		ackErr := conn.WriteJSON(ackFrame{Type: "ack", Channel: frame.Channel, MessageID: frame.MessageID})
		connMu.Unlock()

		if ackErr != nil {
			logger.Error("ack write failed", "stage", "ack", "channel", frame.Channel, "message_id", frame.MessageID, "error", ackErr.Error())
		} else {
			logger.Info("ack sent", "stage", "ack", "channel", frame.Channel, "message_id", frame.MessageID)
		}
	}
}

// logDisconnect logs a ReadJSON failure, distinguishing a clean WebSocket
// close (with its code and reason) from an abnormal one (code 1006 — no
// close frame at all, meaning the TCP connection was cut from underneath
// this process rather than closed by kgate itself) so it's possible to
// tell "kgate closed us deliberately" from "the network/a proxy killed
// the connection" straight from the log.
func logDisconnect(err error) {
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) {
		logger.Warn("disconnected", "stage", "disconnect", "close_code", closeErr.Code, "close_text", closeErr.Text)
		return
	}
	logger.Warn("disconnected", "stage", "disconnect", "error", err.Error())
}

// permanentError marks an event-processing failure as one no amount of
// redelivery can fix — a malformed payload that will never successfully
// decode, for instance, as opposed to a downstream dependency (a database
// call, an external API) that might succeed if retried a moment later.
// The distinction matters specifically because of how kgate redelivers:
// an unacked event may come back on every future reconnect, so leaving a
// permanently-undecodable event unacked means that exact event resurfaces
// as a fresh "processing failed" log line on literally every reconnect
// for the rest of the process's life. Wrap a decode error in handleEvent
// with permanentf rather than fmt.Errorf so it gets acked once and logged
// loudly, instead of recurring forever.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// permanentf wraps a formatted error as permanent — see permanentError.
func permanentf(format string, args ...any) error {
	return &permanentError{fmt.Errorf(format, args...)}
}

// isPermanent reports whether err (or anything it wraps) was marked
// permanent by permanentf.
func isPermanent(err error) bool {
	var p *permanentError
	return errors.As(err, &p)
}

// dispatchEvent calls handleEvent, recovering any panic it raises so a
// single malformed or unexpected event can never take down this
// goroutine — and, since this goroutine backs the process's only
// WebSocket connection with no other supervisor watching it, can never
// take down the whole process either.
func dispatchEvent(ctx context.Context, channel, messageID string, payload json.RawMessage) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
			logger.Error("event handler panicked",
				"stage", "process",
				"channel", channel,
				"message_id", messageID,
				"panic", fmt.Sprintf("%v", r),
				"stack", string(debug.Stack()),
			)
		}
	}()
	return handleEvent(ctx, channel, payload)
}

// unwrapPayload undoes kgate's JSON string-encoding of every delivered
// payload — the wire format is ` + "`" + `{"channel": "...", "payload":
// "<JSON-encoded string>"}` + "`" + `, and kgate passes a publisher's payload
// through byte-for-byte, so every event arrives here as a JSON string
// whose *content* is the real object/array, not the object/array itself.
// Call this on payload before decoding it in handleEvent — every
// json.Unmarshal(payload, ...) will otherwise fail with "cannot unmarshal
// string into Go value of type ...". Unwrapped structurally (does it
// parse as a JSON string?) rather than assumed, so a frame that ever
// arrives already-unwrapped still decodes instead of erroring. Recurses
// in case of more than one encoding layer; stops and returns raw as soon
// as it no longer decodes as a JSON string.
func unwrapPayload(raw json.RawMessage) json.RawMessage {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return raw
	}
	return unwrapPayload(json.RawMessage(s))
}
`

// ensureKgateResilientDelivery brings an app scaffolded before kgate's
// structured logging/resilient-delivery revision up to date. Must run
// after ensureKgateSharedConnection in updateChecks, since its anchor
// (kgateSubscriptionPatched) is that check's own output. A missing
// kgate/kgate.go is a silent no-op, same precedent as every other kgate
// retrofit here.
//
// Scoped to the var block + Subscribe/Unsubscribe/ResumeAll/
// ensureLoopStarted/sendSubscribe/subscribeLoop/subscribeOnce, plus the
// new supporting functions appended right after (logDisconnect,
// permanentError, permanentf, isPermanent, dispatchEvent, unwrapPayload)
// — Register, HandleWebhook, Publish, and handleEvent (the documented
// hand-edit point) are never touched, so this is safe to run regardless
// of whether handleEvent has been customized.
func ensureKgateResilientDelivery(appDir string) (bool, error) {
	path := filepath.Join(appDir, "kgate", "kgate.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if strings.Contains(content, "runSubscribeOnce") {
		return false, nil
	}
	if !strings.Contains(content, kgateSubscriptionPatched) {
		return false, fmt.Errorf("%s: Subscribe/Unsubscribe/ResumeAll don't match the known original (has it been hand-rewritten?) — add the following manually:\n%s", path, kgateResilientPatched)
	}
	content = strings.Replace(content, kgateSubscriptionPatched, kgateResilientPatched, 1)

	for _, imp := range []string{"errors", "log/slog", "runtime/debug"} {
		if !strings.Contains(content, `"`+imp+`"`) {
			content, err = insertImport(content, "", imp)
			if err != nil {
				return false, fmt.Errorf("%s: adding %q import: %w", path, imp, err)
			}
		}
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// kgateWebhookOriginal is kgate.go.tmpl's HandleWebhook exactly as it read
// before it dispatched through dispatchEvent — the literal anchor
// ensureKgateWebhookDispatch matches against.
const kgateWebhookOriginal = `func HandleWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "reading body", http.StatusBadRequest)
		return
	}

	secret := os.Getenv("KGATE_WEBHOOK_SECRET")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(r.Header.Get("X-Signature"))) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var event webhookEvent
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	if err := handleEvent(r.Context(), event.Channel, event.Payload); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
`

// kgateWebhookPatched is what ensureKgateWebhookDispatch replaces
// kgateWebhookOriginal with — same as the current kgate.go.tmpl. Routes
// through dispatchEvent (panic-safe) instead of calling handleEvent
// directly, and maps a permanent error to a 400 response instead of a 500.
const kgateWebhookPatched = `func HandleWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "reading body", http.StatusBadRequest)
		return
	}

	secret := os.Getenv("KGATE_WEBHOOK_SECRET")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(r.Header.Get("X-Signature"))) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var event webhookEvent
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	if err := dispatchEvent(r.Context(), event.Channel, "", event.Payload); err != nil {
		if isPermanent(err) {
			http.Error(w, "undecodable payload", http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
`

// ensureKgateWebhookDispatch brings an app scaffolded before HandleWebhook
// routed through dispatchEvent up to date. Must run after
// ensureKgateResilientDelivery in updateChecks, since dispatchEvent/
// isPermanent don't exist until that check has applied. A missing
// kgate/kgate.go is a silent no-op, same precedent as every other kgate
// retrofit here.
func ensureKgateWebhookDispatch(appDir string) (bool, error) {
	path := filepath.Join(appDir, "kgate", "kgate.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if strings.Contains(content, `dispatchEvent(r.Context(), event.Channel, "", event.Payload)`) {
		return false, nil
	}
	if !strings.Contains(content, kgateWebhookOriginal) {
		return false, fmt.Errorf("%s: HandleWebhook doesn't match the known original (has it been hand-rewritten?) — add the following manually:\n%s", path, kgateWebhookPatched)
	}
	content = strings.Replace(content, kgateWebhookOriginal, kgateWebhookPatched, 1)

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// kgateHandleEventOriginalLegacy is kgate.go.tmpl's handleEvent exactly as
// it read before ensureKgateResilientDelivery's unwrapPayload/permanentf
// revision — i.e. an app that never ran an intermediate `nexler update`
// between that revision and this one still has this even older shape by
// the time ensureKgateServiceExtraction runs. Tried as a second candidate
// "untouched" anchor alongside kgateHandleEventOriginal below.
const kgateHandleEventOriginalLegacy = `func handleEvent(ctx context.Context, channel string, payload json.RawMessage) error {
	// TODO: process the event
	return nil
}`

// kgateHandleEventOriginal is kgate.go.tmpl's handleEvent exactly as it
// read before EventHandler/OnStartup existed — the literal anchor
// ensureKgateServiceExtraction matches against. Must match exactly: this
// is the "is handleEvent still untouched?" check that decides whether the
// extraction can be applied automatically (see kgateHandleEventPatched).
const kgateHandleEventOriginal = `func handleEvent(ctx context.Context, channel string, payload json.RawMessage) error {
	payload = unwrapPayload(payload)
	// TODO: process the event. Example shape for a channel with a typed
	// payload:
	//
	//   case "some.channel":
	//       var e someEventType
	//       if err := json.Unmarshal(payload, &e); err != nil {
	//           return permanentf("decoding some.channel payload: %w", err)
	//       }
	//       return doSomethingWith(ctx, e)
	_ = payload
	return nil
}
`

// kgateHandleEventPatched is what ensureKgateServiceExtraction replaces
// kgateHandleEventOriginal with — same as the current kgate.go.tmpl.
// Turns handleEvent into a stable delegator over the new EventHandler
// package variable (and adds the sibling OnStartup variable Register's
// patched body below calls), so kgate/kgate.go never needs to change
// again for business-logic reasons — services/kgate's init() (written by
// this same retrofit, see writeKgateService) overrides both hooks with
// the app's real event-processing and startup-subscription logic.
const kgateHandleEventPatched = `// EventHandler processes every event, however it arrived — a live
// Subscribe connection (via dispatchEvent, which recovers a panic here)
// or the /webhooks/kgate fallback below. It's a package variable, not a
// fixed function, so services/kgate (generated by ` + "`" + `nexler init kgate` + "`" + `)
// can inject your app's real event-processing logic via its own init()
// without this file ever needing to change again. Switch on channel for
// per-channel behavior; payload has already been unwrapped (see
// unwrapPayload's doc comment) by the time handleEvent calls this.
// Returning a non-nil error from a live subscription prevents the event
// from being acked (so kgate may redeliver it) unless the error is
// permanent (see permanentError), in which case it's acked anyway; from
// the webhook path a permanent error results in a 400 response, any
// other error in a 500. Defaults to a no-op that drops every event.
var EventHandler func(ctx context.Context, channel string, payload json.RawMessage) error = defaultEventHandler

func defaultEventHandler(ctx context.Context, channel string, payload json.RawMessage) error {
	return nil
}

func handleEvent(ctx context.Context, channel string, payload json.RawMessage) error {
	return EventHandler(ctx, channel, unwrapPayload(payload))
}

// OnStartup runs once, from Register's background goroutine (right after
// ResumeAll), to subscribe to whatever channels this app cares about at
// startup. services/kgate's init() overrides this with your app's real
// startup subscriptions — same injection mechanism as EventHandler above.
// Defaults to the "test" smoke-test subscribe kept from before this
// indirection existed: verifies the whole pipeline (core DB registry +
// WebSocket connectivity) works end to end on a fresh app. Subscribe is
// idempotent for an already-subscribed channel, so leaving this default in
// place is harmless even once services/kgate's own Init has run.
var OnStartup func(ctx context.Context) = defaultOnStartup

func defaultOnStartup(ctx context.Context) {
	if err := Subscribe(ctx, "test"); err != nil {
		log.Printf("kgate: subscribing to test channel: %v", err)
	}
}`

// kgateRegisterPatchedV3 is what ensureKgateServiceExtraction replaces
// kgateRegisterPatchedV2 with — same as the current kgate.go.tmpl. Calls
// OnStartup instead of inlining the "test" smoke-test Subscribe, so
// Register's own body no longer embeds a business decision (which
// channels to auto-subscribe) that a hand edit could put at odds with a
// future anchor-based patch.
const kgateRegisterPatchedV3 = `func Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /webhooks/kgate", HandleWebhook)
	// Protected is left false (the default): this route authenticates via
	// the HMAC X-Signature check in HandleWebhook above, not
	// middleware.RequireAuth, so Protected: true here would be misleading.
	openapi.Register(openapi.Operation{
		Method:         "POST",
		Path:           "/webhooks/kgate",
		OperationID:    "kgateWebhook",
		Summary:        "POST /webhooks/kgate",
		Tags:           []string{"kgate"},
		ReqType:        webhookEvent{},
		ReqContentType: "application/json",
	})
	go func() {
		if err := ResumeAll(context.Background()); err != nil {
			log.Printf("kgate: resuming channels: %v", err)
		}
		OnStartup(context.Background())
	}()
}`

// ensureKgateServiceExtraction brings an app scaffolded before kgate's
// business logic (handleEvent's real implementation, and Register's
// startup-subscription decision) lived in the separate, one-time-written
// services/kgate package up to date. Must run after every other kgate
// retrofit above, since its anchors assume the resilient-delivery/
// webhook-dispatch shape they produce.
//
// handleEvent must match kgateHandleEventOriginal *exactly* — this is the
// deliberate "only migrate if still untouched" safety rule: if a
// developer has already written real event-processing logic into
// handleEvent, this check errors out instead of guessing how to move it,
// naming the manual migration steps. It never silently relocates
// hand-written business logic. A missing kgate/kgate.go is a silent
// no-op, same precedent as every other kgate retrofit here.
func ensureKgateServiceExtraction(appDir string) (bool, error) {
	path := filepath.Join(appDir, "kgate", "kgate.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if strings.Contains(content, "EventHandler(ctx, channel") {
		return false, nil
	}

	switch {
	case strings.Contains(content, kgateHandleEventOriginal):
		content = strings.Replace(content, kgateHandleEventOriginal, kgateHandleEventPatched, 1)
	case strings.Contains(content, kgateHandleEventOriginalLegacy):
		content = strings.Replace(content, kgateHandleEventOriginalLegacy, kgateHandleEventPatched, 1)
	default:
		return false, fmt.Errorf("%s: handleEvent has been customized — move its body into services/kgate/kgate.go's HandleEvent by hand, then replace handleEvent (and add the OnStartup/defaultOnStartup declarations shown here) with:\n%s", path, kgateHandleEventPatched)
	}

	if !strings.Contains(content, kgateRegisterPatchedV2) {
		return false, fmt.Errorf("%s: Register doesn't match the known original (has it been hand-rewritten?) — add the following manually:\n%s", path, kgateRegisterPatchedV3)
	}
	content = strings.Replace(content, kgateRegisterPatchedV2, kgateRegisterPatchedV3, 1)

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, err
	}

	modulePath, err := readModulePath(appDir)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(filepath.Join(appDir, "services", "kgate", "kgate.go")); err != nil {
		if err := writeKgateService(appDir, modulePath); err != nil {
			return false, fmt.Errorf("patched %s, but could not generate services/kgate/kgate.go: %w", path, err)
		}
	}
	if err := wireBlankImport(appDir, "public", modulePath+"/services/kgate"); err != nil {
		return false, fmt.Errorf("patched %s and generated services/kgate/kgate.go, but could not wire it into routes/public/public.go automatically: %w\nAdd manually in that file:\n  import _ %q",
			path, err, modulePath+"/services/kgate")
	}

	return true, nil
}

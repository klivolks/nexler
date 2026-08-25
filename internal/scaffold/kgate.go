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

	return nil
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
	if strings.Contains(content, `Subscribe(context.Background(), "test")`) {
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

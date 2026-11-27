package engine

// The checkpoint is the working state of a run: what the loop needs to go on after the process is gone. It is
// written after each model response, after each tool result that must not be produced twice, and at the end of each
// round, together with the message, in one transaction: the stored message never runs ahead of it. It is overwritten
// whole, because the loop rewrites its message list in place (clearing, cutting, summarising), which an appended
// log could not follow.
//
// It holds conversation and tool-result text: never log it.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/cdmx-in/kera/internal/chats"
	"github.com/cdmx-in/kera/internal/provider"
)

// checkpointVersion is raised when an older checkpoint can no longer be read. A run whose checkpoint has another
// version ends as interrupted; it never starts over, which could repeat what its tools did.
const checkpointVersion = 1

// ponytail: the whole message list on every write (about 180 KB at a 65k window, arithmetic, not measured). If the
// write volume shows up, store the unchanged start once and then an appended tail.
type checkpoint struct {
	Version  int         `json:"v"`
	Messages []ckMessage `json:"messages"`
	Base     int         `json:"base"`           // where the run's own messages start
	Round    int         `json:"round"`          // the round of the newest model response
	Open     *openRound  `json:"open,omitempty"` // set from a model response until each of its calls has a result

	Cites          map[string]int    `json:"cites,omitempty"`
	FileIDs        []string          `json:"file_ids,omitempty"`
	Collections    []string          `json:"collections,omitempty"`
	Features       map[string]bool   `json:"features"`
	Params         map[string]any    `json:"params"`
	ProviderTools  []json.RawMessage `json:"provider_tools,omitempty"`
	DisableServers bool              `json:"disable_servers,omitempty"`
	DisabledNames  []string          `json:"disabled_names,omitempty"`
	Spilled        map[string]string `json:"spilled,omitempty"`
	Usage          Usage             `json:"usage"`
	ElapsedMs      int64             `json:"elapsed_ms"` // time worked so far, over every attempt
	Local          bool              `json:"local"`      // the conversation was built for a local model
}

// openRound is a round whose tool calls do not all have a result yet.
type openRound struct {
	At int `json:"at"` // index of the assistant message that asked for the calls
	// Safe says per call which provider's tool may be called a second time after a restart; "" = never. It is
	// recorded when the model asks, because the tools of a restarted server can differ.
	Safe []string `json:"safe"`
	Shot *ckImage `json:"shot,omitempty"` // the newest screenshot among the results so far
	// Idle: the run stopped between calls on purpose, so no call without a result has started and none is in doubt.
	// It is cleared, and that is saved, before any call runs again.
	Idle bool `json:"idle,omitempty"`
	// Parked is the call id of the question the run waits on, the first call without a result. Nothing from it on has
	// started. Every checkpoint that carries it keeps the question's wake time and answer in the row.
	Parked string `json:"parked,omitempty"`
}

type ckMessage struct {
	Role    string    `json:"role"`
	Content string    `json:"content,omitempty"`
	Images  []ckImage `json:"images,omitempty"`
	Calls   []ckCall  `json:"calls,omitempty"`
	CallID  string    `json:"call_id,omitempty"`
	Name    string    `json:"name,omitempty"`
	Data    bool      `json:"data,omitempty"` // provider.Message.Data, which the guardrails read
}

// ckImage is an image: the stored file it came from, or the data itself when there is none (an API caller's image).
type ckImage struct {
	MIME   string `json:"mime,omitempty"`
	FileID string `json:"file_id,omitempty"`
	Data   string `json:"data,omitempty"`
}

// ckCall keeps the arguments as text: a model can send arguments that are not valid JSON.
type ckCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"args"`
}

func imageRef(img provider.Image) ckImage {
	if img.FileID != "" {
		return ckImage{MIME: img.MIME, FileID: img.FileID}
	}
	return ckImage{MIME: img.MIME, Data: img.Data}
}

func ckMessages(in []provider.Message) []ckMessage {
	out := make([]ckMessage, len(in))
	for i, m := range in {
		c := ckMessage{Role: m.Role, Content: m.Content, CallID: m.ToolCallID, Name: m.Name, Data: m.Data}
		for _, img := range m.Images {
			c.Images = append(c.Images, imageRef(img))
		}
		for _, t := range m.ToolCalls {
			c.Calls = append(c.Calls, ckCall{ID: t.ID, Name: t.Name, Args: string(t.Arguments)})
		}
		out[i] = c
	}
	return out
}

// restoreImage reads an image back. A stored file is read again only while the user may still read it.
func (e *Engine) restoreImage(ctx context.Context, x *exec, in ckImage) (provider.Image, bool) {
	if in.FileID == "" {
		return provider.Image{MIME: in.MIME, Data: in.Data}, true
	}
	if ok, err := e.Files.CanRead(ctx, x.u, in.FileID); err != nil || !ok {
		return provider.Image{}, false
	}
	f, err := e.Files.Get(ctx, in.FileID)
	if err != nil {
		return provider.Image{}, false
	}
	return e.image(ctx, f)
}

func (e *Engine) restoreMessages(ctx context.Context, x *exec, in []ckMessage) []provider.Message {
	out := make([]provider.Message, len(in))
	for i, c := range in {
		m := provider.Message{Role: c.Role, Content: c.Content, ToolCallID: c.CallID, Name: c.Name, Data: c.Data}
		for _, ref := range c.Images {
			if img, ok := e.restoreImage(ctx, x, ref); ok {
				m.Images = append(m.Images, img)
			} else {
				slog.Warn("resume: an image can no longer be read", "run", x.run.ID, "file", ref.FileID)
			}
		}
		for _, t := range c.Calls {
			m.ToolCalls = append(m.ToolCalls, provider.ToolCall{ID: t.ID, Name: t.Name, Arguments: json.RawMessage(t.Args)})
		}
		out[i] = m
	}
	return out
}

// checkpointOf collects the run's working state.
func checkpointOf(x *exec, msgs []provider.Message, b *runBudget, round int, open *openRound) *checkpoint {
	x.run.mu.Lock()
	usage := x.run.usage
	x.run.mu.Unlock()
	b.mu.Lock()
	spilled := maps.Clone(b.spilled)
	b.mu.Unlock()
	return &checkpoint{Version: checkpointVersion, Messages: ckMessages(msgs), Base: b.base, Round: round, Open: open,
		Cites: x.cites.all(), FileIDs: x.readableFiles(), Collections: x.collections, Features: x.features, Params: x.params,
		ProviderTools: x.providerTools, DisableServers: x.disableServers, DisabledNames: slices.Sorted(maps.Keys(x.disabledNames)),
		Spilled: spilled, Usage: usage, ElapsedMs: time.Since(x.started).Milliseconds(), Local: x.m.Local}
}

// restore puts the checkpoint's state back into the run's working state (the messages go to the loop).
func (c *checkpoint) restore(x *exec) {
	x.cites.restore(c.Cites)
	x.fileIDs, x.collections = c.FileIDs, c.Collections
	x.features, x.params = c.Features, c.Params
	if x.features == nil {
		x.features = map[string]bool{}
	}
	if x.params == nil {
		x.params = map[string]any{}
	}
	x.providerTools, x.disableServers = c.ProviderTools, c.DisableServers
	for _, n := range c.DisabledNames {
		x.disabledNames[n] = true
	}
}

func decodeCheckpoint(raw []byte) (*checkpoint, error) {
	var c checkpoint
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("checkpoint: %w", err)
	}
	if c.Version != checkpointVersion {
		return nil, fmt.Errorf("checkpoint: version %d, this server reads %d", c.Version, checkpointVersion)
	}
	if o := c.Open; o != nil && (o.At < 0 || o.At >= len(c.Messages) || len(o.Safe) != len(c.Messages[o.At].Calls) ||
		len(c.Messages)-o.At-1 > len(o.Safe)) {
		return nil, fmt.Errorf("checkpoint: the open round does not match its messages")
	}
	return &c, nil
}

// saveCheckpoint writes the working state and the message in one transaction, if this instance still owns the run.
// It does not use the run's context: what a tool did must be recorded even when the run is being stopped. Runs that
// cannot be resumed (temporary, client tools, runs.resume off) have none.
func (e *Engine) saveCheckpoint(x *exec, c *checkpoint) error {
	if !x.durable {
		return nil
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("save the run's state: %w", err)
	}
	var waitKey *string // the question the run is parked on, if any
	if c.Open != nil && c.Open.Parked != "" {
		waitKey = &c.Open.Parked
	}
	x.run.saveMu.Lock()
	defer x.run.saveMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tx, err := e.DB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("save the run's state: %w", err)
	}
	defer tx.Rollback(ctx)
	// the same question keeps its wake time and the answer that may already be there; any other state has neither
	tag, err := tx.Exec(ctx, `UPDATE run SET checkpoint = $4, wait_key = $5,
			wake_at = CASE WHEN $5::text IS NULL THEN NULL WHEN wait_key = $5 THEN wake_at ELSE now() + make_interval(secs => $6) END,
			answer = CASE WHEN wait_key = $5 THEN answer END
		WHERE id = $1 AND lease_owner = $2 AND attempt = $3`,
		x.run.ID, e.instance, x.run.attempt, raw, waitKey, x.askWait().Seconds())
	if err != nil {
		return fmt.Errorf("save the run's state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errLostLease
	}
	if err := chats.SaveProgressIn(ctx, tx, x.run.MessageID, x.run.progress()); err != nil {
		return fmt.Errorf("save the run's state: %w", err)
	}
	return tx.Commit(ctx)
}

// ---- calls that may run again ---------------------------------------------------------

// rerunnableBuiltins are the built-in tools a resumed run may call again, by name: each was checked against its code
// and only reads. A tool added to a set later is not here until someone has checked it too. Left out on purpose:
// builtin:code (runs any code, with whatever the sandbox can reach, and stores files), builtin:memory and
// builtin:schedule (they write), and ask_user (its answer must be recorded the moment it is read).
var rerunnableBuiltins = map[string][]string{
	"builtin:core":      {"get_current_timestamp", "search_chats"},
	"builtin:files":     {"list_chat_files", "view_file", "grep_chat_files"},
	"builtin:knowledge": {"list_knowledge", "search_knowledge_files", "query_knowledge_files", "view_knowledge_file"},
	"builtin:web":       {"search_web", "fetch_url"},
	"builtin:skills":    {"list_skills", "load_skill", "read_skill_file"},
}

// rerunnable reports whether calling the tool a second time changes nothing. A tool server's tool counts only when
// the server declares it read-only (an MCP readOnlyHint, an OpenAPI GET); a built-in only when it is listed above.
func rerunnable(t toolEntry) bool {
	if id := t.provider.ID(); strings.HasPrefix(id, "builtin:") {
		return slices.Contains(rerunnableBuiltins[id], t.spec.Name)
	}
	return t.spec.ReadOnly
}

// safeProvider is the provider whose tool would run c again safely, or "".
func safeProvider(table map[string]toolEntry, c provider.ToolCall) string {
	if t, ok := table[c.Name]; ok && rerunnable(t) {
		return t.provider.ID()
	}
	return ""
}

func safeProviders(table map[string]toolEntry, calls []provider.ToolCall) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = safeProvider(table, c)
	}
	return out
}

// The results a resumed run gives the model for calls it does not run again.
const (
	unknownResult = "Not run again: the server restarted while this call was running; it is unknown whether it took effect; " +
		"check before repeating it."
	changedResult = "Not run again: the server restarted, and this tool is no longer the same read-only tool it was. " +
		"Check before calling it again."
)

// resumePlan decides what happens to the calls of an open round that have no result (from the index `from` on) and
// returns the result to give instead of running, per call ("" = run it). Calls run one after another, and the
// checkpoint is written after each call that is not safe to repeat before the next one starts. So the first such
// call without a result is the only one that can have run unrecorded: it is not run again. The calls after it never
// started; they run normally, judged by the tools there are now (o.Safe is brought up to date for them).
func resumePlan(table map[string]toolEntry, calls []provider.ToolCall, from int, o *openRound) []string {
	skip := make([]string, len(calls))
	started := !o.Idle && o.Parked == "" // calls from here on may have started before the restart
	for i := from; i < len(calls); i++ {
		now := safeProvider(table, calls[i])
		switch {
		case !started:
			o.Safe[i] = now
		case o.Safe[i] == "":
			skip[i], started = unknownResult, false
		case o.Safe[i] != now:
			skip[i] = changedResult
		}
	}
	return skip
}

// all is a copy of the numbering; restore puts one back.
func (c *cites) all() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.nums)
}

func (c *cites) restore(nums map[string]int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nums = maps.Clone(nums)
}

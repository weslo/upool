package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
)

// hookEvents are the Claude Code events init registers upool for, each with the `upool hook`
// subcommand that handles it, in the order init adds them to a settings file that has none of them
// (docs/design.md, "Build in four phases, with the clone recipe first", phase 2; A26). PreToolUse,
// the lane guard, matches only the tools that write a file, and blocks the call when the hook
// fails, so that a failure denies rather than silently allowing (docs/design.md, "Lanes: scripts
// in parallel, scenes one at a time, `.meta` never regenerated").
var hookEvents = []struct{ hook, event, matcher, onFailure string }{
	{"worktree-create", "WorktreeCreate", "", ""},
	{"worktree-remove", "WorktreeRemove", "", ""},
	{"session-start", "SessionStart", "", ""},
	{"pre-tool-use", "PreToolUse", "Edit|Write|NotebookEdit", "block"},
}

// settingsMode is the mode of a settings.local.json that init creates, private to the user as
// Claude Code keeps its own; an existing file keeps its mode. agentDirMode is the mode of a
// directory init creates in the project for an agent's files, such as .claude or a skill's.
const (
	settingsMode fs.FileMode = 0o600
	agentDirMode fs.FileMode = 0o755
)

// settingsPath returns ~/.claude/settings.json, Claude Code's user settings, which upool never
// writes: registered there its hooks would run for every repository on the machine (A26), so
// doctor warns when they are.
func settingsPath(home string) string {
	return filepath.Join(home, ".claude", "settings.json")
}

// commandHook is a hook handler, as init registers it and the hook checks read it. init writes it
// in exec form, upool's path as command and `hook <event>` as args: with args present, Claude Code
// spawns command itself, with no shell to split or expand the path (docs/design.md, the `init`
// row; spike §8 A.2–3). The desktop app's helper can run upool from anywhere, ~/Documents
// included, though it cannot read a file there (docs/contracts.md (a); #163).
type commandHook struct {
	Type    string   `json:"type"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	// OnFailure is "block" for the lane guard, so that a hook that fails denies the tool call;
	// the others leave it unset ([Claude Code hooks]).
	//
	// [Claude Code hooks]: https://code.claude.com/docs/en/hooks
	OnFailure string `json:"onFailure,omitempty"`
}

// hookGroup is the matcher group init adds for each event. The worktree and session events have no
// matcher, so their group runs on every occurrence of the event: WorktreeCreate and WorktreeRemove
// take none, and SessionStart handles every source (docs/contracts.md (b); A14). PreToolUse's
// matches the tools that write a file.
type hookGroup struct {
	Matcher string        `json:"matcher,omitempty"`
	Hooks   []commandHook `json:"hooks"`
}

// registration is init's change to a project's .claude/settings.local.json, which registers upool's
// hooks (A26), worked out before anything is written so that a file that cannot be read or parsed
// stops init with nothing changed.
type registration struct {
	// file is the settings file, as the command reports it, and target the file written: file, or
	// what it links to, so that a link into a dotfiles repository stays a link.
	file, target string
	// exists says whether target exists, and old and mode are its contents and mode bits.
	exists bool
	old    []byte
	mode   fs.FileMode
	// content is the new settings.json, and changed whether it differs from old as JSON.
	content []byte
	changed bool
}

// planRegistration reads the settings file and returns it with upool's hooks registered, each
// running upool, an absolute path.
func planRegistration(file, upool string) (registration, error) {
	reg := registration{file: file, mode: settingsMode}
	reg.target = reg.file
	if _, err := os.Lstat(reg.file); err == nil {
		if reg.target, err = filepath.EvalSymlinks(reg.file); err != nil {
			return registration{}, fmt.Errorf("resolving %s: %w", reg.file, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return registration{}, fmt.Errorf("checking %s: %w", reg.file, err)
	}
	settings := &jsonObject{}
	info, err := os.Stat(reg.target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return registration{}, fmt.Errorf("checking %s: %w", reg.target, err)
	case !info.Mode().IsRegular():
		return registration{}, fmt.Errorf("%s is not a regular file", reg.target)
	default:
		reg.exists, reg.mode = true, info.Mode().Perm()
		if reg.old, err = os.ReadFile(reg.target); err != nil {
			return registration{}, fmt.Errorf("reading %s: %w", reg.target, err)
		}
		if len(bytes.TrimSpace(reg.old)) > 0 {
			if err := json.Unmarshal(reg.old, settings); err != nil {
				return registration{}, fmt.Errorf("reading %s: %w", reg.file, err)
			}
		}
	}
	if err := registerHooks(settings, upool); err != nil {
		return registration{}, fmt.Errorf("registering the hooks in %s: %w", reg.file, err)
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(settings); err != nil {
		return registration{}, fmt.Errorf("encoding %s: %w", reg.file, err)
	}
	reg.content = b.Bytes()
	reg.changed = !reg.exists || !sameJSON(reg.old, reg.content)
	return reg, nil
}

// apply writes the registration, unless the file already held it.
func (reg registration) apply() (installed, error) {
	if !reg.changed {
		return installed{Path: reg.file, Change: unchanged}, nil
	}
	if !reg.exists {
		if err := os.MkdirAll(filepath.Dir(reg.target), agentDirMode); err != nil {
			return installed{}, fmt.Errorf("creating %s: %w", filepath.Dir(reg.target), err)
		}
	}
	if err := writeAtomic(reg.target, reg.content, reg.mode); err != nil {
		return installed{}, err
	}
	if reg.exists {
		return installed{Path: reg.file, Change: updated}, nil
	}
	return installed{Path: reg.file, Change: created}, nil
}

// registerHooks registers, in settings, upool for each event in hookEvents. A handler that runs
// `upool hook` is upool's, from this run or an earlier one, wherever it sits and whichever upool it
// names: each is removed, a matcher group left with no handlers goes with it, and the event's own
// group takes the place of the first group it was removed from, or goes last. A second run
// therefore leaves the file as it was. Every other key, group and handler is kept as written.
func registerHooks(settings *jsonObject, upool string) error {
	hooks := &jsonObject{}
	if raw, ok := settings.get("hooks"); ok {
		if err := json.Unmarshal(raw, hooks); err != nil {
			return fmt.Errorf("hooks: %w", err)
		}
	}
	managed := map[string]bool{}
	for _, h := range hookEvents {
		managed[h.event] = true
	}
	at := map[string]int{}
	for _, event := range slices.Clone(hooks.keys) {
		raw, _ := hooks.get(event)
		var groups []json.RawMessage
		if err := json.Unmarshal(raw, &groups); err != nil {
			if managed[event] {
				return fmt.Errorf("hooks.%s is not an array: %w", event, err)
			}
			continue
		}
		kept, first, err := removeUpoolHooks(groups)
		if err != nil {
			return fmt.Errorf("hooks.%s: %w", event, err)
		}
		if first < 0 {
			continue
		}
		at[event] = first
		if len(kept) == 0 && !managed[event] {
			hooks.remove(event)
			continue
		}
		if err := hooks.setValue(event, kept); err != nil {
			return err
		}
	}
	for _, h := range hookEvents {
		group, err := marshalJSON(hookGroup{Matcher: h.matcher, Hooks: []commandHook{{
			Type: "command", Command: upool, Args: []string{"hook", h.hook},
			OnFailure: h.onFailure,
		}}})
		if err != nil {
			return err
		}
		var groups []json.RawMessage
		if raw, ok := hooks.get(h.event); ok {
			if err := json.Unmarshal(raw, &groups); err != nil {
				return fmt.Errorf("hooks.%s is not an array: %w", h.event, err)
			}
		}
		i, ok := at[h.event]
		if !ok {
			i = len(groups)
		}
		groups = slices.Insert(groups, i, json.RawMessage(group))
		if err := hooks.setValue(h.event, groups); err != nil {
			return err
		}
	}
	return settings.setValue("hooks", hooks)
}

// removeUpoolHooks returns groups without upool's handlers, and the index in the result where the
// first group that held one was, or -1 if none did. A group or handler of a shape it does not
// recognise is not upool's, and is kept as it is.
func removeUpoolHooks(groups []json.RawMessage) (kept []json.RawMessage, first int, err error) {
	first = -1
	kept = []json.RawMessage{}
	for _, raw := range groups {
		group := &jsonObject{}
		var handlers []json.RawMessage
		if json.Unmarshal(raw, group) != nil {
			kept = append(kept, raw)
			continue
		}
		if h, ok := group.get("hooks"); !ok || json.Unmarshal(h, &handlers) != nil {
			kept = append(kept, raw)
			continue
		}
		others := []json.RawMessage{}
		for _, h := range handlers {
			var handler commandHook
			if json.Unmarshal(h, &handler) == nil && runsUpoolHook(handler) {
				continue
			}
			others = append(others, h)
		}
		if len(others) == len(handlers) {
			kept = append(kept, raw)
			continue
		}
		if first < 0 {
			first = len(kept)
		}
		if len(others) == 0 {
			continue
		}
		if err := group.setValue("hooks", others); err != nil {
			return nil, 0, err
		}
		b, err := marshalJSON(group)
		if err != nil {
			return nil, 0, err
		}
		kept = append(kept, b)
	}
	return kept, first, nil
}

// runsUpoolHook says whether h runs `upool hook`, by any path to upool: in exec form, a command
// named upool whose first argument is hook; in shell form, with no args, a command line whose
// first two words are those.
func runsUpoolHook(h commandHook) bool {
	command, args := h.Command, h.Args
	if args == nil {
		words := strings.Fields(h.Command)
		if len(words) == 0 {
			return false
		}
		command, args = words[0], words[1:]
	}
	return filepath.Base(command) == upoolName && len(args) > 0 && args[0] == "hook"
}

// sameJSON says whether a and b, both valid JSON, hold the same value, whatever their layout.
func sameJSON(a, b []byte) bool {
	var va, vb any
	return decodeNumbers(a, &va) == nil && decodeNumbers(b, &vb) == nil &&
		reflect.DeepEqual(va, vb)
}

// decodeNumbers decodes b into v, keeping numbers as written.
func decodeNumbers(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	return dec.Decode(v)
}

// marshalJSON encodes v as compact JSON without escaping <, > and &, which settings.json has no
// reason to.
func marshalJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encoding JSON: %w", err)
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// jsonObject is a JSON object that keeps its keys in the order written and its values as written,
// so that a rewrite of a settings file changes only what init registers. JSON null decodes as an
// empty object.
type jsonObject struct {
	keys   []string
	values map[string]json.RawMessage
}

// get returns key's value, and whether the object has key.
func (o *jsonObject) get(key string) (json.RawMessage, bool) {
	v, ok := o.values[key]
	return v, ok
}

// set sets key to value, appending key if the object lacks it.
func (o *jsonObject) set(key string, value json.RawMessage) {
	if o.values == nil {
		o.values = map[string]json.RawMessage{}
	}
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

// setValue sets key to v encoded.
func (o *jsonObject) setValue(key string, v any) error {
	b, err := marshalJSON(v)
	if err != nil {
		return err
	}
	o.set(key, b)
	return nil
}

// remove removes key.
func (o *jsonObject) remove(key string) {
	delete(o.values, key)
	o.keys = slices.DeleteFunc(o.keys, func(k string) bool { return k == key })
}

// UnmarshalJSON decodes b, a JSON object or null. A key written twice keeps its first place and
// its last value, the value encoding/json would keep.
func (o *jsonObject) UnmarshalJSON(b []byte) error {
	*o = jsonObject{}
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil {
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return errors.New("not a JSON object")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := tok.(string)
		if !ok {
			return fmt.Errorf("object key %v is not a string", tok)
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return err
		}
		o.set(key, value)
	}
	_, err = dec.Token()
	return err
}

// MarshalJSON encodes the object with its keys in order.
func (o jsonObject) MarshalJSON() ([]byte, error) {
	b := []byte{'{'}
	for i, key := range o.keys {
		if i > 0 {
			b = append(b, ',')
		}
		k, err := marshalJSON(key)
		if err != nil {
			return nil, err
		}
		b = append(append(append(b, k...), ':'), o.values[key]...)
	}
	return append(b, '}'), nil
}

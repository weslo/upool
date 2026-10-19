package pool

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// A slot in each state, as Next sees it. The two leased slots differ in their lease: a bare take's
// has no holder PID and is never reclaimed (A6).
var (
	newSlot     = Slot{}
	creating    = Slot{Name: "slot-2", State: StateCreating, OwnerPID: new(48377)}
	warming     = Slot{Name: "slot-1", State: StateWarming, OwnerPID: new(48390)}
	readySlot   = Slot{Name: "slot-1", State: StateReady}
	unconfirmed = Slot{Name: "slot-1", State: StateLeasedUnconfirmed,
		Lease: &Lease{Holder: "0f6b1c2d", Confirmed: false}}
	leased = Slot{Name: "slot-1", State: StateLeased,
		Lease: &Lease{Holder: "0f6b1c2d", HolderPID: new(48213), Confirmed: true}}
	bareLeased = Slot{Name: "slot-1", State: StateLeased,
		Lease: &Lease{Holder: "terminal", Confirmed: true}}
	parked     = Slot{Name: "slot-1", State: StateParked}
	needsReset = Slot{Name: "slot-1", State: StateNeedsReset}
)

// slots are every slot TestStatesRejectUnlistedEvents puts every event to, by subtest name: the
// zero Slot, each state of docs/contracts.md (f), and a state (f) does not have.
var slots = []struct {
	name string
	slot Slot
}{
	{"new", newSlot},
	{"creating", creating},
	{"warming", warming},
	{"ready", readySlot},
	{"leased-unconfirmed", unconfirmed},
	{"leased", leased},
	{"leased_without_holder_pid", bareLeased},
	{"parked", parked},
	{"needs-reset", needsReset},
	{"unknown", Slot{Name: "slot-1", State: "bogus"}},
}

// events returns every event, once for each combination of its facts.
func events() []Event {
	bools := []bool{false, true}
	es := []Event{CloneStart{}, CloneFinish{}, Refresh{}, Seed{}, Confirm{}, ConfirmTimeout{},
		Prune{}, Missing{}}
	for _, b := range bools {
		es = append(es, WarmingFinish{Clean: b}, HookTake{ReadyAvailable: b},
			Take{ReadyAvailable: b}, Remove{Clean: b}, Release{Clean: b})
		for _, c := range bools {
			es = append(es, DeadOwner{EditorLive: b, Clean: c})
			for _, d := range bools {
				es = append(es, Reclaim{HolderAlive: b, EditorLive: c, Clean: d})
			}
		}
	}
	return es
}

// step is one slot and event, and the outcome Next must give: a state, or with want empty, an
// error.
type step struct {
	from  Slot
	event Event
	want  State
}

// refused is a step for every event but except from s, each of which Next must refuse.
func refused(s Slot, except Event) []step {
	var steps []step
	for _, e := range events() {
		if e != except {
			steps = append(steps, step{s, e, ""})
		}
	}
	return steps
}

// transitions is the transition table of docs/contracts.md (f), one entry per row in its order,
// each named after its number and cause. The steps are the slots and events that cause it; where a
// fact does not matter to a transition, it has a step for each value. 16 is no transition, so its
// steps are the events a parked slot refuses: all of them but 17's.
var transitions = []struct {
	name  string
	steps []step
}{
	{"1_clone_starts", []step{{newSlot, CloneStart{}, StateCreating}}},
	{"2_clone_finishes", []step{{creating, CloneFinish{}, StateReady}}},
	{"3_refresh_ready", []step{{readySlot, Refresh{}, StateWarming}}},
	{"4_hook_take_ready", []step{
		{readySlot, HookTake{ReadyAvailable: true}, StateLeasedUnconfirmed},
		{readySlot, HookTake{ReadyAvailable: false}, StateLeasedUnconfirmed},
	}},
	{"5_take_ready", []step{
		{readySlot, Take{ReadyAvailable: true}, StateLeased},
		{readySlot, Take{ReadyAvailable: false}, StateLeased},
	}},
	{"6_confirm", []step{{unconfirmed, Confirm{}, StateLeased}}},
	{"7_confirm_timeout", []step{{unconfirmed, ConfirmTimeout{}, StateReady}}},
	{"8_remove_clean", []step{
		{leased, Remove{Clean: true}, StateNeedsReset},
		{bareLeased, Remove{Clean: true}, StateNeedsReset},
	}},
	{"9_remove_dirty", []step{
		{leased, Remove{Clean: false}, StateParked},
		{bareLeased, Remove{Clean: false}, StateParked},
	}},
	{"10_hook_take_needs_reset_none_ready", []step{
		{needsReset, HookTake{ReadyAvailable: false}, StateLeasedUnconfirmed},
	}},
	{"11_release_clean", []step{
		{leased, Release{Clean: true}, StateReady},
		{bareLeased, Release{Clean: true}, StateReady},
	}},
	{"12_release_dirty", []step{
		{leased, Release{Clean: false}, StateParked},
		{bareLeased, Release{Clean: false}, StateParked},
	}},
	{"13_reclaim_dead_holder_clean", []step{
		{leased, Reclaim{HolderAlive: false, EditorLive: false, Clean: true}, StateReady},
	}},
	{"14_reclaim_dead_holder_dirty", []step{
		{leased, Reclaim{HolderAlive: false, EditorLive: false, Clean: false}, StateParked},
		{leased, Reclaim{HolderAlive: false, EditorLive: true, Clean: false}, StateParked},
	}},
	{"15_prune_ready", []step{{readySlot, Prune{}, StateRemoved}}},
	{"16_parked_no_automatic_transition", refused(parked, Missing{})},
	{"17_missing", []step{
		{creating, Missing{}, StateMissing},
		{warming, Missing{}, StateMissing},
		{readySlot, Missing{}, StateMissing},
		{unconfirmed, Missing{}, StateMissing},
		{leased, Missing{}, StateMissing},
		{bareLeased, Missing{}, StateMissing},
		{parked, Missing{}, StateMissing},
		{needsReset, Missing{}, StateMissing},
	}},
	{"18_warming_finishes_clean", []step{{warming, WarmingFinish{Clean: true}, StateReady}}},
	{"19_take_needs_reset_none_ready", []step{
		{needsReset, Take{ReadyAvailable: false}, StateLeased},
	}},
	{"20_refresh_needs_reset", []step{{needsReset, Refresh{}, StateReady}}},
	{"21_dead_owner_creating", []step{
		{creating, DeadOwner{EditorLive: false, Clean: true}, StateRemoved},
		{creating, DeadOwner{EditorLive: false, Clean: false}, StateRemoved},
		{creating, DeadOwner{EditorLive: true, Clean: true}, StateRemoved},
		{creating, DeadOwner{EditorLive: true, Clean: false}, StateRemoved},
	}},
	{"22_dead_owner_warming_no_editor", []step{
		{warming, DeadOwner{EditorLive: false, Clean: true}, StateReady},
		{warming, DeadOwner{EditorLive: false, Clean: false}, StateParked},
	}},
	{"23_seed", []step{{readySlot, Seed{}, StateWarming}}},
}

// checkRefused fails t unless err is Next's refusal of e from s: an error naming s's state and e,
// with no state returned.
func checkRefused(t *testing.T, s Slot, e Event, got State, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("Next(%s, %s) = %s, want an error", s.Name, e, got)
		return
	}
	if got != "" {
		t.Errorf("Next(%s, %s) state = %q with error %v, want empty", s.Name, e, got, err)
	}
	if msg := err.Error(); !strings.Contains(msg, fmt.Sprintf("%q", s.State)) ||
		!strings.Contains(msg, e.String()) {
		t.Errorf("Next(%s, %s) error = %q, want it to name the state %q and the event", s.Name,
			e, msg, s.State)
	}
}

func TestTransition(t *testing.T) {
	for i, tr := range transitions {
		if n, _, _ := strings.Cut(tr.name, "_"); n != strconv.Itoa(i+1) {
			t.Fatalf("transitions[%d] is %s, want transition %d of docs/contracts.md (f)", i,
				tr.name, i+1)
		}
		t.Run(tr.name, func(t *testing.T) {
			for _, st := range tr.steps {
				got, err := Next(st.from, st.event)
				if st.want == "" {
					checkRefused(t, st.from, st.event, got, err)
					continue
				}
				if err != nil || got != st.want {
					t.Errorf("Next(%s %s, %s) = %q, %v, want %q, nil", st.from.Name, st.from.State,
						st.event, got, err, st.want)
				}
			}
		})
	}
	if len(transitions) != 23 {
		t.Errorf("transitions has %d rows, want the 23 of docs/contracts.md (f)", len(transitions))
	}
}

func TestStatesRejectUnlistedEvents(t *testing.T) {
	listed := func(s Slot, e Event) bool {
		for _, tr := range transitions {
			for _, st := range tr.steps {
				if st.want != "" && st.event == e && reflect.DeepEqual(st.from, s) {
					return true
				}
			}
		}
		return false
	}
	for _, sl := range slots {
		t.Run(sl.name, func(t *testing.T) {
			for _, e := range events() {
				if listed(sl.slot, e) {
					continue
				}
				got, err := Next(sl.slot, e)
				checkRefused(t, sl.slot, e, got, err)
			}
		})
	}
}

func TestRefusalNamesTheSlot(t *testing.T) {
	_, err := Next(parked, Take{ReadyAvailable: true})
	want := `slot-1: no transition from "parked" on take (a ready slot available)`
	if err == nil || err.Error() != want {
		t.Errorf("Next(parked slot-1, take) error = %v, want %q", err, want)
	}
}

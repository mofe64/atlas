# Atlas Design Language

This document defines the structure, behavior, and visual language of the Atlas
Native operator interface. The [developer documentation](README.md) explains
the system behavior. This document explains how the interface must communicate
that behavior to an operator.

The implemented interface is authoritative for current visual detail. This
document is authoritative for design intent, reusable contracts, interface
language, and open product decisions.

---

## 1. What we are designing

Atlas is a **local command-and-control console for a fleet of drones**. It is
not a general dashboard, documentation site, or marketing surface. An operator
can use it for long periods during flight tests and incident response. The
interface can be the operator's primary source of aircraft state.

This operational role controls each rule in this document.

### The operator

| | |
|---|---|
| **Who** | Drone operators and developers validating PX4-based workflows, through SITL today and companion-computer agents in the field. |
| **Where** | Both a desk with a large monitor and a laptop outdoors in daylight. Both are first-class. |
| **Tempo** | Three distinct modes — real-time supervision, deliberate preparation, and post-flight review. They are not the same job. |
| **Stakes** | Aircraft-moving authority. A misread status or a missed state change has physical consequences. |

### Brand personality

Operational, calm, precise. The interface should feel like a **dependable instrument
panel**: low drama, high legibility, dense enough to reward repeated use, and honest
about what it does and does not know.

---

## 2. The five principles

Each design decision must support one or more of these principles.

### 2.1 Structure follows the operator's job, not the codebase

Navigation must represent the operator's task. It must not represent the
software-component structure. A component name is not sufficient reason for a
top-level navigation item.

Organize the interface by **intent and tempo**:

| Surface | Tempo | Answers |
|---|---|---|
| **Command** | Real-time | What is my fleet doing right now? |
| **Dispatch** | Deliberate | Which aircraft responds to this incident, and how? |
| **Aircraft** | Drill-down | What is this specific vehicle doing, seeing, and holding? |
| **Plan** | Authoring | What mission do I want to fly later? |
| **Evidence** | Review | What did we capture, and what does it mean? |

Divide **Aircraft** into sections because an operator performs several tasks on
one aircraft:

| Section | Contains |
|---|---|
| **Overview** | Flight data, preflight checks, power, PX4 messages, connection |
| **Camera** | Live feed, detected objects, manual gimbal + zoom, and camera follow |
| **Aircraft follow** | Reviewed target, standoff limits, readiness gates, and supervised aircraft movement |
| **Missions** | Why it can or cannot take a mission now, plus past flights by outcome |
| **History** | Retained telemetry trends and aircraft events |
| **Settings** | Identity, archive and restore |

**Flying is always Command.** Dispatch prepares and reserves; Plan authors and saves;
neither sends anything to an aircraft. This keeps one answer to "where do I go when
something is in the air", and removes the current app's unreachable execution page.

### 2.2 The fleet is the subject, not one aircraft

Atlas reasons about a fleet. The interface must too. Any surface that can only show
one aircraft at a time is a single-vehicle tool wearing a fleet's name.

Concretely: every aircraft's **control authority, current activity, and key
instruments** must be visible simultaneously, without selection. Selection adds
depth; it must never be the only way to learn that something is wrong.

### 2.3 Authority is always visible

An aircraft can be idle, executing a mission, executing an incident response, under
camera follow (gimbal authority), or under Follow from standoff (PX4 Offboard
authority). These are mutually exclusive in ways that matter for safety.

Rules:

- Every aircraft displays **which authority currently commands it**, everywhere it appears.
- Control modes are labelled by **what they move** — "moves gimbal" versus "moves aircraft" — not by their internal names.
- Related authorities are **co-located and compared**, never separated across screens. Separation is the weakest possible way to distinguish two things.
- Any active aircraft-moving authority is surfaced **globally**, on every screen, with a stop control.

### 2.4 State changes must be noticeable

A monitoring interface that updates silently trains operators to distrust it. Motion
budget goes to **state transitions**, never to decoration:

- A status tone downgrade gets one brief, non-looping emphasis.
- A new critical alert gets a persistent ribbon at the top of the frame, above the work.
- Routine 1 Hz refreshes get nothing at all.

Degraded states that require an operator decision — `DEGRADED_HOLD` above all — must
be impossible to miss from any screen.

### 2.5 Say what is true, derive it from the same source as the color

Never hardcode a status string that state can contradict. If a panel is coloured by
`gates.every(ok)`, its heading must be derived from `gates.every(ok)` too. A heading
reading "Ready to request" above eight blocked gates is worse than no heading.

Corollaries:

- Compose messages as whole strings with whole-string fallbacks. Never concatenate fragments that can produce "Updated not yet".
- State the **reason** alongside every disabled control, at the same visual rank as the control.
- Prefer operator consequence over system vocabulary. "Saved and AV-04 is reserved — nothing has been sent to the aircraft" beats "prepared atomically".

---

## 3. Visual language

The direction is **instrument console**: an evolution of the existing Atlas palette
and type pairing, re-tuned from editorial reading to sustained monitoring.

### 3.1 The core move — display type becomes instrument type

Atlas already pairs Avenir Next with Avenir Next Condensed. Previously the condensed
face was used for **headlines**, at up to 6rem. That is a magazine gesture on a
console.

The condensed face is now reserved for **measurements** — altitudes, speeds,
batteries, counts, durations, IDs. It keeps the brand's voice while completely
changing the genre. Headings shrink to compact uppercase labels; the largest type on
any working screen is a number that means something.

```css
--font-ui:         "Avenir Next", Avenir, "Segoe UI", system-ui, sans-serif;
--font-instrument: "Avenir Next Condensed", "DIN Condensed", "Oswald", sans-serif;
```

All numeric values use `font-variant-numeric: tabular-nums` so columns align and
digits do not jitter as they update.

### 3.2 Type scale

Compressed. No hero sizes on working screens.

| Token | Desk | Field | Use |
|---|---|---|---|
| `--t-micro` | 0.6875rem | 0.75rem | Uppercase labels, metadata |
| `--t-xs` | 0.75rem | 0.8125rem | Secondary text, gate detail |
| `--t-sm` | 0.8125rem | 0.875rem | Body |
| `--t-md` | 0.9375rem | 1rem | Emphasis, aircraft names |
| `--t-lg` | 1.125rem | — | Panel titles |
| `--t-readout` | 1.75rem | — | Instrument values |
| `--t-readout-lg` | 2.5rem | — | Primary instrument values |

Field mode raises the floor rather than scaling everything — outdoor legibility is a
minimum-size problem, not a proportion problem.

### 3.3 Color

OKLCH throughout. Neutrals are tinted toward the brand hue (145–150) — never pure
grey, never pure black or white.

**Color is status language. It is never decoration.**

| Token | Role |
|---|---|
| `--nominal` | Healthy, ready, succeeded |
| `--caution` | Degraded, consideration, needs attention |
| `--critical` | Blocked, failed, requires a decision now |
| `--authority` | An active control authority is commanding something |
| `--idle` | Present but not doing anything |

`--authority` (blue) is the one addition to the existing palette, and it earns its
place: it distinguishes "this is healthy" from "this is *being actively commanded*",
which the old green-only vocabulary could not express.

Surfaces come in three elevations (`base`, `raised`, `sunken`) plus `inverse` for the
header. Depth comes from **surface value and hairline rules**, not drop shadows.

### 3.4 Two modes: desk and field

Not light/dark. Both modes are light — the operating context is daylight and indoor
office, and a glowing dark console is a fashion choice, not a field requirement.

| | Desk | Field |
|---|---|---|
| Surfaces | Soft mist tones | Pushed toward white |
| Text | `oklch(22%)` primary | `oklch(15%)` primary |
| Rules | 1px, subtle | 2px, stronger |
| Status colors | Standard chroma | Higher chroma, lower lightness |
| Type floor | 0.6875rem | 0.75rem |

Implemented as a semantic-token override on `:root[data-mode="field"]`. Primitives do
not change; only the semantic layer is redefined.

### 3.5 Layout

- **Fixed app frame.** Live screens fill the viewport and never scroll as a page. Panels scroll internally with `overscroll-behavior: contain`.
- **Maps never steal a page scroll.** A map inside a scrolling document will hijack the wheel; the frame model removes the conflict entirely.
- **Hairline rules over cards.** Grouping comes from alignment, spacing, and 1px rules. No card grids, never a card inside a card.
- **4pt spacing scale**, varied deliberately — tight within a group, generous between groups.
- **Container queries** for components that appear in both a narrow rail and a wide panel.

### 3.6 Density

This is a console. Density is a feature, not a problem to solve with whitespace.
Target roughly 5 aircraft, 10 incidents, and a full instrument cluster visible
without scrolling at 1440×900. Reject any layout that shows fewer than 3 aircraft on
a laptop screen.

---

## 4. Component contracts

### Aircraft strip

The aircraft strip is the core fleet component. It shows identity, the
**authority badge**, current activity, and four key instruments. It also shows
a progress meter when the activity has a measurable extent. Do not reduce it to
an aircraft name and a status indicator.

### Authority badge

`IDLE` · `MISSION` · `RESPONSE` · `CAM FOLLOW` · `AIRCRAFT FOLLOW` · `PX4 HOLD` · `LINK STALE`

Filled treatment for authorities that are actively commanding; outlined for passive
states. Always accompanied by a non-color cue (text) so it survives color-blindness
and greyscale.

### Safety cluster

Hold / RTL / Land. **One component, one appearance, one position rule, on every
screen where an aircraft is selected.** Always accompanied by a plain-language
statement of availability or the reason it is unavailable. Never rendered
conditionally on something unrelated, like whether an incident is selected.

### Gate list

Every precondition for a control mode, each with a pass/fail marker, a name, and its
current value or blocking reason. Gates are never summarised away — an operator
denied an action is entitled to know exactly which condition failed and what it
currently reads.

### Destructive action block

Any action that removes something from normal use — archiving an aircraft, binning a
capture — uses one pattern:

1. **Name the action** on the aircraft or item, not generically.
2. **List what survives and what changes**, marked ✓ and ✕. Never make the operator guess.
3. **Ask for a reason** in a free-text field, stored with the event.
4. **Keep the confirm button disabled** until the reason is entered.
5. **State why it is available or blocked** underneath.

The operator interface has no immediate permanent deletion. Archive is
reversible. Binned evidence is recoverable during its grace period. State this
recovery behavior at each applicable action.

### Evidence row

Show the thumbnail, type, duration, timestamp, source aircraft, incident,
description, and decision state. Keep non-ready evidence in the list. An
operator must see each assembling, failed, or binned item.

### Flight history row

Show the mission name, **outcome chip**, timestamp, progress, and duration. If
the mission did not complete normally, show the reason. Use outcome text, not
enum names: *Completed*, *Returned home early*, *Stopped by you*, or *Never
started*.

Progress bars are for flights **in motion**. A finished flight states its progress as a
number ("12 of 18 waypoints") — a bar frozen at 67% implies something is still running.

### Mutual-exclusion notice

If current activity blocks an action, show the conflict at the attempted
action. Name the activity and give the operator a safe resolution. For example,
a mission cannot start during Follow from standoff. The Missions section must
offer **Stop following** at the blocked control.

### Instrument readout

Uppercase micro label, condensed tabular value, unit as a smaller inline suffix.
Tone class applied only when the value itself is out of nominal range.

---

## 5. UX writing

### 5.1 Three registers, not two

An earlier version of this document named only two categories and caused a
predictable overcorrection: copy that avoided our jargon by sliding into consumer
software. There are **three** registers, and only one of them is right.

| Register | Examples | Verdict |
|---|---|---|
| **Domain vocabulary the operator owns** | PX4, RTL, Offboard, Hold, gimbal, pitch/yaw, waypoint, telemetry, armed, AGL, AMSL, geofence, nadir, standoff, confidence, retention, checksum | **Keep.** These are the words of the trade. Translating them is condescending and less precise. |
| **Atlas implementation nouns** | lease, authority, envelope, converged, filtered, binding, session-scoped, revision, durable, atomically, asset | **Replace.** These describe how Atlas is built, not what the aircraft is doing. |
| **Consumer-grade over-simplification** | "moving on its own", "91% sure", "the bin", "what the camera sees", "Is this useful?", "you can bring it back" | **Also wrong.** Vaguer, longer, and it talks down to a professional. |

### 5.2 The target register

> **A competent colleague briefing another competent colleague.**

Assumes domain literacy. Assumes no codebase literacy. Never pads.

Two practical tests:

1. **Would a drone operator with no knowledge of Atlas's internals read this and know what to do?** If no, it is tier two.
2. **Would that same operator find it slightly patronising?** If yes, it is tier three.

Precision usually makes copy *shorter*. "Offboard · Atlas is flying it" beats
"Aircraft is moving on its own" on both accuracy and width. When a plain rewrite gets
longer, it is usually the wrong rewrite.

### 5.3 Glossary

Extend this table rather than inventing new phrasings. The third column exists because
both failure directions are real.

| Concept | Too technical | **Correct** | Too plain |
|---|---|---|---|
| Follow, airframe | Follow from standoff | **Aircraft follow** — "flies the aircraft" | "the drone chases it" |
| Follow, gimbal | Image-space gimbal follow | **Camera follow** — "camera only" | "the camera watches it" |
| Taking the gimbal | Acquire payload lease | **Take camera control** | "grab the camera" |
| Gimbal held by mission | Payload lease held by mission intent | **The mission is aiming the camera** | "the mission is busy with it" |
| Offboard control active | PX4 Offboard velocity control engaged | **Offboard · Atlas is flying it** | "aircraft is moving on its own" |
| Geolocation converged | Converged geolocation | **Position fix · ±3.1 m** | "we know where it is" |
| Motion filter passed | Motion status FILTERED | **Target motion · 1.8 m/s bearing 214°** | "movement measured" |
| Inside reviewed envelope | Inside reviewed envelope | **Inside geofence · 310 m of 800 m** | "inside your limits" |
| Detection confidence | Detection score 0.91 | **91% confidence** | "91% sure" |
| Track occluded | TEMPORARILY_OCCLUDED | **Occluded** | "hidden" |
| Telemetry age | Telemetry staleness | **Telem · 0.4 s** | "Data" |
| Relative altitude | Relative altitude (rel) | **Altitude (AGL)** | "height above home" |
| Absolute altitude | Absolute altitude AMSL | **Altitude (AMSL)** | "height above sea" |
| Lease renewing | Operator lease renewing at 1 Hz | **Link · good, 0.4 s ago** | "the control link is still active" |
| Degraded hold | DEGRADED_HOLD | **Follow stopped · holding** | "it gave up and is hovering" |
| Link stale | Heartbeat threshold exceeded | **No signal · 3m 12s** | "we lost it" |
| Response pattern | BOUNDED_AREA_SCAN | **Area scan** | "search an area" |
| Response pattern | HOLD_AT_STAGING | **Hold at staging** | "wait nearby" |
| Response pattern | OFFSET_OBSERVE | **Observe from offset** | "watch from a distance" |
| Response pattern | BOUNDED_ORBIT | **Orbit** | "circle the location" |
| Mission template | WAYPOINT / DIRECT_WAYPOINTS | **Waypoints** | "fly a set path" |
| Prepared assignment | Prepared atomically | **Reserves the aircraft. Nothing is sent to it.** | "holds the drone for this job" |
| Arrival failure policy | Arrival failure policy | **If an action fails** | "what to do if it goes wrong" |
| Evidence item | Evidence asset | **Capture** / **Photo** / **Clip** | "thing the camera got" |
| Review state | Review state UNREVIEWED | **Review decision** | "Is this useful?" |
| Retention class | Retention class STANDARD | **Retention · 30 days** | "how long to keep it" |
| Trash | Trash asset / recoverable trash | **Delete (recoverable)** | "move to the bin" |
| Provenance | Provenance | **Source** | "where this came from" |
| Capability set | Agent capabilities | **Capabilities** | "can do" |
| Upload | Upload mission plan | **Upload** / **Send to aircraft** | "give it to the drone" |

Use `RTL` as a **flight-mode label** in logs and telemetry. Use **Return home**
as a button label. A button gives an instruction, while a readout reports a
state.

### 5.4 General rules

- **State the consequence, not only the mechanism.** Use "Stopping commands PX4
  Hold. It does not trigger RTL or Land."
- **Every word earns its place.** No intro sentence restating the heading.
- **Blocked controls name the blocker and the recovery action.** For example,
  use "Held by the mission. Take control above."
- **Empty states teach.** Say what to do next and where.
- **Errors state the condition and recovery.** For example, use "Telemetry is 8
  seconds old. Reconnect before you start."
- **Never repeat what is already on screen.**
- **Units and precision are part of the message.** Prefer `±3.1 m` to
  "accurate."


## 6. Accessibility and resilience

Accessibility and resilience are requirements for each interface change.

- WCAG AA contrast minimum for all text including placeholders and micro-labels. Field mode targets AAA for body text.
- Never encode status in color alone; always pair with text or a glyph.
- Visible `:focus-visible` on every interactive element.
- Full keyboard reachability, including safety controls.
- `aria-current` on navigation, `role="status"`/`role="alert"` on live regions, `aria-selected` on selection lists.
- `prefers-reduced-motion` honoured for every animation and transition.
- Minimum 44px touch targets where the interface may be used on a touchscreen laptop.

---

## 7. Anti-patterns

Things that have appeared in Atlas or commonly appear in tools like it, and should be
rejected in review:

| Anti-pattern | Why |
|---|---|
| Implementation nouns on screen (lease, authority, envelope, converged) | Forces the operator to translate our architecture mid-flight. See §5. |
| Editorial hero on a working screen | Consumes the most valuable pixels with information the operator learned on day one. |
| A tab per subsystem | Produces navigation only the authors can use. |
| A screen that shows one aircraft when several are flying | Misrepresents the product as single-vehicle. |
| Hardcoded status headings | Can contradict the state they sit above. |
| Safety controls that differ per screen | Defeats spatial memory exactly when it matters most. |
| Zero-padded counts (`00`) | Instrument cosplay; harms legibility and reads as an error code. |
| Motion on page load, none on state change | Spends the entire motion budget on decoration. |
| An active-flight surface with no navigation entry | Makes the most important screen the easiest to lose. |
| Gray text on a coloured background | Reads as washed-out and dead; use a darker shade of the background hue. |
| A destructive confirm that only says "Are you sure?" | Restates the question instead of answering it. List what survives and what changes. |
| Hiding failed or pending items from a list | The operator needs to know a capture did not save. Show it with its state. |
| A progress bar on a finished item | Implies motion that is not happening. Use a number. |
| Discovering a conflict from a rejected command | Say what is blocking, where the operator would try it, with the action that clears it. |
| Dark mode with glowing accents | Looks like a decision without being one; wrong for daylight field use. |

---

## 8. Decisions still needed

Deliberately unresolved:

1. **Selection model on Command.** Does selecting an aircraft in the fleet rail also pan the map, or are they independent? Panning is helpful when hunting; disruptive when monitoring.
2. **Alert escalation.** Should `DEGRADED_HOLD` interrupt with a modal-equivalent, or is a persistent ribbon sufficient? Ribbon is the current proposal on the grounds that modals are usually laziness — but this may be the exception.
3. **Follow lease ownership.** Hoisting renewal above the view layer keeps the session alive across navigation, at the cost of removing an implicit safety guarantee. Both watchdogs remain, so this is judged acceptable — but it should be an explicit decision, recorded here when made.
4. **Narrow-laptop layout.** Below 68rem the three-column layouts collapse to one column. A field laptop at 1280×800 deserves a designed two-column arrangement rather than a fallback.
5. **Evidence treatment.** It is non-real-time and may warrant a visually distinct treatment that signals "you are not supervising a flight right now".
6. **Re-flying a partial mission.** The Missions section states that sending a mission again starts from waypoint 1, because Atlas does not resume a finished flight partway. Whether operators need a "fly the remaining waypoints" path is a real product question raised by the returned-home-early case.
7. **Evidence export.** The proposed design offers "Export copy", which writes a file plus checksum outside Atlas. Remote replication and a full export workflow are named in the docs as separate future concerns; the button may be premature.
8. **Aircraft switcher.** Sections now carry a dropdown to move between vehicles without returning to Command. Whether it should also switch the map focus, and what it does mid-follow, is undecided.
9. **Manual camera control while following.** Taking camera control is blocked by mission intent. Whether an operator may aim the camera manually *during* an aircraft follow is a real product question — the airframe controller yaws to face the target, so manual pan may fight it.

### Review questions carried forward

These questions were restated during the console-overhaul review and remain product
decisions; their wording is retained so implementation history is not mistaken for
product resolution:

1. **Command selection model** — does selecting an aircraft pan the map? Currently it does not, and `onAircraftSelect` on the map sets selection without any visual link back to the rail.
2. **Alert escalation** — is a ribbon enough for `DEGRADED_HOLD`, or does it need to interrupt?
3. **Follow lease ownership** — now implemented as hoisted, so this is settled in practice. Record the decision and its rationale in this document and close the question.
4. **Narrow-laptop layout** — three columns at 1280×800 gives ~19rem rails. Needs a designed two-column arrangement.
6. **Re-flying a partial mission** — unresolved.
7. **Evidence export** — unresolved.
8. **Aircraft switcher** — not implemented at the time of review; the intended switching behavior still needs a product decision.
9. **Manual camera control during follow** — unresolved.

---

## 9. Using this document

When adding or changing operator-facing behavior:

1. Identify which of the five principles the change serves.
2. Reuse an existing component contract before inventing a new one.
3. Add new semantic tokens rather than one-off values; extend both modes together.
4. Check the change against the anti-pattern table.
5. Update this document in the same change when a rule, token, or component contract changes.

The shipped implementation is authoritative for visual detail. This document is
authoritative for intent. Where they disagree, verify whether the implementation or
this document needs correction and update the owning source.

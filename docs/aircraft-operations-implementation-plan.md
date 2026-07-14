# Aircraft Operations Implementation Plan

## 1. Objective and system context

Atlas already supports fleet monitoring, mission planning/execution, on-demand
RTSP video, perception health/detections, and mission-scoped manual payload
override. The missing product boundary is an aircraft-owned inspection
workspace: operators cannot validate video, perception, gimbal movement, or
zoom before a mission owns the aircraft.

This implementation introduces three explicit operational contexts:

1. **Monitor** — fleet state, telemetry, readiness, alerts, and history.
2. **Inspect** — on-demand clean video, optional detections, perception health,
   and leased payload control while the aircraft is safely on the ground.
3. **Execute** — mission-owned navigation and payload intent with a temporary,
   leased operator override.

The work remains local-first. React owns presentation, Rust/Tauri owns policy,
audit, SQLite, RTSP decoding, and the ground-station gRPC service, and the Go
agent owns the physical payload controller and perception-frame transmission.

## 2. Existing behavior

- `LiveVideo` starts Native's RTSP decoder only while mounted.
- Perception health and detection frames are both streamed continuously once
  the agent opens its independent perception stream.
- Payload commands contain `missionRunId` and `controlSessionId`; Native and the
  agent require the mission run to be `RUNNING` or `PAUSED`.
- A seven-second renewable agent-side lease provides exclusive manual payload
  ownership. End or expiry restores the mission's current gimbal/zoom intent.
- Drone rows already support an `archived` status in SQLite, but fleet queries,
  registration, bindings, and UI do not implement archive semantics.
- Automated tests use unique temporary SQLite files. Manual Tauri development
  uses the normal application database unless `ATLAS_SQLITE_PATH` is supplied.

## 3. Safety and lifecycle invariants

The following are acceptance rules, not optional UI behavior:

1. Every physical command requires an active drone, a fresh connected link,
   server-side parameter validation, a durable command receipt, and an agent
   acknowledgement/result.
2. Inspection payload control requires telemetry received within the Native
   freshness window with `armed = false` and `inAir = false`. Unknown values
   are not treated as safe.
3. Mission override requires the targeted drone's mission run to be `RUNNING`
   or `PAUSED`.
4. A control context is a discriminated union. `inspection` cannot carry a
   mission run; `mission_override` must carry one.
5. One agent-side payload-control session owns the payload at a time. A second
   session is rejected even if it uses a different context.
6. Begin and renew accept only 3–15 second leases. Loss of the WebView, Native,
   link, or renew loop cannot leave rate control active indefinitely.
7. Inspection end/expiry sends zero angular rates and releases Gimbal v2
   primary control. Mission-override end/expiry sends zero rates and restores
   the current mission gimbal/zoom intent.
8. Mission start is rejected while an inspection session owns the payload.
9. Archived drones are absent from normal fleet counts/selectors, retain all
   operational history, have no active binding, cannot receive commands, and
   cannot silently reactivate on agent reconnect.
10. Perception health remains low-rate and continuous. Detection-frame metadata
    is sent only while a renewable UI subscription or an active mission requires
    it. Onboard inference remains running for the first release.

## 4. Command and state contracts

### 4.1 Payload control context

All payload-control commands use the following JSON shape:

```ts
type ControlContext =
  | { kind: "inspection" }
  | { kind: "mission_override"; missionRunId: string };

type PayloadCommandIdentity = {
  controlContext: ControlContext;
  controlSessionId: string;
  gimbalId: number;
  cameraComponentId: number;
};
```

`payload_control_begin` and `payload_control_renew` additionally require
`leaseDurationMs`. Gimbal and camera commands carry the same identity so the
agent can prove that the command belongs to the current leased owner.

The control state machine is:

```text
idle
  -> acquiring
      -> active
          -> renewing -> active
          -> ending   -> safe terminal action -> idle
          -> expired  -> safe terminal action -> idle
      -> rejected -> idle
```

The safe terminal action depends on the context:

```text
inspection       -> zero angular rates -> release gimbal control
mission_override -> zero angular rates -> restore current mission intent
```

### 4.2 Perception frame subscription

The bidirectional perception stream gains a Native-to-agent subscription
message containing:

- `subscriptionId`: stable for one mounted consumer;
- `purpose`: `live_view` initially;
- `action`: `START_OR_RENEW` or `STOP`;
- `leaseDurationMs`: 3–30 seconds for start/renew.

The agent consumes all local runtime frames so the runtime cannot block, but it
forwards frame metadata only when either condition is true:

```text
unexpired Native subscription exists
OR mission state is RUNNING/PAUSED
```

Health messages bypass this gate. Closing the perception stream clears remote
subscriptions. Mission demand is derived from mission executor updates and is
therefore independent of whether the camera surface is visible.

### 4.3 Drone lifecycle

```text
active/maintenance/disabled
  --archive while not freshly connected-->
archived + binding ended + open stale link closed

archived --agent registration--> rejected + lifecycle event
archived --operator restore--> active (no binding is recreated)
active   --next registration--> new active binding/link
```

Archive and restore are idempotent. Hard deletion is explicitly out of scope.

## 5. Data model changes

Schema version 12 adds `drone_lifecycle_events`:

```text
id, drone_id, event_type, reason, occurred_at_unix_ms, details_json
```

Events include `archived`, `restored`, and
`archived_reconnect_rejected`. The existing `drones.status` value remains the
source of truth. Missions, mission runs, commands, telemetry, status events,
agents, and ended bindings are retained unchanged.

Fleet reads accept `includeArchived`; normal polling uses `false`. The archived
filter requests archived rows explicitly and never mixes them into operational
fleet totals or mission aircraft selectors.

## 6. UI implementation

The selected aircraft becomes a persistent workspace with five sections:

- **Overview** — existing link, telemetry, readiness, power, and recent PX4
  information.
- **Live** — `LiveVideo`, perception readiness, and inspection payload control.
- **Missions** — current assignment first, then previous runs, with deliberate
  navigation into the mission workspace.
- **History** — the existing telemetry/event history scoped to the aircraft.
- **Settings** — identity/lifecycle state, inline archive confirmation, and
  restore for archived aircraft.

The aircraft name, link status, armed state, flight state, and lifecycle state
remain visible above every section. Merely opening **Live** observes; the
operator must explicitly choose **Take inspection control** before controls are
enabled. Control acquisition, acknowledgement, lease failure, ending, and safe
release are announced with text as well as colour.

The mission execution workspace reuses the same payload-control engine with a
`mission_override` context and retains its mission-specific view/restore copy.

## 7. Implementation slices

### Slice A — contracts and agent safety

1. Generalize Rust validation/policy to the discriminated control context.
2. Generalize the Go payload controller session model.
3. Implement context-specific end/expiry behavior.
4. Block mission activation while inspection owns the payload.
5. Update unit/integration fixtures and agent documentation.

Acceptance:

- inspection begins without a mission when Native safety gates pass;
- mission override still restores the current waypoint intent;
- mismatched contexts/sessions are rejected;
- inspection end and expiry both stop rates and release control;
- mission activation cannot race an inspection lease.

### Slice B — demand-driven detection transmission

1. Extend and regenerate the shared protobuf contract.
2. Store the perception response sender for the registered drone stream.
3. Add Tauri start/renew/stop subscription commands.
4. Add the agent frame-demand lease set and mission-state demand.
5. Renew a live-view subscription from `LiveVideo`; stop it on unmount.

Acceptance:

- health is forwarded with no subscriber;
- frames are suppressed with no subscriber and no active mission;
- a valid lease enables frames and expiry disables them;
- `RUNNING`/`PAUSED` mission state enables frames independently;
- multiple UI consumers cannot disable one another.

### Slice C — aircraft operations workspace

1. Add persistent aircraft identity/status chrome and section navigation.
2. Preserve the existing overview.
3. Add the Live inspection surface using reusable video/payload components.
4. Add aircraft-scoped mission runs and history surfaces.
5. Add empty, unavailable, stale, unsupported-capability, pending, and error
   states with keyboard-safe controls and cleanup on unmount.

Acceptance:

- operators can observe video without acquiring payload ownership;
- controls remain unavailable until the inspection lease is acknowledged;
- switching aircraft ends the old control and video/perception subscriptions;
- mission execution uses `mission_override`, not a synthetic mission;
- aircraft identity and physical state remain visible wherever controls appear.

### Slice D — archive and restore

1. Add migration 12 and database lifecycle methods.
2. Reject archive while a fresh connection exists.
3. End bindings/links and write lifecycle events atomically.
4. Reject archived reconnects before binding/link creation and audit them.
5. Filter archived rows by default; add archived fleet view and restore UI.

Acceptance:

- archived rows disappear from normal fleet counts and mission selectors;
- history remains queryable;
- reconnect does not change archived status or create an active binding;
- restore permits a later registration but does not fabricate a connection;
- repeated archive/restore requests are safe.

### Slice E — isolated development database

1. Add `npm run tauri:dev:isolated`.
2. Store its database under ignored `.atlas-run/native-dev/` by default.
3. Preserve an explicitly supplied `ATLAS_SQLITE_PATH`.
4. Update the Native README and schema documentation.

Acceptance:

- the safe development command never opens the installed application's normal
  database;
- its path is absolute, deterministic, ignored, and reusable across restarts;
- automated tests continue using unique temporary databases.

## 8. Validation plan

Run, in order:

1. `cargo fmt --check` and targeted Rust database/ground-station tests.
2. Full `cargo test` for Atlas Native.
3. `gofmt` and targeted payload/perception transport tests.
4. Full `go test ./...` for Atlas Agent.
5. `npm run build` for strict TypeScript and production Vite bundling.
6. Inspect the final Git diff for generated protobuf consistency, accidental
   database artifacts, and unrelated user changes.

Physical gimbal, Hailo, camera, and HM30 behavior cannot be proven by local
automated tests. The field smoke test is: connect a disarmed aircraft, open
Live, verify health before overlay subscription, acquire inspection control,
move/stop/zoom, leave Live and confirm release, execute a mission override and
confirm current-waypoint restoration, then archive while disconnected and
verify reconnect rejection.

## 9. Rollout and compatibility

This is a coordinated Native/agent protocol release. The protobuf change is
backward-incompatible at the behavior level even though new fields are additive:
an older agent will continue sending frames continuously and cannot understand
leases. Native and agent packages should therefore ship together and advertise
the updated perception subscription capability. Payload parameter JSON is also
changed in lockstep; mixed versions will reject manual payload commands rather
than executing them ambiguously.

No destructive migration or hard-delete path is introduced. Existing command,
mission, telemetry, and registration history remains readable.

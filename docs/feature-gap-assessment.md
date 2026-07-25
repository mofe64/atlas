# Atlas Feature Gaps

**Status:** Current product-direction summary; not a shipped-behaviour contract
**Deployment scope:** Public-safety operations in the United Kingdom and Nigeria

For implemented behaviour, use the [developer documentation index](README.md),
[mission guide](mission-types-and-flight-patterns.md),
[incident guide](incident-dispatch.md), and
[perception/tracking/follow guide](inference-tracking-and-follow.md).

## Current direction

Atlas is a local-first aircraft operations system:

- Native remains the operator and durable operational authority.
- Agent owns onboard PX4, payload, perception, and explicit controller
  integration.
- PX4 owns vehicle state estimation, stabilization, modes, and failsafes.
- Backend is optional and is not in the live aircraft-control path.

External services may provide context, identity, coordination, or replicas.
Their outage must not interrupt a current local flight.

The installed payload model is deliberately concrete:

- SIYI A8 supplies visible video and gimbal control.
- Hailo supplies onboard detector inference.
- Atlas tracking owns normalized track identity and lifecycle.
- OAK-D Lite is the current calibrated-depth provider.
- H-Flow supplies optical flow and range to PX4.

Provider details stay behind adapters. They must not become universal
capability gates.

## Implemented foundation

The current system already provides:

- direct Agent-to-Native registration, telemetry, status, and reconnect;
- durable Hold, RTL, Land, mission, gimbal, zoom, and action lifecycles;
- immutable waypoint, route-scan, area-scan, and orbit plans;
- manual incidents with reviewed Staging, Offset Observe, Area Scan, and Orbit
  responses;
- persistent Map, Video, and Split operations views;
- local segmented recording, stills, event clips, provenance, retention, and
  evidence-gap records;
- Hailo detection, ByteTrack/CMC tracking, counts, selections, and track-linked
  evidence;
- selected-track geolocation and commissioned-by-profile aircraft Follow
  software with fail-closed watchdogs;
- terrain profiling and an offline known-building warning layer; and
- calibrated metric depth and local health from the independent spatial
  provider.

This list is an index, not a duplicate specification. Follow the linked
architecture documents for exact contracts.

## Priority gaps

### 1. Outdoor obstacle avoidance

Indoor navigation, accumulated mapping, the hold-only indoor mission, and its
Native cloud viewer are decommissioned.

The replacement should start with a bounded obstacle-observation contract:

- capture time and explicit expiry;
- sensor and body frame;
- units and valid range;
- compact occupied sectors or points;
- provider/component health; and
- an independently versioned capability contract.

The first implementation must not revive pose estimation, persistent maps, or
a ground-station visualization dependency. It must define stop/hold behaviour,
controller authority, stale-data handling, and the flight envelope before it
can authorize movement.

Open decisions:

- observation representation and maximum lifetime;
- horizontal/vertical field partitioning and clearance envelope;
- which PX4 state is required by the consumer;
- controller ownership relative to missions and Follow;
- bench, grounded-aircraft, and flight acceptance criteria; and
- the Pi-native provider/service dependency declaration.

### 2. Authoritative airspace inputs

No automated UK or Nigerian airspace integration is approved.

Resume this work only when Atlas has a documented machine interface and
operational reuse terms from NATS/NAMA or a licensed provider. Every item must
retain country, issuing authority, provider, effective interval, retrieval
time, source version, and licence basis. Missing or stale coverage must never
be presented as clear airspace.

### 3. Identity and multi-operator authority

Native currently assumes one local operational authority. Before shared or
remote operations, define:

- authenticated operators and offline access;
- one renewable command lease per aircraft;
- explicit, audited handoff;
- observer-only access;
- local emergency authority; and
- safe behaviour when coordination services disappear.

### 4. Evidence export and replication

Local evidence capture and retention exist. Remaining work is:

- a reviewed export package and manifest;
- encryption and key policy;
- storage-capacity and reserve policy;
- verified remote replication; and
- explicit behaviour during loss, retry, conflict, and partial upload.

Remote storage is a replica, not a prerequisite for local capture.

### 5. Perception and geolocation acceptance

The software foundation exists, but each supported hardware profile still
needs representative evidence for:

- detector/tracker latency, memory, continuity, and accuracy;
- camera/zoom calibration and physical boresight;
- terrain/range source authority and uncertainty;
- surveyed geolocation accuracy; and
- selected-track Follow HIL and controlled-flight behaviour.

ReID remains disabled until its measured value justifies cost and identity
risk. Aircraft movement must never be commanded directly from bounding-box
pixels.

### 6. Broader obstacle and terrain sources

The current OS building layer is a known-building warning, not proof of an
obstacle-free route. Decide whether operational scope requires vegetation,
wires, masts, cranes, temporary obstacles, or local surveys, and define
freshness/provenance before integrating them.

## Deferred or rejected scope

Do not prioritize:

- full CAD replacement or mock incident connectors;
- unofficial scraped airspace feeds;
- street/address labels projected into live video;
- virtual-joystick flight control;
- talk-down/audio without a payload requirement;
- decorative 3D city views;
- unrestricted autonomous pursuit;
- replacing Hailo solely for tracker convenience; or
- continuous full-rate VLM processing.

Add thermal or other payload controls only when real hardware and a verified
transport exist.

## System invariants

All future work must preserve:

1. Native records durable intent before delivery.
2. Agent validates identity, deadlines, capabilities, and local safety state.
3. Only one component owns a movement or payload-control lease at a time.
4. High-rate streams use bounded/latest-value transport; durable lifecycle
   events are not silently dropped.
5. Optional provider loss degrades only capabilities that consume it.
6. Backend or internet loss does not interrupt current local control.
7. Missing, stale, unknown, or unverified data is never promoted to safe.
8. Hardware acceptance is installation-specific and is invalidated by relevant
   sensor, mount, controller, or dependency changes.
9. Current architecture docs describe current code; historical evidence lives
   in handovers and Git.

## Open product decisions

- Initial single-operator versus multi-operator deployment.
- Maximum response distance and expected radio envelope.
- Required local evidence capacity, retention, and encryption.
- Approved airspace providers and outage guarantees in each country.
- Approved terrain/elevation source for geolocation.
- Maximum geolocation uncertainty for aircraft Follow.
- Follow standoff, speed, altitude, duration, boundary, and recovery defaults.
- Companion-computer performance budgets by supported aircraft profile.

## Immediate sequence

1. Finish removing stale indoor/release/documentation surface.
2. **Completed:** move the reduced spatial provider to an independently
   packaged Pi-native DepthAI service installed alongside Agent before one
   `atlas-setup` pass.
3. Define and test the compact obstacle-observation contract.
4. Implement perception-only obstacle extraction with explicit expiry.
5. Design controller authority and fail-safe behaviour.
6. Run bench, grounded-aircraft, and constrained-flight acceptance before
   enabling movement.

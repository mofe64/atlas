# Atlas Terminology

This glossary is the controlled project vocabulary for Atlas documentation.
Use each term with the meaning in this document. Keep code and protocol
literals unchanged when they use a different form.

## System components

| Term | Meaning | Permitted short form |
| --- | --- | --- |
| **Atlas** | The complete local-first drone operations system. | None |
| **Atlas Native** | The Tauri desktop ground station. It contains the React interface and the Rust operational host. | Native, after the first full use |
| **React interface** | The presentation layer inside Atlas Native. | interface |
| **Rust host** | The trusted process inside Atlas Native that owns local policy, persistence, and services. | host |
| **Atlas Agent** | The Go runtime on the onboard computer. | Agent, after the first full use |
| **Atlas Spatial Runtime** | The independent onboard process that owns the configured depth camera and calibrated depth boundary. | Spatial, after the first full use |
| **Atlas Backend** | The separate HTTP and PostgreSQL service for identity and future coordinated services. | Backend, after the first full use |
| **PX4** | The flight controller and flight-control authority. | None |
| **`mavsdk_server`** | The local typed MAVSDK-to-MAVLink service used by Atlas Agent. | None |
| **perception runtime** | An accelerator-specific process that produces normalized detections and health. | runtime, when unambiguous |

## Identity and connection

| Term | Meaning |
| --- | --- |
| **aircraft** | The physical flight vehicle. |
| **drone** | The durable Atlas record for one physical aircraft. |
| **vehicle agent** | The durable identity of one Atlas Agent installation. |
| **binding** | The durable association between a vehicle agent and a drone. |
| **registration** | The protocol exchange that identifies an Agent session to Atlas Native. |
| **communication link** | One Agent-to-Native network session. A reconnect creates a new link. |
| **connected** | A communication link is active. This term does not mean registered, bound, ready, or safe to fly. |
| **fresh** | The value age is inside the limit for the applicable operation. Always identify or link to the limit. |
| **stale** | The value age is outside the applicable freshness limit. |

## Commands and missions

| Term | Meaning |
| --- | --- |
| **vehicle command** | A durable request for one aircraft operation and its lifecycle events. |
| **mission definition** | Editable operator intent and mission parameters. |
| **mission plan** | The immutable, reviewed waypoints and semantic actions produced from a mission definition. |
| **mission run** | One execution history that binds a mission plan to one drone. |
| **semantic action** | An Atlas action with operational meaning that can require an acknowledgement, such as an arrival or perception action. |
| **arrival action** | A semantic action that Atlas executes at a reviewed incident-response trigger. |
| **mission progress** | Reported execution position within an uploaded mission. It is not proof that a semantic action succeeded. |
| **terminal state** | A final lifecycle state from which normal execution does not continue. Name the exact state when it matters. |

## Incidents

| Term | Meaning |
| --- | --- |
| **incident** | A revisioned operational event and target location that can require an aircraft response. |
| **incident revision** | The version of incident data used for assessment and planning. |
| **incident assignment** | The durable reservation that connects an incident, drone, response plan, and execution state. |
| **response pattern** | Reviewed incident-specific geometry and arrival behavior. |
| **suitability assessment** | The recorded evaluation of whether a drone can perform the selected response. |

## Perception, evidence, and control

| Term | Meaning |
| --- | --- |
| **perception source** | One accelerator-neutral stream of normalized detections and health for a camera source ID. |
| **detection** | One model observation in one source frame. |
| **track** | Atlas-owned temporal association of detections inside one track session. |
| **track session** | The identity boundary within which Atlas track IDs are valid. |
| **selection** | The exact operator choice of one track session and one track ID. |
| **camera follow** | Image-space control that moves only the gimbal to center the selected track. |
| **Follow from standoff** | Geographic-space control that moves the aircraft relative to a filtered selected-target estimate. Preserve this capitalization for the product feature. |
| **payload control lease** | Time-limited authority for manual gimbal or camera control. |
| **aircraft-follow lease** | Time-limited authority for Follow from standoff. |
| **capture** | An explicit photo or bounded event clip saved from camera operations with the source context needed to reopen it. A capture is not the bounded live-frame history. |
| **clean video** | Decoded or recorded source video without a permanently rendered detection overlay. |
| **frame demand** | A bounded request for perception frames from an active consumer. |

## Requirement words

| Word | Use |
| --- | --- |
| **must** | A required condition or action. |
| **must not** | A prohibited condition or action. |
| **can** | A capability or possible result. |
| **may** | Permission. Do not use it only to mean possibility. |
| **should** | A recommendation with a valid exception. |

## State-name rule

Use the human-readable term in normal prose. Include the exact literal when a
developer must compare code, protocol, or database state.

Example:

> The mission run enters the staged state (`STAGED`) after Atlas Agent
> acknowledges Hold at Staging.

Do not create an informal state name when the system has an exact state.

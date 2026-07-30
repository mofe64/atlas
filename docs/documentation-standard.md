# Atlas Technical Documentation Standard

This standard defines how to write and maintain Atlas documentation. It uses
ASD-STE100 principles for clear technical English. It also keeps the detail
that developers need to understand software architecture, state, and failure
behavior.

Atlas documentation is **ASD-STE100-aligned**, not independently certified as
fully compliant. A certified claim requires trained review against the complete
current standard and its controlled dictionary.

## Objectives

Atlas documentation must help a reader to:

1. identify the component that owns a behavior;
2. understand the normal data and control flow;
3. identify the applicable safety and freshness rules;
4. complete an operation without an ambiguous instruction;
5. identify a successful result and a failure result; and
6. find the authoritative code, configuration, or protocol contract.

Technical accuracy has priority over linguistic simplicity. Do not remove a
condition, exception, unit, state, or failure mode to make a sentence shorter.
Split the information into more sentences or a table instead.

## Document profiles

Atlas uses two language profiles.

### Procedural profile

Use this profile for installation, configuration, maintenance, verification,
troubleshooting, acceptance, and recovery procedures.

Apply these rules:

- Write an instruction as a command.
- Put one action in each numbered step.
- Put a condition before the action when the condition controls the action.
- Identify the component or person that does the action.
- Give the expected result after a command or group of commands.
- Put a warning or caution before the action that can cause harm.
- Use no more than 20 words in a procedural sentence when practical.
- Do not combine alternatives with an instruction. Give each alternative its
  own step or conditional branch.
- Do not use an unqualified result such as "works", "looks good", or "is
  healthy". State the value, state, message, or observation that proves the
  result.

The following documents use the procedural profile:

- `atlas-agent/INSTALLATION.md`
- `docs/development-guide.md` when it gives commands or checklists
- `docs/h-flow-px4-setup-and-verification.md`
- `docs/indoor-navigation-decommission-audit.md`
- the run and validation sections in component README files

### Developer explanation profile

Use this profile for architecture, design, protocol, data-model, and
implementation documents.

Apply these rules:

- State the purpose and system boundary before implementation detail.
- Use no more than 25 words in a descriptive sentence when practical.
- Prefer active voice when ownership matters.
- Name the owner of policy, state, and side effects.
- Use one term for one concept.
- Define a project-specific term at its first important use.
- Separate current behavior from planned behavior.
- Explain state transitions as ordered lists, tables, or diagrams.
- State invariants and failure behavior explicitly.
- Keep code identifiers, API fields, database names, commands, and literal
  messages unchanged.

Longer sentences are permitted when splitting them would separate a necessary
condition from its consequence. Clarity is the test, not the word count alone.

## Core writing rules

### Use one term for one concept

Use the terms in [Atlas terminology](terminology.md). Do not use a synonym when
the synonym can imply a different owner or state.

Examples:

| Do not alternate between | Use |
| --- | --- |
| desktop, ground app, GCS | Atlas Native |
| onboard service, companion app | Atlas Agent |
| cloud, API, central service | Atlas Backend |
| job, flight, execution | mission run, when that record is meant |
| online, attached, paired | connected, registered, or bound, as applicable |

After the first full use in a document, **Native**, **Agent**, and **Backend**
are permitted short forms when the component remains unambiguous. Do not use a
short form where it can mean a generic software role.

### Identify ownership

Write:

> Atlas Native validates the request and records the command.

Do not write:

> The request is validated and recorded.

Passive voice is acceptable when the actor is unknown or not important. Do not
use passive voice when it hides control authority or a side effect.

### Make conditions explicit

Write:

> If telemetry is stale, Atlas Native rejects the command.

Do not write:

> Atlas Native normally rejects commands when telemetry is not recent enough.

Use **must** for a requirement, **must not** for a prohibition, **can** for a
capability, and **may** only for permission. Use **should** only for a
recommendation.

### Keep sentences direct

- Use the active verb near the subject.
- Use American English spelling in project prose: `behavior`, `center`, `color`,
  `meter`, and `millimeter`.
- Remove introductory phrases that do not change the meaning.
- Do not use contractions.
- Avoid words such as "simply", "just", "obviously", and "clearly".
- Replace vague references such as "this", "that", and "it" when the referenced
  item is not the closest clear noun.
- Use a list when a sentence contains three or more peer items.
- Use positive instructions when possible. Use a negative instruction for a
  prohibition or safety boundary.

### Preserve technical literals

Do not rewrite:

- source-code identifiers;
- protobuf messages or fields;
- database table, column, and state names;
- environment variables;
- command-line flags and commands;
- file paths;
- IP addresses, ports, units, and configuration values;
- product names and official PX4, MAVSDK, MAVLink, DroneCAN, SIYI, Hailo, or
  DepthAI terms;
- text that the software displays or records.

Put literals in backticks. Explain them in controlled language outside the
literal.

### Distinguish current and future behavior

Use these labels:

- **Current:** the repository implements and supports the behavior.
- **Planned:** the behavior is approved work but is not implemented.
- **Unsupported:** Atlas does not authorize or support the behavior.
- **Deprecated:** the behavior remains temporarily but must not be used for new
  work.

Do not describe planned behavior in the present tense.

## Procedure structure

Use this order when the sections apply:

1. Purpose
2. Scope and authority
3. Prerequisites
4. Safety
5. Procedure
6. Verification
7. Recovery or rollback
8. Troubleshooting
9. References

For each verification, identify:

- the command or action;
- the expected result;
- the failure indication; and
- the next safe action.

## Architecture-document structure

Use this order when the sections apply:

1. Purpose and boundary
2. System context
3. Component responsibilities
4. Control flow
5. Data flow
6. State or lifecycle model
7. Invariants
8. Failure behavior
9. Security or trust boundaries
10. Code map

Do not duplicate detailed behavior from another canonical document. Give the
reader the boundary and link to the owning document.

## Safety language

Use these labels consistently:

- **Warning:** an action can cause injury, loss of the aircraft, or unsafe
  aircraft movement.
- **Caution:** an action can damage equipment, corrupt data, or invalidate a
  test.
- **Note:** information helps the reader but does not describe a hazard.

Put the label before the related action. State the hazard, the prevention, and
the possible result.

Example:

> **Warning:** Remove the propellers before you power the aircraft on the
> bench. An unexpected motor command can cause injury.

Do not use a warning label to emphasize ordinary information.

## Tables, diagrams, and code blocks

- Use a table for exact field mappings, responsibilities, states, or repeated
  comparisons.
- Use a diagram when three or more components exchange data or authority.
- Introduce every diagram with the fact that the reader must learn from it.
- Describe the important conclusion after a complex diagram.
- Put commands in a fenced code block.
- State the working directory when a command depends on it.
- Do not put multiple user actions in one shell line unless the actions form
  one atomic command.

## Review checklist

Before you merge a documentation change, confirm that:

- the code and configuration support every statement about current behavior;
- the document uses the applicable profile;
- each safety-critical instruction has an explicit condition and result;
- each state name and project term matches [Atlas terminology](terminology.md);
- current, planned, unsupported, and deprecated behavior are distinct;
- links and anchors resolve;
- commands are safe for the documented context;
- units and coordinate frames are explicit;
- diagrams agree with the text; and
- the relevant behavioral document changed with the code.

## Standard reference

Use the current official
[ASD-STE100 Simplified Technical English standard](https://www.asd-ste100.org/)
when a project requires a formal compliance review. This Atlas profile is based
on ASD-STE100 Issue 9, dated 15 January 2025.

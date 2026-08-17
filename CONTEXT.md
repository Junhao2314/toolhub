# ToolHub

ToolHub is a control plane that stores reusable configuration and converges
selected runtime targets onto an explicit desired state.

## Language

**Library**:
The central collection of immutable, content-addressed Skill artifacts and MCP
definitions available for reuse.
_Avoid_: Runtime inventory, deployment target

**Skill Tag**:
A normalized lowercase metadata label (slug, up to 50 per Skill) on a Library
Skill. The reserved `required` tag means every non-`shared` Profile revision
must include that Skill; new revisions that omit it are rejected.
_Avoid_: Free-text category, profile membership flag

**Skill Import**:
Copying a Skill discovered on a local runtime into the Library. Import changes
the Library only and never changes a runtime target.
_Avoid_: Apply, delivery, export

**Batch Skill Import**:
One request to import multiple selected local-runtime Skills independently.
Valid items may succeed when other items fail; it never Applies a Profile.
_Avoid_: Profile Bundle Import, atomic batch

**Hermes Skill Intake**:
Importing selected, eligible Skills from the bounded local Hermes inventory into
the Library. Hermes remains an inventory source and never becomes a writable
Skill Target.
_Avoid_: Hermes Apply, Salt intake, path import

**Inventory Candidate**:
A safely identified runtime Skill that may be selected for intake or retained as
a visible non-importable result with a reason.
_Avoid_: Filesystem path, Library member

**Profile**:
A named, client-specific task configuration that pins exact Skill versions for
delivery to compatible Targets. MCP configuration is not Profile membership;
it is owned by the Shared Relay Configuration edited from the MCP page.
_Avoid_: Installed state, runtime inventory, client session

**Profile Revision**:
An immutable state of a Profile containing its exact member versions and Secret
bindings. Changing members or bindings creates a new revision under the same
Profile name.
_Avoid_: Target snapshot, in-place Profile mutation

**Profile Archive**:
A reversible state that removes a Profile from new Preflight and Apply choices
while retaining its protected history.
_Avoid_: Delete, Purge

**Profile Purge**:
The irreversible removal of retention-eligible, unreferenced Profile history.
Purge does not remove shared or otherwise referenced Library contents.
_Avoid_: Archive, Target Restore

**Profile Refresh**:
An explicit change that advances a Profile to selected newer Library Skill
versions. Refresh changes the Profile but does not Apply it to any Target.
_Avoid_: Automatic update, Apply Profile

**MCP Config Revision**:
An immutable version of one MCP server's launch or connection configuration.
Relay Configuration revisions pin these rows; Profiles do not.
_Avoid_: Observed Contract Revision, mutable MCP row

**Observed Contract Revision**:
An immutable observation of one running MCP server's tool identities, schemas,
annotations, and presentation metadata. It changes only through observation;
the accepted baseline changes only after explicit review.
_Avoid_: MCP Config Revision, package version

**Shared Relay Runtime**:
The single persistent local mcpm runtime whose configured MCP servers are each
started once and reused by every Claude and Codex client session.
_Avoid_: Profile runtime, per-client MCP process

**Relay Configuration Revision**:
An immutable desired set of exact MCP Config Revisions started by the Shared
Relay Runtime. It is infrastructure state and is not a user task Profile.
_Avoid_: Profile Revision, observed runtime inventory

**MCP Enabled State**:
Whether an MCP Config Revision is included in the applied Relay Configuration.
Disabled means configured-off, not a process failure.
_Avoid_: Profile membership, tool visibility policy

**Published Profile Revision**:
The exact Profile Skill revision successfully delivered to a target. Saving a
newer draft does not publish it.
_Avoid_: Shared relay MCP owner, client session activation

**Shared Relay Member Health**:
The bounded mcpm/systemd/HTTP projection for one configured MCP member. A
healthy compatibility relay reports `ready`; an intentionally disabled member
reports `disabled`; genuine runtime failure reports `unavailable`.
_Avoid_: Synthetic MCP call, Profile health

**Agent Profile**:
A reusable, client-specific subagent team definition that a Profile Revision may
pin. Many Agent Profiles may exist, but one Profile Revision selects at most one.
_Avoid_: Agent runtime, stacked Profile, client session

**Agent Profile Revision**:
An immutable state of an Agent Profile containing its scheduler Skill and exact
Agent Role definitions. Changes create a new revision without advancing Profiles
that pin an older revision.
_Avoid_: Profile Revision, in-place Agent Profile mutation

**Agent Role**:
A named responsibility within an Agent Profile, including its model binding,
reasoning policy, permissions, and Role Skill Set.
_Avoid_: Agent process, client session, Profile

**Role Skill Set**:
The exact pinned Skill versions made available to one Agent Role. It is part of
an Agent Profile Revision and is not an independently Applied Profile.
_Avoid_: Nested Profile, inherited runtime inventory

**Role Execution Policy**:
The permission ceiling for one Agent Role. Read-only roles run without prompts;
writer roles may change only the current workspace without prompts, may never
spawn subagents, and cannot exceed the parent session's permissions.
_Avoid_: Unrestricted YOLO, parent permission override, approval workflow

**Model Gateway**:
The single shared local data-plane service that presents client-compatible model
endpoints and resolves role model aliases to configured Provider Connections.
ToolHub manages its desired configuration and health but is not in the request path.
_Avoid_: MCP relay, Agent runtime, ToolHub HTTP API

**Provider Connection**:
A versioned model-service endpoint, protocol, capability description, and
write-only Secret binding available to the Model Gateway.
_Avoid_: MCP server, plaintext API key, Agent Role

**Provider Verification Mode**:
The Provider Connection rule for metadata-only checks, payload-free passive
observation from real calls, or no network verification. Verification never
sends a synthetic inference prompt; an untested connection remains usable but
is shown as unverified rather than healthy.
_Avoid_: Mandatory probe, synthetic prompt, model benchmark

**Model Route**:
An immutable ordered route containing one primary Provider model and at most one
optional fallback Provider model. The two entries may use different Providers
and model identifiers and are never inferred from matching names.
_Avoid_: Automatic model discovery, load-balancing pool, retry chain

**Role Model Binding**:
The exact Model Route Revision and reasoning policy assigned to one Agent Role
through a stable Model Gateway alias.
_Avoid_: Automatic fallback, client-global model, Provider Connection

**Pending Secret Binding**:
An imported Profile or MCP revision whose required Secret values have not yet
been supplied and confirmed. It may be saved, but it cannot be preflighted or
Applied until every required binding is complete.
_Avoid_: Pending import, Installed state

**Secret Binding**:
An installation-local association between an MCP Secret slot and an encrypted
Secret record. Secret-only differences create a new revision but do not require
a new Profile name.
_Avoid_: Plaintext Secret, Bundle passphrase

**Profile Bundle**:
A portable package containing one Profile plus the exact Library versions and
contents it needs, used to reproduce that configuration on another ToolHub
installation. A standard Profile Bundle contains no Secret values.
_Avoid_: Library export, Target backup, Apply

**Secret-Bearing Profile Bundle**:
An explicitly requested Profile Bundle that also contains the Profile
revision's referenced Secret values in plaintext. It is a credential backup,
not a shareable Bundle.
_Avoid_: Standard Profile Bundle, encrypted backup

**Profile Bundle Export**:
Creating a downloadable Profile Bundle from ToolHub. It does not read or change
a Target.
_Avoid_: Apply Profile, Target backup

**Profile Bundle Import**:
Loading a Profile Bundle into a ToolHub Library and creating its Profile.
Importing a bundle does not Apply it to a Target.
_Avoid_: Skill Import, Apply Profile

**Bundle Origin**:
Self-described export metadata consisting of a safe instance label and export
time. It is display provenance, not a trust assertion.
_Avoid_: Member source, hostname, managed username

**Member Provenance**:
The recorded intake or upstream source of an individual Library member, such as
Claude, Codex, Hermes, Git, SkillsMP, or Xiaping.
_Avoid_: Bundle Origin, Target

**Apply Profile**:
Converging a writable target's manageable scope onto exactly one Profile. Apply
is a mirror operation, not an additive merge.
_Avoid_: Install one Skill, append, export

**Target**:
A runtime-specific destination or inventory surface on a local host or Salt
minion.
_Avoid_: Profile, node

**Target Projection**:
The runtime-applicable subset of one Profile revision delivered to a specific
Target. Local Claude/Codex and Salt Claude/Codex Targets receive the Profile's
pinned Skills; `local/shared-relay` receives the independent Relay
Configuration owned by the MCP workflow. Profile revisions never contribute
MCP members or routing policy.
_Avoid_: Partial Profile, Target override

**Desired State**:
The exact, pinned target contents established by Apply Profile, Relay
Configuration Apply, or Restore and subsequently maintained by reconcile.
Long-term Skill changes are made through Profile revisions; shared MCP changes
are made through Relay Configuration revisions.
_Avoid_: Latest Library versions, observed inventory

**Restored Desired State**:
An emergency Target-specific desired state created from a retained backup. It
may be reconciled until the Target is returned to Profile management.
_Avoid_: Profile Revision, Target edit

**Profile Adoption**:
Creating a new Profile from a verified Restored Desired State. Adoption copies
its exact available member revisions and never Applies them automatically.
_Avoid_: Target Edit, automatic Apply

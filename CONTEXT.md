# ToolHub

ToolHub is a control plane that stores reusable configuration and converges
selected runtime targets onto an explicit desired state.

## Language

**Library**:
The central collection of immutable, content-addressed Skill artifacts and MCP
definitions available for reuse.
_Avoid_: Runtime inventory, deployment target

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
A named combination of exact Library Skill versions and MCP revisions that
defines a stable target desired state.
_Avoid_: Installed state, runtime inventory

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
versions or MCP revisions. Refresh changes the Profile but does not Apply it to
any Target.
_Avoid_: Automatic update, Apply Profile

**MCP Revision**:
An immutable version of an MCP definition that a Profile can pin. A newer MCP
revision does not change Profiles that reference an older revision.
_Avoid_: Mutable MCP row, current MCP configuration

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
Target. Local Skill Targets receive Skills, the local Shared Relay receives MCP,
and Salt Claude/Codex Targets receive both.
_Avoid_: Partial Profile, Target override

**Desired State**:
The exact, pinned target contents established by Apply Profile or Restore and
subsequently maintained by reconcile. Long-term configuration changes are made
through Profile revisions rather than direct Target edits.
_Avoid_: Latest Library versions, observed inventory

**Restored Desired State**:
An emergency Target-specific desired state created from a retained backup. It
may be reconciled until the Target is returned to Profile management.
_Avoid_: Profile Revision, Target edit

**Profile Adoption**:
Creating a new Profile from a verified Restored Desired State. Adoption copies
its exact available member revisions and never Applies them automatically.
_Avoid_: Target Edit, automatic Apply

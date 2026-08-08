# Generation 1 Configuration Import

`toolhub-config-migrate` is an offline administrator tool for importing the
reviewed configuration subset from an exact legacy migration ledger `1..11`
into a distinct, fresh generation-2 PostgreSQL database. It never calls the
Bridge, starts workers or the relay, contacts Salt, creates desired snapshots,
or applies a Profile.

The importer accepts current approved Skills, active ToolHub-owned MCP
definitions and their encrypted references, Claude/Codex desired membership,
and the global update schedule. Authentication state, Agent/SSH material,
jobs, deployments, activations, runtime observations, and audit history are
not imported.

## Build

```bash
cd /root/docker/toolhub
GOCACHE=/tmp/toolhub-gocache go build -o bin/toolhub-config-migrate ./cmd/toolhub-config-migrate
```

The binary is intentionally not included in the ToolHub application image.
Run it from the trusted host while both legacy and generation-2 application
writers are stopped.

## Dry Run

Credentials come only from environment variables or a root-owned mode-`0600`
legacy key file. Do not put database credentials or keys in command arguments.

```bash
export TOOLHUB_LEGACY_DATABASE_URL='postgres://...'
export TOOLHUB_LEGACY_MASTER_KEY_FILE='/root/toolhub-migration/legacy-master.key'
bin/toolhub-config-migrate --report-json /root/toolhub-migration/dry-run.json
```

Dry-run opens a repeatable-read, read-only legacy transaction; verifies the
exact schema, archive bytes, Secret references, and legacy key; then prints a
redacted deterministic report. It does not connect to or initialize the
destination. Review and record the reported source fingerprint.

## Apply

Apply requires the reviewed fingerprint and all normal destination bootstrap
settings. The destination must be a different empty database, or the pristine
generation-2 baseline left by a previous failed attempt.

```bash
export TOOLHUB_DATABASE_URL='postgres://...'
export TOOLHUB_MASTER_KEY='...'
export TOOLHUB_BOOTSTRAP_USERNAME='admin'
export TOOLHUB_BOOTSTRAP_PASSWORD='...'
export TOOLHUB_LOCAL_NODE_NAME='project-host'
export TOOLHUB_MANAGED_USERNAME='root'
export TOOLHUB_TIMEZONE='Asia/Shanghai'
export TOOLHUB_RELAY_PORT='6276'

bin/toolhub-config-migrate \
  --apply \
  --expect-source-fingerprint '<reviewed-lowercase-sha256>' \
  --report-json /root/toolhub-migration/apply.json
```

Apply initializes generation 2, creates only the singleton bootstrap account
and local baseline, then inserts all imported configuration, one actorless
audit event, and the completion marker in one advisory-locked transaction.
Legacy Secret values are decrypted into bounded buffers, re-encrypted under
fresh UUIDs with the destination key, and zeroed. A failed import transaction
leaves the initialized destination baseline retryable.

The generated Profiles are:

- `claude-skills`: Claude Skill selection;
- `codex-skills`: Codex Skill selection;
- `shared-mcp`: Claude and Codex MCP servers.

Review these Profiles in the generation-2 UI. Applying them is a separate,
explicit post-cutover operation requiring normal target scan and preflight.

Re-running the same frozen source against the same imported destination is a
verified no-op. A changed source fingerprint, non-pristine destination, legacy
destination, missing/extra legacy migration, unsupported configuration, or
unresolved Secret reference fails closed.

## Rollback

There is no reverse row migration. Keep the legacy image, database volume,
verified logical dump, and cold archive intact. Before generation-2 acceptance,
rollback means stopping the generation-2 stack and restarting the retained
legacy application and PostgreSQL stack together.

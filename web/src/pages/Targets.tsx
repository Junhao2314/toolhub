import {
  Activity,
  Bot,
  Download,
  KeyRound,
  Monitor,
  Play,
  RefreshCw,
  RotateCcw,
  Server,
  Square,
  UserRoundCog,
} from "lucide-react";
import { useEffect, useRef, useState } from "react";
import {
  api,
  type Backup,
  type LocalMCPImportPreflight,
  type LocalMCPServerPreview,
  type MCPServer,
  type Node,
  type Operation,
  type Skill,
  type Target,
  type TargetDetail,
} from "../api/client";
import {
  Button,
  Empty,
  ErrorNotice,
  Field,
  IconButton,
  Loading,
  Modal,
  PageHeader,
  Status,
} from "../components/ui";
import { useData } from "../hooks/useData";
import { useI18n } from "../i18n";

export default function Targets() {
  const { t } = useI18n();
  const state = useData(async () => {
    const [targets, skills, servers] = await Promise.all([
      api.list<Target>("/targets"),
      api.list<Skill>("/skills"),
      api.list<MCPServer>("/mcp/servers"),
    ]);
    return {
      targets: targets.items,
      skills: skills.items,
      servers: servers.items,
    };
  }, []);
  const [selected, setSelected] = useState("");
  const [notice, setNotice] = useState("");
  const [refreshing, setRefreshing] = useState(false);
  const [refreshOperationID, setRefreshOperationID] = useState("");
  const refreshInFlight = useRef(false);
  useEffect(() => {
    if (
      state.data?.targets.length &&
      !state.data.targets.some((target) => target.id === selected)
    )
      setSelected(state.data.targets[0].id);
  }, [state.data, selected]);
  const activeTargetID = state.data?.targets.some(
    (target) => target.id === selected,
  )
    ? selected
    : (state.data?.targets[0]?.id ?? "");
  const refreshNodes = () => {
    if (refreshInFlight.current) return;
    refreshInFlight.current = true;
    setRefreshing(true);
    api
      .post<Operation>("/nodes/refresh")
      .then((operation) => {
        setRefreshOperationID(operation.id);
        setNotice(t("Node refresh queued"));
      })
      .catch((reason: Error) => {
        refreshInFlight.current = false;
        setRefreshing(false);
        setRefreshOperationID("");
        setNotice(reason.message);
      });
  };
  useEffect(() => {
    if (!refreshOperationID) return;
    let active = true;
    let timeout = 0;
    const finish = () => {
      refreshInFlight.current = false;
      setRefreshing(false);
      setRefreshOperationID("");
    };
    const poll = () => {
      api
        .get<Operation>(`/operations/${refreshOperationID}`)
        .then((operation) => {
          if (!active) return;
          if (operation.status === "succeeded") {
            finish();
            setNotice(t("Node refresh completed"));
            state.reload();
            return;
          }
          if (["failed", "partial", "cancelled"].includes(operation.status)) {
            finish();
            setNotice(
              operation.errorReason ||
                t(
                  operation.status === "cancelled"
                    ? "Node refresh cancelled"
                    : "Node refresh failed",
                ),
            );
            return;
          }
          setNotice(t("Node refresh running"));
          timeout = window.setTimeout(poll, 500);
        })
        .catch((reason: Error) => {
          if (!active) return;
          finish();
          setNotice(reason.message);
        });
    };
    poll();
    return () => {
      active = false;
      window.clearTimeout(timeout);
    };
  }, [refreshOperationID, state.reload, t]);
  return (
    <>
      <PageHeader
        title={t("Targets")}
        detail={t("Runtime inventory and pinned desired snapshots")}
        actions={
          <Button variant="secondary" disabled={refreshing} onClick={refreshNodes}>
            <RefreshCw className={refreshing ? "spin" : ""} size={16} />
            {t("Refresh nodes")}
          </Button>
        }
      />
      {notice && <div className="inline-notice">{notice}</div>}
      {state.loading ? (
        <Loading />
      ) : state.error || !state.data ? (
        <ErrorNotice message={state.error} retry={state.reload} />
      ) : state.data.targets.length === 0 ? (
        <Empty title={t("No Targets")} />
      ) : (
        <div className="targets-layout">
          <aside className="target-index">
            <div className="target-index-header">
              <span>{t("Nodes & Targets")}</span>
              <span className="count-badge">{state.data.targets.length}</span>
            </div>
            {state.data.targets.map((target) => (
              <button
                className={activeTargetID === target.id ? "active" : ""}
                key={target.id}
                onClick={() => setSelected(target.id)}
              >
                <div className="target-item-info">
                  <div className="target-item-icon">
                    {target.runtime === "shared-relay" ? (
                      <Bot size={17} />
                    ) : target.nodeKind === "salt" ? (
                      <Server size={17} />
                    ) : (
                      <Monitor size={17} />
                    )}
                  </div>
                  <div className="target-item-text">
                    <strong>
                      {target.runtime === "shared-relay"
                        ? t("Shared MCP relay")
                        : target.runtime}
                    </strong>
                    <small>{target.nodeName}</small>
                  </div>
                </div>
                <Status value={target.health} />
              </button>
            ))}
          </aside>
          {activeTargetID && (
            <TargetInspector
              key={activeTargetID}
              targetID={activeTargetID}
              skills={state.data.skills}
              servers={state.data.servers}
              refreshTargets={state.reload}
              operation={(message) => setNotice(message)}
            />
          )}
        </div>
      )}
    </>
  );
}

function TargetInspector({
  targetID,
  skills,
  servers,
  refreshTargets,
  operation,
}: {
  targetID: string;
  skills: Skill[];
  servers: MCPServer[];
  refreshTargets: () => void;
  operation: (message: string) => void;
}) {
  const { t } = useI18n();
  const state = useData(async () => {
    const [detail, backups] = await Promise.all([
      api.get<TargetDetail>(`/targets/${targetID}`),
      api.list<Backup>(`/targets/${targetID}/backups`),
    ]);
    return { detail, backups: backups.items };
  }, [targetID]);
  const [restoring, setRestoring] = useState<Backup | null>(null);
  const [mcpImport, setMCPImport] = useState(false);
  const [adopting, setAdopting] = useState(false);
  const [usernameEditor, setUsernameEditor] = useState(false);
  const [relayQueueing, setRelayQueueing] = useState("");
  const queue = (request: Promise<Operation>, label: string) =>
    request
      .then((op) => operation(`${label} · ${op.id.slice(0, 8)}`))
      .catch((reason: Error) => operation(reason.message));
  if (state.loading) return <Loading />;
  if (state.error || !state.data)
    return <ErrorNotice message={state.error} retry={state.reload} />;
  const { detail, backups } = state.data;
  const target = detail.target;
  const members = detail.inventory.members ?? [];
  const managedSkills = new Set(
    detail.desired?.manifest.skills.map((item) => item.slug) ?? [],
  );
  const managedMCP = new Set(
    detail.desired?.manifest.mcpServers.map((item) => item.name) ?? [],
  );
  const isManaged = (member: (typeof members)[number]) =>
    member.kind === "anchor" ||
    (member.kind === "skill" && managedSkills.has(member.name)) ||
    (member.kind === "mcp" && managedMCP.has(member.name));
  const allowsLocalIntake =
    target.nodeKind === "local" && ["claude", "codex"].includes(target.runtime);
  const relay = detail.inventory.relay;
  const relayStatuses = target.relayMemberStatuses?.length
    ? target.relayMemberStatuses
    : (relay?.memberStatuses ?? []);
  const relayStatusByID = new Map(
    relayStatuses.map((item) => [item.memberId, item]),
  );
  const relayStatusByName = new Map(
    relayStatuses.map((item) => [item.name, item]),
  );
  const desiredRelayMembers = (detail.desired?.manifest.mcpServers ?? []).map(
    (member) =>
      relayStatusByID.get(member.memberId) ??
      relayStatusByName.get(member.name) ?? {
        memberId: member.memberId,
        name: member.name,
        status: "unavailable" as const,
        capabilityKinds: [],
        capabilities: {
          tools: 0,
          resources: 0,
          resourceTemplates: 0,
          prompts: 0,
        },
        checkedAt: "",
        errorReason: t("Not checked"),
      },
  );
  const queueRelay = (action: string, label: string) => {
    setRelayQueueing(action);
    queue(api.post(`/targets/${target.id}/relay/${action}`), label).finally(
      () => setRelayQueueing(""),
    );
  };
  return (
    <div className="target-inspector">
      <section className="target-hero">
        <div className="hero-main">
          <div className="hero-badge">
            {target.runtime === "shared-relay" ? (
              <Bot size={22} />
            ) : target.nodeKind === "salt" ? (
              <Server size={22} />
            ) : (
              <Monitor size={22} />
            )}
          </div>
          <div>
            <div className="hero-tags">
              <span className="runtime-label">
                {target.nodeKind} / {target.runtime}
              </span>
              <Status value={target.health} />
            </div>
            <h2>{target.targetKey}</h2>
            <p className="hero-sub">
              {t("Node")}: <strong>{target.nodeName}</strong> · {t("User")}:{" "}
              <code>{target.managedUsername}</code>
            </p>
          </div>
        </div>
        <div className="summary-actions">
          {target.nodeKind === "salt" && (
            <Button variant="secondary" onClick={() => setUsernameEditor(true)}>
              <UserRoundCog size={16} />
              {t("Edit node username")}
            </Button>
          )}
          <Button
            variant="secondary"
            onClick={() =>
              queue(api.post(`/targets/${target.id}/scan`), t("Scan queued"))
            }
          >
            <RefreshCw size={16} />
            {t("Scan")}
          </Button>
          {allowsLocalIntake && (
            <Button variant="secondary" onClick={() => setMCPImport(true)}>
              <KeyRound size={16} />
              {t("Import MCP from runtime")}
            </Button>
          )}
          {target.runtime === "hermes" && <Status value={t("Read-only")} />}
          {detail.desired?.snapshot.sourceKind === "restore" && (
            <Button variant="secondary" onClick={() => setAdopting(true)}>
              <UserRoundCog size={16} />
              {t("Adopt as Profile")}
            </Button>
          )}
        </div>
      </section>
      {target.errorReason && (
        <ErrorNotice message={`${target.errorCode}: ${target.errorReason}`} />
      )}
      {target.runtime === "shared-relay" && (
        <section className="relay-strip">
          <div>
            <Activity size={20} />
            <span>
              <strong>
                {relay?.endpoint ||
                  `http://127.0.0.1:${detail.desired?.manifest.relayPort ?? 6276}/mcp`}
              </strong>
              <small>
                {relay?.state || "unknown"} · {t("Port")}{" "}
                {relay?.fixedPort ?? detail.desired?.manifest.relayPort ?? 6276}{" "}
                · {t(relay?.systemdEnabled ? "Enabled" : "Disabled")}
              </small>
            </span>
            <Status
              value={
                relay?.intentionalPaused
                  ? "paused"
                  : relay?.healthy
                    ? "healthy"
                    : "blocked"
              }
            />
          </div>
          <div className="relay-actions">
            <IconButton
              label={t("Start")}
              disabled={relayQueueing !== ""}
              onClick={() => queueRelay("start", t("Relay start queued"))}
            >
              <Play size={16} />
            </IconButton>
            <IconButton
              label={t("Stop")}
              disabled={relayQueueing !== ""}
              onClick={() => queueRelay("stop", t("Relay stop queued"))}
            >
              <Square size={16} />
            </IconButton>
            <IconButton
              label={t("Restart")}
              disabled={relayQueueing !== ""}
              onClick={() => queueRelay("restart", t("Relay restart queued"))}
            >
              <RefreshCw
                className={relayQueueing === "restart" ? "spin" : ""}
                size={16}
              />
            </IconButton>
            <IconButton
              label={t("Health check")}
              disabled={relayQueueing !== ""}
              onClick={() => queueRelay("health", t("Health check queued"))}
            >
              <Activity size={16} />
            </IconButton>
          </div>
        </section>
      )}
      {target.runtime === "shared-relay" && (
        <dl className="relay-facts">
          <div>
            <dt>{t("Retry count")}</dt>
            <dd>{target.relayFailureCount}</dd>
          </div>
          <div>
            <dt>{t("Next retry")}</dt>
            <dd>
              {target.relayNextRetryAt
                ? new Date(target.relayNextRetryAt).toLocaleString()
                : "—"}
            </dd>
          </div>
          <div>
            <dt>{t("Retry state")}</dt>
            <dd>
              <Status value={target.relaySuspended ? "suspended" : "active"} />
            </dd>
          </div>
          <div>
            <dt>{t("Last full check")}</dt>
            <dd>
              {target.relayLastMemberCheckAt
                ? new Date(target.relayLastMemberCheckAt).toLocaleString()
                : "—"}
            </dd>
          </div>
          <div>
            <dt>{t("Runtime contract")}</dt>
            <dd>{relay?.contract ? t(relay.contract) : "—"}</dd>
          </div>
        </dl>
      )}
      <dl className="target-facts">
        <div className="fact-card">
          <dt>{t("Desired revision")}</dt>
          <dd>
            <code>{target.desiredRevision || "—"}</code>
          </dd>
        </div>
        <div className="fact-card">
          <dt>{t("Snapshot source")}</dt>
          <dd>{detail.desired?.snapshot.sourceKind || "—"}</dd>
        </div>
        <div className="fact-card">
          <dt>{t("Last scan")}</dt>
          <dd>
            {target.lastScannedAt
              ? new Date(target.lastScannedAt).toLocaleString()
              : "—"}
          </dd>
        </div>
        <div className="fact-card">
          <dt>{t("Last reconcile")}</dt>
          <dd>
            {target.lastReconciledAt
              ? new Date(target.lastReconciledAt).toLocaleString()
              : "—"}
          </dd>
        </div>
      </dl>
      {target.runtime === "shared-relay" && (
        <section className="target-band relay-members">
          <header>
            <h3>{t("Desired MCP health")}</h3>
            <span>{desiredRelayMembers.length}</span>
          </header>
          {desiredRelayMembers.length === 0 ? (
            <Empty title={t("No desired MCP members")} />
          ) : (
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>{t("Member")}</th>
                    <th>{t("State")}</th>
                    <th>{t("Capabilities")}</th>
                    <th>{t("Checked")}</th>
                    <th>{t("Reason")}</th>
                  </tr>
                </thead>
                <tbody>
                  {desiredRelayMembers.map((member) => {
                    const counts = member.capabilities;
                    const capabilities = [
                      [t("Tools"), counts.tools],
                      [t("Resources"), counts.resources],
                      [t("Templates"), counts.resourceTemplates],
                      [t("Prompts"), counts.prompts],
                    ].filter(([, count]) => Number(count) > 0);
                    return (
                      <tr key={member.memberId}>
                        <td>
                          <strong>{member.name}</strong>
                        </td>
                        <td>
                          <Status value={member.status} />
                        </td>
                        <td>
                          <span className="relay-capabilities">
                            {capabilities.length
                              ? capabilities.map(([kind, count]) => (
                                  <span key={String(kind)}>
                                    {kind} {count}
                                  </span>
                                ))
                              : "—"}
                          </span>
                        </td>
                        <td>
                          {member.checkedAt
                            ? new Date(member.checkedAt).toLocaleString()
                            : "—"}
                        </td>
                        <td>
                          <span className="relay-reason">
                            {member.errorCode && (
                              <code>{member.errorCode}</code>
                            )}
                            {member.errorReason || "—"}
                          </span>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </section>
      )}
      {target.runtime === "hermes" ? (
        <HermesInventory
          target={target}
          members={members}
          revision={detail.targetRevision}
          skills={skills}
          operation={operation}
        />
      ) : (
        <section className="target-band">
          <header>
            <h3>{t("Inventory")}</h3>
            <span>{members.length}</span>
          </header>
          {members.length === 0 ? (
            <Empty title={t("No inventory snapshot")} />
          ) : (
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>{t("Member")}</th>
                    <th>{t("Kind")}</th>
                    <th>{t("Scope")}</th>
                    <th>{t("Hash")}</th>
                    <th>{t("State")}</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {members.map((member) => {
                    const managed = isManaged(member);
                    const importable =
                      allowsLocalIntake &&
                      member.kind === "skill" &&
                      !member.protected &&
                      !managed &&
                      !!member.contentHash &&
                      !!detail.targetRevision;
                    return (
                      <tr key={member.id}>
                        <td>
                          <strong>{member.name}</strong>
                        </td>
                        <td>
                          <Status value={member.kind} />
                        </td>
                        <td>{member.scope || "—"}</td>
                        <td>
                          <code>{member.contentHash?.slice(0, 12) || "—"}</code>
                        </td>
                        <td>
                          <Status
                            value={
                              member.protected
                                ? "protected"
                                : managed
                                  ? "managed"
                                  : "unmanaged"
                            }
                          />
                        </td>
                        <td>
                          <div className="row-actions">
                            {importable && (
                              <Button
                                aria-label={`${t("Import Skill")} · ${member.name}`}
                                variant="secondary"
                                onClick={() =>
                                  queue(
                                    api.post(
                                      `/targets/${target.id}/skill-import`,
                                      {
                                        name: member.name,
                                        expectedRevision: detail.targetRevision,
                                        contentHash: member.contentHash,
                                      },
                                    ),
                                    t("Skill import queued"),
                                  )
                                }
                              >
                                <Download size={15} />
                                {t("Import Skill")}
                              </Button>
                            )}
                          </div>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </section>
      )}
      <section className="target-band">
        <header>
          <h3>{t("Backups")}</h3>
          <span>{backups.length}</span>
        </header>
        {backups.length === 0 ? (
          <Empty title={t("No backups")} />
        ) : (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>{t("Created")}</th>
                  <th>{t("Revision")}</th>
                  <th>{t("Expires")}</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {backups.map((backup) => (
                  <tr key={backup.id}>
                    <td>{new Date(backup.createdAt).toLocaleString()}</td>
                    <td>
                      <code>{backup.targetRevision.slice(0, 12)}</code>
                    </td>
                    <td>{new Date(backup.expiresAt).toLocaleDateString()}</td>
                    <td>
                      <div className="row-actions">
                        <Button
                          variant="secondary"
                          onClick={() => setRestoring(backup)}
                        >
                          <RotateCcw size={15} />
                          {t("Restore")}
                        </Button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
      {restoring && (
        <RestoreDialog
          target={target}
          revision={detail.targetRevision}
          backup={restoring}
          close={() => setRestoring(null)}
          queued={(op) => {
            setRestoring(null);
            operation(`${t("Restore queued")} · ${op.id.slice(0, 8)}`);
          }}
        />
      )}
      {mcpImport && (
        <LocalMCPImportDialog
          target={target}
          close={() => setMCPImport(false)}
          queued={(op) => {
            setMCPImport(false);
            operation(`${t("MCP import queued")} · ${op.id.slice(0, 8)}`);
          }}
        />
      )}
      {usernameEditor && (
        <NodeUsernameDialog
          target={target}
          close={() => setUsernameEditor(false)}
          saved={(configured) => {
            setUsernameEditor(false);
            state.reload();
            refreshTargets();
            operation(
              t(
                configured
                  ? "Node username override saved"
                  : "Node username override cleared",
              ),
            );
          }}
        />
      )}
      {adopting && (
        <AdoptProfileDialog
          target={target}
          close={() => setAdopting(false)}
          saved={(profile) => {
            setAdopting(false);
            state.reload();
            refreshTargets();
            operation(`${t("Profile adopted")} · ${profile.name}`);
          }}
        />
      )}
    </div>
  );
}

function AdoptProfileDialog({
  target,
  close,
  saved,
}: {
  target: Target;
  close: () => void;
  saved: (profile: { name: string }) => void;
}) {
  const { t } = useI18n();
  const [name, setName] = useState(`${target.targetKey} restored`);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    setError("");
    api
      .post<{ name: string }>(`/targets/${target.id}/profile-adoption`, {
        name: name.trim(),
      })
      .then(saved)
      .catch((reason: Error) => {
        setError(reason.message);
        setBusy(false);
      });
  };
  return (
    <Modal title={`${t("Adopt as Profile")} · ${target.targetKey}`} close={close}>
      <form className="modal-form" onSubmit={submit}>
        {error && <ErrorNotice message={error} />}
        <Field label={t("Profile name")}>
          <input value={name} maxLength={120} onChange={(event) => setName(event.target.value)} autoFocus />
        </Field>
        <div className="modal-actions">
          <Button type="button" variant="secondary" onClick={close}>{t("Cancel")}</Button>
          <Button type="submit" disabled={busy || !name.trim()}>{t("Create Profile")}</Button>
        </div>
      </form>
    </Modal>
  );
}

function NodeUsernameDialog({
  target,
  close,
  saved,
}: {
  target: Target;
  close: () => void;
  saved: (configured: boolean) => void;
}) {
  const { t } = useI18n();
  const state = useData(async () => {
    const response = await api.list<Node>("/nodes");
    const node = response.items.find(
      (item) => item.id === target.nodeId && item.kind === "salt",
    );
    if (!node) throw new Error(t("Salt node is no longer available"));
    return node;
  }, [target.nodeId]);
  return (
    <Modal
      title={`${t("Managed username")} · ${target.nodeName}`}
      close={close}
    >
      {state.loading ? (
        <Loading />
      ) : state.error || !state.data ? (
        <ErrorNotice message={state.error} retry={state.reload} />
      ) : (
        <NodeUsernameForm
          node={state.data}
          effectiveUsername={target.managedUsername}
          close={close}
          saved={saved}
        />
      )}
    </Modal>
  );
}

function NodeUsernameForm({
  node,
  effectiveUsername,
  close,
  saved,
}: {
  node: Node;
  effectiveUsername: string;
  close: () => void;
  saved: (configured: boolean) => void;
}) {
  const { t } = useI18n();
  const initial = node.managedUsernameOverride ?? "";
  const [username, setUsername] = useState(initial);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    const managedUsername = username.trim();
    setSubmitting(true);
    setError("");
    api
      .patch<void>(`/nodes/${node.id}`, { managedUsername })
      .then(() => saved(managedUsername !== ""))
      .catch((reason: Error) => {
        setError(reason.message);
        setSubmitting(false);
      });
  };
  return (
    <form className="modal-form" onSubmit={submit}>
      {error && <ErrorNotice message={error} />}
      <Field label={t("Node username override")}>
        <input
          value={username}
          placeholder={effectiveUsername}
          maxLength={32}
          pattern="[a-z_][a-z0-9_-]{0,31}"
          autoCapitalize="none"
          spellCheck={false}
          onChange={(event) => setUsername(event.target.value)}
        />
      </Field>
      <div className="modal-actions">
        <Button type="button" variant="secondary" onClick={close}>
          {t("Cancel")}
        </Button>
        <Button
          type="submit"
          disabled={submitting || username.trim() === initial}
        >
          {t("Save")}
        </Button>
      </div>
    </form>
  );
}

function HermesInventory({
  target,
  members,
  revision,
  skills,
  operation,
}: {
  target: Target;
  members: TargetDetail["inventory"]["members"];
  revision: string;
  skills: Skill[];
  operation: (message: string) => void;
}) {
  const { t } = useI18n();
  const inventory = members ?? [];
  const [selected, setSelected] = useState(new Set<string>());
  const [importableOnly, setImportableOnly] = useState(true);
  const [includeBuiltin, setIncludeBuiltin] = useState(false);
  const [includeShadowed, setIncludeShadowed] = useState(false);
  const [source, setSource] = useState("all");
  const [provider, setProvider] = useState("all");
  const [category, setCategory] = useState("all");
  const [query, setQuery] = useState("");
  const [busy, setBusy] = useState(false);
  const providers = [
    ...new Set(inventory.map((item) => item.provider).filter(Boolean)),
  ].sort();
  const categories = [
    ...new Set(inventory.map((item) => item.category).filter(Boolean)),
  ].sort();
  const filtered = inventory.filter((item) => {
    if (item.kind !== "skill") return false;
    if (importableOnly && !item.importable) return false;
    if (!includeBuiltin && item.builtin) return false;
    if (!includeShadowed && item.shadowed) return false;
    if (source !== "all" && item.source !== source) return false;
    if (provider !== "all" && item.provider !== provider) return false;
    if (category !== "all" && item.category !== category) return false;
    const text =
      `${item.name} ${item.slug ?? ""} ${item.description ?? ""}`.toLowerCase();
    return !query.trim() || text.includes(query.trim().toLowerCase());
  });
  const toggle = (id: string) => {
    const next = new Set(selected);
    next.has(id) ? next.delete(id) : next.add(id);
    setSelected(next);
  };
  const selectAll = () =>
    setSelected(new Set(filtered.slice(0, 50).map((item) => item.id)));
  const libraryBySlug = new Map(
    skills.map((skill) => [skill.slug, skill.currentContentHash]),
  );
  const needsConfirmation = [...selected].some((id) => {
    const item = inventory.find((candidate) => candidate.id === id);
    return (
      !!item &&
      !!item.slug &&
      libraryBySlug.has(item.slug) &&
      libraryBySlug.get(item.slug) !== item.contentHash
    );
  });
  const submit = () => {
    const items = [...selected]
      .map((id) => inventory.find((item) => item.id === id))
      .filter((item): item is NonNullable<typeof item> => !!item)
      .map((item) => ({
        id: item.id,
        slug: item.slug ?? item.name.toLowerCase(),
        contentHash: item.contentHash ?? "",
      }));
    if (
      items.length === 0 ||
      items.length > 50 ||
      (needsConfirmation &&
        !confirm(t("Confirm update of different Skill content?")))
    )
      return;
    setBusy(true);
    api
      .post<Operation>(`/targets/${target.id}/skill-imports`, {
        targetRevision: revision,
        items,
        confirmUpdates: needsConfirmation,
      })
      .then((op) => {
        operation(`${t("Hermes Skill batch queued")} · ${op.id.slice(0, 8)}`);
        setSelected(new Set());
      })
      .catch((reason: Error) => operation(reason.message))
      .finally(() => setBusy(false));
  };
  return (
    <section className="target-band">
      <header>
        <div>
          <h3>{t("Hermes Skills")}</h3>
          <small>
            {t("Importable only")} · {filtered.length} / {inventory.length}
          </small>
        </div>
        <span>{selected.size}/50</span>
      </header>
      <div className="filter-bar">
        <input
          aria-label={t("Search")}
          placeholder={t("Search name, slug, description")}
          value={query}
          onChange={(event) => setQuery(event.target.value)}
        />
        <select
          value={source}
          onChange={(event) => setSource(event.target.value)}
        >
          <option value="all">{t("Hub + Local")}</option>
          <option value="hub">Hub</option>
          <option value="local">Local</option>
        </select>
        <select
          value={provider}
          onChange={(event) => setProvider(event.target.value)}
        >
          <option value="all">{t("All providers")}</option>
          {providers.map((item) => (
            <option key={item} value={item}>
              {item}
            </option>
          ))}
        </select>
        <select
          value={category}
          onChange={(event) => setCategory(event.target.value)}
        >
          <option value="all">{t("All categories")}</option>
          {categories.map((item) => (
            <option key={item} value={item}>
              {item}
            </option>
          ))}
        </select>
        <label>
          <input
            type="checkbox"
            checked={importableOnly}
            onChange={(event) => setImportableOnly(event.target.checked)}
          />
          {t("Importable only")}
        </label>
        <label>
          <input
            type="checkbox"
            checked={includeBuiltin}
            onChange={(event) => setIncludeBuiltin(event.target.checked)}
          />
          {t("Builtin")}
        </label>
        <label>
          <input
            type="checkbox"
            checked={includeShadowed}
            onChange={(event) => setIncludeShadowed(event.target.checked)}
          />
          {t("Shadowed")}
        </label>
      </div>
      <div className="row-actions">
        <Button
          variant="secondary"
          onClick={selectAll}
          disabled={filtered.length === 0}
        >
          <Download size={15} />
          {t("Select all filtered")}
        </Button>
        <Button onClick={submit} disabled={busy || selected.size === 0}>
          <Download size={15} />
          {t("Import selected")}
        </Button>
      </div>
      {filtered.length === 0 ? (
        <Empty title={t("No importable Hermes Skills")} />
      ) : (
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th />
                <th>{t("Skill")}</th>
                <th>{t("Source")}</th>
                <th>{t("Provider")}</th>
                <th>{t("Category")}</th>
                <th>{t("Version")}</th>
                <th>{t("Status")}</th>
              </tr>
            </thead>
            <tbody>
              {filtered.map((item) => (
                <tr key={item.id}>
                  <td>
                    <input
                      type="checkbox"
                      checked={selected.has(item.id)}
                      disabled={!selected.has(item.id) && selected.size >= 50}
                      onChange={() => toggle(item.id)}
                    />
                  </td>
                  <td>
                    <strong>{item.name}</strong>
                    <small>{item.slug}</small>
                  </td>
                  <td>{item.source || "local"}</td>
                  <td>{item.provider || "—"}</td>
                  <td>{item.category || "—"}</td>
                  <td>
                    <code>{item.contentHash?.slice(0, 12) || "—"}</code>
                  </td>
                  <td>
                    <Status
                      value={
                        item.shadowed
                          ? "shadowed"
                          : item.builtin
                            ? "builtin"
                            : item.importable
                              ? "ready"
                              : item.eligibilityReason || "blocked"
                      }
                    />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function LocalMCPImportDialog({
  target,
  close,
  queued,
}: {
  target: Target;
  close: () => void;
  queued: (operation: Operation) => void;
}) {
  const { t } = useI18n();
  const state = useData(
    () =>
      api.post<LocalMCPImportPreflight>(
        `/targets/${target.id}/mcp-import/preflight`,
      ),
    [target.id],
  );
  const [selected, setSelected] = useState<LocalMCPServerPreview | null>(null);
  const [confirmed, setConfirmed] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const submit = () => {
    if (!selected || !confirmed) return;
    setSubmitting(true);
    setError("");
    api
      .post<Operation>("/mcp/import", {
        confirmationToken: selected.confirmationToken,
      })
      .then(queued)
      .catch((reason: Error) => {
        setError(reason.message);
        setSubmitting(false);
      });
  };
  return (
    <Modal
      title={`${t("Import MCP from runtime")} · ${target.targetKey}`}
      close={close}
    >
      {state.loading ? (
        <Loading />
      ) : state.error || !state.data ? (
        <ErrorNotice message={state.error} retry={state.reload} />
      ) : state.data.items.length === 0 ? (
        <Empty title={t("No importable MCP servers")} />
      ) : (
        <div className="mcp-import-list">
          {state.data.items.map((item) => (
            <label key={`${item.name}:${item.contentHash}`}>
              <input
                type="radio"
                name="local-mcp-import"
                aria-label={`${t("Select")} ${item.name}`}
                checked={selected?.confirmationToken === item.confirmationToken}
                onChange={() => setSelected(item)}
              />
              <span>
                <strong>{item.name}</strong>
                <small>
                  {item.transport} ·{" "}
                  <code>
                    {item.command
                      ? [item.command, ...item.args].join(" ")
                      : item.url || "—"}
                  </code>
                </small>
                <small>
                  {t("Environment secrets")}: {item.envKeys.join(", ") || "—"}
                </small>
                <small>
                  {t("Header secrets")}: {item.headerKeys.join(", ") || "—"}
                </small>
              </span>
            </label>
          ))}
        </div>
      )}
      {error && <ErrorNotice message={error} />}
      {state.data && state.data.items.length > 0 && (
        <label className="capture-confirmation">
          <input
            type="checkbox"
            checked={confirmed}
            onChange={(event) => setConfirmed(event.target.checked)}
          />
          <span>{t("Read and encrypt the selected secret values once")}</span>
        </label>
      )}
      <div className="modal-actions">
        <Button variant="secondary" onClick={close}>
          {t("Cancel")}
        </Button>
        <Button
          disabled={!selected || !confirmed || submitting}
          onClick={submit}
        >
          <Download size={16} />
          {submitting ? t("Importing") : t("Import selected")}
        </Button>
      </div>
    </Modal>
  );
}

function RestoreDialog({
  target,
  revision,
  backup,
  close,
  queued,
}: {
  target: Target;
  revision: string;
  backup: Backup;
  close: () => void;
  queued: (operation: Operation) => void;
}) {
  const { t } = useI18n();
  const [error, setError] = useState("");
  const submit = () =>
    api
      .post<Operation>(`/targets/${target.id}/restore`, {
        backupId: backup.id,
        expectedRevision: revision,
      })
      .then(queued)
      .catch((reason: Error) => setError(reason.message));
  return (
    <Modal title={`${t("Restore")} · ${target.targetKey}`} close={close}>
      {error && <ErrorNotice message={error} />}
      <dl className="detail-list">
        <div>
          <dt>{t("Backup")}</dt>
          <dd>
            <code>{backup.id}</code>
          </dd>
        </div>
        <div>
          <dt>{t("Created")}</dt>
          <dd>{new Date(backup.createdAt).toLocaleString()}</dd>
        </div>
        <div>
          <dt>{t("Target revision")}</dt>
          <dd>
            <code>{backup.targetRevision}</code>
          </dd>
        </div>
      </dl>
      <div className="modal-actions">
        <Button variant="secondary" onClick={close}>
          {t("Cancel")}
        </Button>
        <Button variant="danger" onClick={submit}>
          <RotateCcw size={16} />
          {t("Restore")}
        </Button>
      </div>
    </Modal>
  );
}

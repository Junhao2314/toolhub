import { ChevronDown, ChevronUp, RefreshCw, Save } from "lucide-react";
import { useMemo, useState } from "react";
import {
  api,
  type ContractGovernanceProjection,
  type ContractState,
  type MCPServer,
  type MCPVisibilityMode,
  type ObservedContractTool,
  type Profile,
  type ProfileClientKind,
  type ProfileMCPGovernance,
  type ProfileToolRule,
  type Skill,
  type ToolDecision,
} from "../../api/client";
import {
  Button,
  Empty,
  ErrorNotice,
  Field,
  Modal,
  Status,
} from "../../components/ui";
import { useI18n } from "../../i18n";

type EditorTab = "basic" | "skills" | "mcp" | "rules" | "status";

const tabs: Array<{ id: EditorTab; label: string }> = [
  { id: "basic", label: "Basic information" },
  { id: "skills", label: "Skills" },
  { id: "mcp", label: "MCP tools" },
  { id: "rules", label: "Tool rules" },
  { id: "status", label: "Application status" },
];

const decisions: ToolDecision[] = ["allow", "confirm", "deny"];
const decisionRank: Record<ToolDecision, number> = {
  allow: 0,
  confirm: 1,
  deny: 2,
};

function toggleSet(source: Set<string>, id: string) {
  const next = new Set(source);
  next.has(id) ? next.delete(id) : next.add(id);
  return next;
}

function acceptedTools(contract?: ContractState) {
  const latestByID = new Map(
    (contract?.latest?.tools ?? []).map((tool) => [tool.id, tool]),
  );
  return (contract?.accepted?.tools ?? [])
    .map((tool) => {
      if (!contract?.latest) return tool;
      const latest = latestByID.get(tool.id);
      return {
        ...tool,
        status: latest?.status ?? "paused_incompatible",
      };
    })
    .sort(
    (left, right) => left.position - right.position,
  );
}

function defaultVisible(mode: MCPVisibilityMode) {
  return mode === "all_accepted";
}

function reasonLabel(reason: string) {
  if (reason === "explicit_override") return "Global policy";
  if (reason === "reviewed_read_only" || reason === "annotation_read_only")
    return "Reviewed read-only";
  if (reason === "profile-rule") return "Profile rule";
  if (reason === "unclassified" || reason === "unclassified_mutating")
    return "Unclassified tool";
  return "Tool rules";
}

function contractToolStatus(
  status: ObservedContractTool["status"],
  confirmedRename: boolean,
) {
  if (status === "new_hidden" && !confirmedRename)
    return "New tools are not visible yet";
  if (status === "paused_incompatible") return "Tool definitions changed";
  if (status === "changed_presentation") return "Presentation changed";
  return "Accepted tool";
}

function contractStatus(contract: ContractState | undefined) {
  if (!contract?.accepted) return "unavailable";
  if (contract.reviewState === "paused") return "paused";
  if (contract.reviewState === "changed") return "changed";
  return "accepted";
}

export default function ProfileGovernanceEditor({
  profile,
  skills,
  servers,
  contracts,
  close,
  saved,
}: {
  profile?: Profile;
  skills: Skill[];
  servers: MCPServer[];
  contracts: ContractGovernanceProjection;
  close: () => void;
  saved: (profile: Profile) => void;
}) {
  const { t } = useI18n();
  const [tab, setTab] = useState<EditorTab>("basic");
  const [name, setName] = useState(profile?.name ?? "");
  const [description, setDescription] = useState(profile?.description ?? "");
  const [clientKind, setClientKind] = useState<ProfileClientKind>(
    profile?.clientKind ?? "claude",
  );
  const [category, setCategory] = useState(profile?.category ?? "coding");
  const [variant, setVariant] = useState(profile?.variant ?? "default");
  const [skillIds, setSkillIds] = useState(
    new Set(profile?.skillIds ?? []),
  );
  const [serverIds, setServerIds] = useState(
    new Set(profile?.mcpServerIds ?? []),
  );
  const [governance, setGovernance] = useState<
    Record<string, ProfileMCPGovernance>
  >(() =>
    Object.fromEntries(
      (profile?.mcpGovernance ?? []).map((item) => [item.serverId, item]),
    ),
  );
  const [toolRules, setToolRules] = useState<Record<string, ProfileToolRule>>(
    () =>
      Object.fromEntries(
        (profile?.toolRules ?? []).map((item) => [item.toolId, item]),
      ),
  );
  const [expandedServer, setExpandedServer] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const contractByServer = useMemo(
    () =>
      Object.fromEntries(contracts.items.map((item) => [item.serverId, item])),
    [contracts.items],
  );
  const confirmedRenameKeys = useMemo(
    () =>
      new Set(
        contracts.renames
          .filter((rename) => rename.status === "confirmed")
          .map(
            (rename) =>
              `${rename.serverId}:${rename.addedContractRevisionId}:${rename.addedToolId}`,
          ),
      ),
    [contracts.renames],
  );
  const selectedServers = servers.filter((server) => serverIds.has(server.id));

  const governanceFor = (server: MCPServer): ProfileMCPGovernance => {
    const current = governance[server.id];
    const profilePin = profile?.mcpServers?.find(
      (item) => item.serverId === server.id,
    );
    const existingProfileMember = profile?.mcpServerIds.includes(server.id);
    return {
      serverId: server.id,
      mcpRevisionId:
        current?.mcpRevisionId ??
        profilePin?.revisionId ??
        server.currentRevisionId,
      acceptedContractRevisionId:
        current?.acceptedContractRevisionId ??
        (current || existingProfileMember
          ? undefined
          : contractByServer[server.id]?.accepted?.revision.id),
      visibilityMode: current?.visibilityMode ?? "all_accepted",
    };
  };

  const contractTransitionPending = (server: MCPServer) => {
    const acceptedRevisionID =
      contractByServer[server.id]?.accepted?.revision.id;
    const pinnedRevisionID = governanceFor(server).acceptedContractRevisionId;
    return Boolean(
      acceptedRevisionID && pinnedRevisionID !== acceptedRevisionID,
    );
  };

  const confirmedRenameFor = (
    server: MCPServer,
    tool: ObservedContractTool,
  ) => {
    const acceptedRevisionID = governanceFor(server).acceptedContractRevisionId;
    return Boolean(
      acceptedRevisionID &&
        confirmedRenameKeys.has(
          `${server.id}:${acceptedRevisionID}:${tool.id}`,
        ),
    );
  };

  const visibleFor = (server: MCPServer, tool: ObservedContractTool) => {
    if (tool.status === "paused_incompatible") return false;
    const rule = toolRules[tool.id];
    if (rule) return rule.visible;
    if (tool.status === "new_hidden" && !confirmedRenameFor(server, tool))
      return false;
    return defaultVisible(governanceFor(server).visibilityMode);
  };

  const decisionFor = (tool: ObservedContractTool) => {
    const selected = toolRules[tool.id]?.decision ?? tool.globalDecision;
    return decisionRank[selected] >= decisionRank[tool.globalDecision]
      ? selected
      : tool.globalDecision;
  };

  const effectiveDecisionFor = decisionFor;

  const effectiveVisibleCount = selectedServers.reduce((count, server) => {
    return (
      count +
      acceptedTools(contractByServer[server.id]).filter(
        (tool) =>
          visibleFor(server, tool) && effectiveDecisionFor(tool) !== "deny",
      ).length
    );
  }, 0);

  const selectServer = (server: MCPServer) => {
    const selecting = !serverIds.has(server.id);
    setServerIds(toggleSet(serverIds, server.id));
    if (selecting) {
      setGovernance((current) => ({
        ...current,
        [server.id]: governanceFor(server),
      }));
    }
  };

  const setVisibilityMode = (
    server: MCPServer,
    visibilityMode: MCPVisibilityMode,
  ) => {
    setGovernance((current) => ({
      ...current,
      [server.id]: { ...governanceFor(server), visibilityMode },
    }));
    const toolIDs = new Set(
      acceptedTools(contractByServer[server.id]).map((tool) => tool.id),
    );
    setToolRules((current) =>
      Object.fromEntries(
        Object.entries(current).map(([toolID, rule]) => [
          toolID,
          toolIDs.has(toolID)
            ? { ...rule, visible: defaultVisible(visibilityMode) }
            : rule,
        ]),
      ),
    );
  };

  const adoptAcceptedContract = (server: MCPServer) => {
    const acceptedContractRevisionId =
      contractByServer[server.id]?.accepted?.revision.id;
    if (!acceptedContractRevisionId) return;
    setGovernance((current) => ({
      ...current,
      [server.id]: {
        ...governanceFor(server),
        acceptedContractRevisionId,
      },
    }));
  };

  const setToolVisible = (
    server: MCPServer,
    tool: ObservedContractTool,
    visible: boolean,
  ) => {
    const decision = decisionFor(tool);
    setToolRules((current) => ({
      ...current,
      [tool.id]: {
        toolId: tool.id,
        visible,
        decision,
        reasonCodes:
          decision === tool.globalDecision ? [] : ["profile-rule"],
      },
    }));
  };

  const setToolDecision = (
    server: MCPServer,
    tool: ObservedContractTool,
    decision: ToolDecision,
  ) => {
    setToolRules((current) => ({
      ...current,
      [tool.id]: {
        toolId: tool.id,
        visible: visibleFor(server, tool),
        decision,
        reasonCodes:
          decision === tool.globalDecision ? [] : ["profile-rule"],
      },
    }));
  };

  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    if (selectedServers.some(contractTransitionPending)) return;
    const selectedTools = new Set(
      selectedServers.flatMap((server) =>
        acceptedTools(contractByServer[server.id]).map((tool) => tool.id),
      ),
    );
    const payload = {
      name: name.trim(),
      description: description.trim(),
      clientKind,
      category: category.trim(),
      variant: variant.trim(),
      migrationState: profile?.migrationState ?? "ready",
      revision: profile?.revision ?? 0,
      skillIds: skills
        .filter((skill) => skillIds.has(skill.id))
        .map((skill) => skill.id),
      mcpServerIds: selectedServers.map((server) => server.id),
      skillVersionIds: Object.fromEntries(
        skills
          .filter((skill) => skillIds.has(skill.id))
          .map((skill) => [
            skill.id,
            profile?.skills?.find((pin) => pin.skillId === skill.id)
              ?.versionId ?? skill.currentVersionId,
          ]),
      ),
      mcpRevisionIds: Object.fromEntries(
        selectedServers.map((server) => [
          server.id,
          governanceFor(server).mcpRevisionId,
        ]),
      ),
      mcpGovernance: selectedServers.map(governanceFor),
      toolRules: Object.values(toolRules)
        .filter((rule) => selectedTools.has(rule.toolId))
        .map((rule) => {
          const tool = selectedServers
            .flatMap((server) => acceptedTools(contractByServer[server.id]))
            .find((item) => item.id === rule.toolId);
          if (!tool) return rule;
          const decision =
            decisionRank[rule.decision] >= decisionRank[tool.globalDecision]
              ? rule.decision
              : tool.globalDecision;
          return {
            ...rule,
            decision,
            reasonCodes:
              decision === tool.globalDecision ? [] : ["profile-rule"],
          };
        }),
    };
    setBusy(true);
    setError("");
    const request = profile
      ? api.put<Profile>(`/profiles/${profile.id}`, payload)
      : api.post<Profile>("/profiles", payload);
    request
      .then(saved)
      .catch((reason: Error) => setError(reason.message))
      .finally(() => setBusy(false));
  };

  const pendingContractTransitions = selectedServers.filter(
    contractTransitionPending,
  );

  return (
    <Modal
      title={profile ? `${t("Edit")} · ${profile.name}` : t("New Profile")}
      close={close}
    >
      <form className="profile-governance-editor" onSubmit={submit}>
        {error && <ErrorNotice message={error} />}
        <div className="profile-editor-tabs" role="tablist">
          {tabs.map((item) => (
            <button
              key={item.id}
              type="button"
              role="tab"
              aria-selected={tab === item.id}
              className={tab === item.id ? "active" : ""}
              onClick={() => setTab(item.id)}
            >
              {t(item.label)}
            </button>
          ))}
        </div>

        {tab === "basic" && (
          <section className="profile-editor-section" role="tabpanel">
            <div className="profile-form-grid">
              <Field label={t("Name")}>
                <input
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                />
              </Field>
              <Field label={t("Client")}>
                <select
                  value={clientKind}
                  onChange={(event) =>
                    setClientKind(event.target.value as ProfileClientKind)
                  }
                >
                  <option value="claude">Claude</option>
                  <option value="codex">Codex</option>
                  <option value="shared">{t("Shared")}</option>
                  <option value="unknown">{t("Unknown")}</option>
                </select>
              </Field>
              <Field label={t("Category")}>
                <input
                  value={category}
                  onChange={(event) => setCategory(event.target.value)}
                />
              </Field>
              <Field label={t("Variant")}>
                <input
                  value={variant}
                  onChange={(event) => setVariant(event.target.value)}
                />
              </Field>
              <Field label={t("Description")}>
                <input
                  value={description}
                  onChange={(event) => setDescription(event.target.value)}
                />
              </Field>
            </div>
          </section>
        )}

        {tab === "skills" && (
          <section className="profile-editor-section" role="tabpanel">
            <SelectionList
              empty={t("No Skills in Library")}
              items={skills.map((skill) => ({
                id: skill.id,
                name: skill.name,
                detail: (() => {
                  const pin = profile?.skills?.find(
                    (item) => item.skillId === skill.id,
                  );
                  return `${skill.slug} · ${(pin?.versionId ?? skill.currentVersionId).slice(0, 12)}${pin && !pin.current ? ` · ${t("Pinned")}` : ""}`;
                })(),
              }))}
              selected={skillIds}
              toggle={(id) => setSkillIds(toggleSet(skillIds, id))}
            />
          </section>
        )}

        {tab === "mcp" && (
          <section className="profile-editor-section" role="tabpanel">
            {servers.length === 0 ? (
              <Empty title={t("No MCP servers")} />
            ) : (
              <div className="profile-server-list">
                {servers.map((server) => {
                  const contract = contractByServer[server.id];
                  const selected = serverIds.has(server.id);
                  const tools = acceptedTools(contract);
                  const status = contractStatus(contract);
                  const profileMCPPin = profile?.mcpServers?.find(
                    (item) => item.serverId === server.id,
                  );
                  const acceptedContractChanged =
                    contractTransitionPending(server);
                  return (
                    <section key={server.id}>
                      <header>
                        <label>
                          <input
                            type="checkbox"
                            checked={selected}
                            disabled={!contract?.accepted && !selected}
                            onChange={() => selectServer(server)}
                          />
                          <span>
                            <strong>{server.name}</strong>
                            <small>
                              {server.transport} · r
                              {profileMCPPin?.revision ?? server.revision}
                              {profileMCPPin && !profileMCPPin.current
                                ? ` · ${t("Pinned")}`
                                : ""}{" "}
                              · {tools.length}{" "}
                              {t("Tools").toLowerCase()}
                            </small>
                          </span>
                        </label>
                        <Status value={status} />
                      </header>
                      {contract?.reviewState === "changed" && (
                        <div className="profile-governance-notice">
                          {t("Tool definitions changed")}
                        </div>
                      )}
                      {acceptedContractChanged && (
                        <div className="profile-governance-notice action">
                          <span>{t("Tool definition version")}</span>
                          <Button
                            type="button"
                            variant="secondary"
                            aria-label={`${t("Adopt accepted tool definition version")} · ${server.name}`}
                            onClick={() => adoptAcceptedContract(server)}
                          >
                            <RefreshCw size={14} />
                            {t("Adopt accepted tool definition version")}
                          </Button>
                        </div>
                      )}
                      {!contract?.accepted && (
                        <div className="profile-governance-notice">
                          {t("New tools are not visible yet")}
                        </div>
                      )}
                      {selected && contract?.accepted && !acceptedContractChanged && (
                        <>
                          <div className="profile-server-controls">
                            <Field label={`${t("Visibility")} · ${server.name}`}>
                              <select
                                aria-label={`${t("Visibility")} · ${server.name}`}
                                value={governanceFor(server).visibilityMode}
                                onChange={(event) =>
                                  setVisibilityMode(
                                    server,
                                    event.target.value as MCPVisibilityMode,
                                  )
                                }
                              >
                                <option value="all_accepted">
                                  {t("Show all accepted tools")}
                                </option>
                                <option value="selected">
                                  {t("Show selected tools")}
                                </option>
                                <option value="hidden">
                                  {t("Hide all tools")}
                                </option>
                              </select>
                            </Field>
                            <Button
                              type="button"
                              variant="secondary"
                              aria-label={`${t("Configure tools")} · ${server.name}`}
                              onClick={() =>
                                setExpandedServer(
                                  expandedServer === server.id ? "" : server.id,
                                )
                              }
                            >
                              {expandedServer === server.id ? (
                                <ChevronUp size={15} />
                              ) : (
                                <ChevronDown size={15} />
                              )}
                              {t("Configure tools")}
                            </Button>
                          </div>
                          {expandedServer === server.id && (
                            <div className="profile-tool-visibility">
                              {tools.map((tool) => (
                                <label key={tool.id}>
                                  <input
                                    type="checkbox"
                                    aria-label={`${t("Visible")} · ${tool.name}`}
                                    checked={visibleFor(server, tool)}
                                    disabled={tool.status === "paused_incompatible"}
                                    onChange={(event) =>
                                      setToolVisible(
                                        server,
                                        tool,
                                        event.target.checked,
                                      )
                                    }
                                  />
                                  <span>{tool.name}</span>
                                  <small>
                                    {t(
                                      contractToolStatus(
                                        tool.status,
                                        confirmedRenameFor(server, tool),
                                      ),
                                    )}
                                  </small>
                                  <Status value={effectiveDecisionFor(tool)} />
                                </label>
                              ))}
                            </div>
                          )}
                        </>
                      )}
                    </section>
                  );
                })}
              </div>
            )}
          </section>
        )}

        {tab === "rules" && (
          <section className="profile-editor-section" role="tabpanel">
            {selectedServers.length === 0 ? (
              <Empty title={t("Select an MCP server first")} />
            ) : (
              <>
                {pendingContractTransitions.length > 0 && (
                  <div className="profile-governance-notice">
                    {t("Adopt the accepted tool definition version before editing tool rules")}
                  </div>
                )}
                <div className="profile-rule-list">
                  {selectedServers
                    .filter((server) => !contractTransitionPending(server))
                    .flatMap((server) =>
                      acceptedTools(contractByServer[server.id]).map((tool) => {
                        const global = tool.globalDecision;
                        const available = decisions.filter(
                          (decision) =>
                            decisionRank[decision] >= decisionRank[global],
                        );
                        return (
                          <div key={tool.id}>
                            <span>
                              <strong>{tool.name}</strong>
                              <small>
                                {server.name} · {t("Global policy")} {t(global)}
                                {tool.reasonCodes.length
                                  ? ` · ${[
                                      ...new Set(
                                        tool.reasonCodes.map(reasonLabel),
                                      ),
                                    ]
                                      .map((label) => t(label))
                                      .join(" · ")}`
                                  : ""}
                                {decisionFor(tool) !== global
                                  ? ` · ${t("Profile rule")}`
                                  : ""}
                              </small>
                            </span>
                            <Field label={`${t("Decision")} · ${tool.name}`}>
                              <select
                                aria-label={`${t("Decision")} · ${tool.name}`}
                                value={decisionFor(tool)}
                                onChange={(event) =>
                                  setToolDecision(
                                    server,
                                    tool,
                                    event.target.value as ToolDecision,
                                  )
                                }
                              >
                                {available.map((decision) => (
                                  <option key={decision} value={decision}>
                                    {t(decision)}
                                  </option>
                                ))}
                              </select>
                            </Field>
                            <Status value={effectiveDecisionFor(tool)} />
                          </div>
                        );
                      }),
                    )}
                </div>
              </>
            )}
          </section>
        )}

        {tab === "status" && (
          <section className="profile-editor-section" role="tabpanel">
            <div className="profile-application-status">
              <div>
                <span>{t("Revision state")}</span>
                <Status value="Draft" />
              </div>
              <div>
                <span>{t("Pinned Skills")}</span>
                <strong>{skillIds.size}</strong>
              </div>
              <div>
                <span>{t("Pinned MCP revisions")}</span>
                <strong>{serverIds.size}</strong>
              </div>
              <div>
                <span>{t("Effective visibility")}</span>
                <strong>
                  {effectiveVisibleCount}{" "}
                  {t(effectiveVisibleCount === 1 ? "visible tool" : "visible tools")}
                </strong>
              </div>
            </div>
            <div className="profile-governance-notice">
              {t("Saving creates a draft. Apply publishes this exact revision.")}
            </div>
          </section>
        )}

        <div className="modal-actions">
          <Button type="button" variant="secondary" onClick={close}>
            {t("Cancel")}
          </Button>
          <Button
            type="submit"
            disabled={
              busy || !name.trim() || pendingContractTransitions.length > 0
            }
          >
            <Save size={15} />
            {busy ? t("Saving") : t("Save")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function SelectionList({
  items,
  selected,
  toggle,
  empty,
}: {
  items: Array<{ id: string; name: string; detail: string }>;
  selected: Set<string>;
  toggle: (id: string) => void;
  empty: string;
}) {
  if (items.length === 0) return <Empty title={empty} />;
  return (
    <div className="profile-selection-list">
      {items.map((item) => (
        <label key={item.id}>
          <input
            type="checkbox"
            checked={selected.has(item.id)}
            onChange={() => toggle(item.id)}
          />
          <span>
            <strong>{item.name}</strong>
            <small>{item.detail}</small>
          </span>
        </label>
      ))}
    </div>
  );
}

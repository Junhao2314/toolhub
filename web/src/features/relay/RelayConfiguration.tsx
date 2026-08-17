import { Play, Save, ShieldCheck } from "lucide-react";
import { useMemo, useState } from "react";
import {
  api,
  type MCPServer,
  type Operation,
  type RelayConfigurationProjection,
  type RelayPreflightResponse,
  type Target,
} from "../../api/client";
import { Button, ErrorNotice, Status } from "../../components/ui";
import { useI18n } from "../../i18n";

export default function RelayConfiguration({
  configuration,
  servers,
  relayTarget,
  reload,
}: {
  configuration: RelayConfigurationProjection;
  servers: MCPServer[];
  relayTarget?: Target;
  reload: () => void;
}) {
  const { t } = useI18n();
  const [selected, setSelected] = useState(
    () => new Set(configuration.current.mcpServers.map((pin) => pin.serverId)),
  );
  const [preflight, setPreflight] = useState<RelayPreflightResponse | null>(null);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  const currentIDs = configuration.current.mcpServers.map((pin) => pin.serverId);
  const currentIDSet = new Set(currentIDs);
  const serverByID = useMemo(
    () => new Map(servers.map((server) => [server.id, server])),
    [servers],
  );
  const orderedSelectedServers = [
    ...configuration.current.mcpServers.flatMap((pin) => {
      const server = serverByID.get(pin.serverId);
      return server && selected.has(server.id) ? [server] : [];
    }),
    ...servers.filter(
      (server) => selected.has(server.id) && !currentIDSet.has(server.id),
    ),
  ];
  const selectedIDs = orderedSelectedServers.map((server) => server.id);
  const dirty =
    configuration.current.mcpServers.length !== orderedSelectedServers.length ||
    configuration.current.mcpServers.some(
      (pin, index) =>
        pin.serverId !== orderedSelectedServers[index]?.id ||
        pin.mcpRevisionId !== orderedSelectedServers[index]?.currentRevisionId,
    );
  const configurationChanged =
    configuration.current.canonicalHash !== configuration.applied.canonicalHash;

  const toggleServer = (serverID: string) => {
    setSelected((current) => {
      const next = new Set(current);
      next.has(serverID) ? next.delete(serverID) : next.add(serverID);
      return next;
    });
    setPreflight(null);
  };

  const run = async (name: string, action: () => Promise<void>) => {
    setBusy(name);
    setError("");
    setNotice("");
    try {
      await action();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      setBusy("");
    }
  };

  const saveDraft = () =>
    run("save", async () => {
      await api.put("/relay/configuration", {
        revision: configuration.current.revision,
        mcpServerIds: selectedIDs,
        mcpRevisionIds: Object.fromEntries(
          orderedSelectedServers.map((server) => [server.id, server.currentRevisionId]),
        ),
        metadata: configuration.current.metadata ?? {},
      });
      setPreflight(null);
      setNotice(t("Shared mcpm configuration draft saved"));
      reload();
    });

  const runPreflight = () =>
    run("preflight", async () => {
      const result = await api.post<RelayPreflightResponse>(
        "/relay/configuration/preflight",
        { revisionId: configuration.current.id, profileIds: [], mode: "compatibility" },
      );
      setPreflight(result);
      setNotice(t("Shared relay preflight passed"));
    });

  const apply = () =>
    run("apply", async () => {
      if (!preflight) return;
      const operation = await api.post<Operation>("/relay/configuration/apply", {
        revisionId: configuration.current.id,
        profileIds: [],
        mode: "compatibility",
        targetRevision: preflight.result.targetRevision,
        routingHash: preflight.routingHash,
      });
      setNotice(`${t("Shared relay update queued")} · ${operation.id.slice(0, 8)}`);
      reload();
    });

  const preflightItems = preflight
    ? [
        ...preflight.result.diff.add.map((item) => ({ ...item, action: "Add" })),
        ...preflight.result.diff.replace.map((item) => ({ ...item, action: "Replace" })),
        ...preflight.result.diff.delete.map((item) => ({ ...item, action: "Delete" })),
        ...preflight.result.diff.excluded.map((item) => ({ ...item, action: "Excluded" })),
      ]
    : [];

  return (
    <section className="relay-section" aria-labelledby="relay-configuration-title">
      <header className="relay-section-heading">
        <div>
          <h2 id="relay-configuration-title">{t("Shared mcpm relay")}</h2>
          <p>{t("ToolHub edits the enabled set; mcpm owns every upstream process")}</p>
        </div>
        <Status value="compatibility" />
      </header>

      {error && <ErrorNotice message={error} />}
      {notice && <div className="inline-notice">{notice}</div>}

      <div className="relay-revision-strip">
        <div>
          <span>{t("Current draft")}</span>
          <strong>{t("Configuration")} r{configuration.current.revision}</strong>
          <code>{configuration.current.canonicalHash.slice(0, 12)}</code>
        </div>
        <div>
          <span>{t("Running revision")}</span>
          <strong>{t("Applied")} r{configuration.applied.revision}</strong>
          <code>{configuration.applied.canonicalHash.slice(0, 12)}</code>
        </div>
        <div className={configurationChanged ? "restart" : "hot-reload"}>
          <span>{t("Runtime impact")}</span>
          <strong>{t(configurationChanged ? "mcpm will reconcile the shared process set" : "No pending relay change")}</strong>
        </div>
      </div>

      <div className="relay-readiness-strip">
        <div>
          <span>{t("mcpm capability")}</span>
          <strong>
            {configuration.runtimeCapability.compatible
              ? `mcpm ${configuration.runtimeCapability.runtimeVersion}`
              : t("mcpm unavailable")}
          </strong>
          <small>
            {configuration.runtimeCapability.compatible
              ? t("Compatibility/pass-through health is HTTP-backed")
              : configuration.runtimeCapability.errorCode}
          </small>
        </div>
        <div>
          <span>{t("Ownership")}</span>
          <strong>{t("ToolHub config · mcpm processes")}</strong>
          <small>{t("Profiles manage Skills only")}</small>
        </div>
      </div>

      <div className="table-scroll">
        <table aria-label={t("Running MCP set")}>
          <thead>
            <tr>
              <th>{t("Enabled member")}</th>
              <th>{t("Config revision")}</th>
              <th>{t("Process state")}</th>
              <th>{t("Capabilities")}</th>
            </tr>
          </thead>
          <tbody>
            {configuration.applied.mcpServers.map((pin) => {
              const server = serverByID.get(pin.serverId);
              const member = relayTarget?.relayMemberStatuses.find(
                (item) => item.memberId === pin.mcpRevisionId || item.name === server?.name,
              );
              return (
                <tr key={pin.serverId}>
                  <td><strong>{server?.name ?? pin.serverId}</strong><small>{server?.transport ?? "—"}</small></td>
                  <td><code>{pin.mcpRevisionId.slice(0, 12)}</code></td>
                  <td><Status value={member?.status ?? "unavailable"} />{member?.errorReason && <small>{member.errorReason}</small>}</td>
                  <td>{member ? `${member.capabilities.tools} ${t("Tools").toLowerCase()}` : "—"}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      <details className="relay-editor" open>
        <summary>{t("Edit enabled mcpm members")}</summary>
        <div className="relay-server-options">
          {servers.map((server) => (
            <label key={server.id}>
              <input type="checkbox" checked={selected.has(server.id)} onChange={() => toggleServer(server.id)} />
              <span><strong>{server.name}</strong><small>{server.transport} · r{server.revision}</small></span>
            </label>
          ))}
        </div>
        <Button disabled={!dirty || busy !== ""} onClick={saveDraft}><Save size={15} />{t("Save relay draft")}</Button>
      </details>

      <div className="relay-apply-flow">
        <div>
          <span>1</span><strong>{t("Relay preflight")}</strong>
          {preflightItems.length > 0 ? <div className="relay-diff-list">{preflightItems.map((item, index) => <small key={`${item.action}-${item.name}-${index}`}>{t(item.action)} · {item.name}</small>)}</div> : <small>{t("Save a draft, then preflight the exact enabled set")}</small>}
          <Button variant="secondary" disabled={busy !== "" || dirty} onClick={runPreflight}><ShieldCheck size={15} />{t("Run relay preflight")}</Button>
        </div>
        <div>
          <span>2</span><strong>{t("Apply shared relay")}</strong>
          <small>{t("mcpm starts, stops, and reuses one process per enabled member")}</small>
          <Button disabled={busy !== "" || !preflight} onClick={apply}><Play size={15} />{t("Apply relay update")}</Button>
        </div>
      </div>
    </section>
  );
}

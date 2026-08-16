import { Play, Save, ShieldCheck } from "lucide-react";
import { useMemo, useState } from "react";
import {
  api,
  type MCPServer,
  type Operation,
  type Profile,
  type RelayConfigurationProjection,
  type RelayPreflightResponse,
  type Target,
} from "../../api/client";
import { Button, ErrorNotice, Status } from "../../components/ui";
import { useI18n } from "../../i18n";

export default function RelayConfiguration({
  configuration,
  servers,
  profiles,
  relayTarget,
  reload,
}: {
  configuration: RelayConfigurationProjection;
  servers: MCPServer[];
  profiles: Profile[];
  relayTarget?: Target;
  reload: () => void;
}) {
  const { t } = useI18n();
  const [selected, setSelected] = useState(
    () => new Set(configuration.current.mcpServers.map((pin) => pin.serverId)),
  );
  const [affectedProfileIDs, setAffectedProfileIDs] = useState<string[] | null>(
    null,
  );
  const [preflight, setPreflight] = useState<RelayPreflightResponse | null>(null);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  const currentIDs = configuration.current.mcpServers.map((pin) => pin.serverId);
  const currentIDSet = new Set(currentIDs);
  const orderedSelectedServers = [
    ...configuration.current.mcpServers.flatMap((pin) => {
      const server = servers.find((candidate) => candidate.id === pin.serverId);
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
  const profileByID = useMemo(
    () => new Map(profiles.map((profile) => [profile.id, profile])),
    [profiles],
  );
  const serverByID = useMemo(
    () => new Map(servers.map((server) => [server.id, server])),
    [servers],
  );
  const configurationChanged =
    configuration.current.canonicalHash !== configuration.applied.canonicalHash;

  const toggleServer = (serverID: string) => {
    setSelected((current) => {
      const next = new Set(current);
      next.has(serverID) ? next.delete(serverID) : next.add(serverID);
      return next;
    });
    setAffectedProfileIDs(null);
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
          orderedSelectedServers.map((server) => [
            server.id,
            server.currentRevisionId,
          ]),
        ),
        metadata: configuration.current.metadata ?? {},
      });
      setNotice(t("Relay configuration draft saved"));
      reload();
    });

  const prepare = () =>
    run("prepare", async () => {
      const result = await api.post<{ items: string[] }>(
        "/relay/configuration/prepare-profile-updates",
        { revisionId: configuration.current.id },
      );
      setAffectedProfileIDs(result.items);
      setPreflight(null);
      setNotice(t("Affected Profile revisions prepared"));
    });

  const runPreflight = () =>
    run("preflight", async () => {
      const result = await api.post<RelayPreflightResponse>(
        "/relay/configuration/preflight",
        {
          revisionId: configuration.current.id,
          profileIds: affectedProfileIDs ?? [],
        },
      );
      setPreflight(result);
      setNotice(t("Relay preflight passed"));
    });

  const apply = () =>
    run("apply", async () => {
      if (!preflight) return;
      const operation = await api.post<Operation>(
        "/relay/configuration/apply",
        {
          revisionId: configuration.current.id,
          profileIds: affectedProfileIDs ?? [],
          targetRevision: preflight.result.targetRevision,
          routingHash: preflight.routingHash,
        },
      );
      setNotice(`${t("Relay update queued")} · ${operation.id.slice(0, 8)}`);
    });

  const preflightItems = preflight
    ? [
        ...preflight.result.diff.add.map((item) => ({ ...item, action: "Add" })),
        ...preflight.result.diff.replace.map((item) => ({
          ...item,
          action: "Replace",
        })),
        ...preflight.result.diff.delete.map((item) => ({
          ...item,
          action: "Delete",
        })),
        ...preflight.result.diff.excluded.map((item) => ({
          ...item,
          action: "Excluded",
        })),
      ]
    : [];

  return (
    <section className="relay-section" aria-labelledby="relay-configuration-title">
      <header className="relay-section-heading">
        <div>
          <h2 id="relay-configuration-title">{t("Relay configuration")}</h2>
          <p>{t("One shared process set with revision-bound routing")}</p>
        </div>
        <Status value={configuration.mode} />
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
          <strong>
            {t(
              configurationChanged
                ? "Will disconnect current sessions"
                : "Profile-only routing update does not restart relay",
            )}
          </strong>
        </div>
      </div>

      <div className="table-scroll">
        <table aria-label={t("Running MCP set")}>
          <thead>
            <tr>
              <th>{t("Running set")}</th>
              <th>{t("Config revision")}</th>
              <th>{t("Process state")}</th>
              <th>{t("Capabilities")}</th>
            </tr>
          </thead>
          <tbody>
            {configuration.applied.mcpServers.map((pin) => {
              const server = serverByID.get(pin.serverId);
              const member = relayTarget?.relayMemberStatuses.find(
                (item) =>
                  item.memberId === pin.mcpRevisionId ||
                  item.name === server?.name,
              );
              return (
                <tr key={pin.serverId}>
                  <td>
                    <strong>{server?.name ?? pin.serverId}</strong>
                    <small>{server?.transport ?? "—"}</small>
                  </td>
                  <td>
                    <code>{pin.mcpRevisionId.slice(0, 12)}</code>
                  </td>
                  <td>
                    <Status value={member?.status ?? "unavailable"} />
                    {member?.errorReason && <small>{member.errorReason}</small>}
                  </td>
                  <td>
                    {member
                      ? `${member.capabilities.tools} ${t("Tools").toLowerCase()}`
                      : "—"}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      <details className="relay-editor">
        <summary>{t("Edit running set")}</summary>
        <div className="relay-server-options">
          {servers.map((server) => (
            <label key={server.id}>
              <input
                type="checkbox"
                checked={selected.has(server.id)}
                onChange={() => toggleServer(server.id)}
              />
              <span>
                <strong>{server.name}</strong>
                <small>
                  {server.transport} · r{server.revision}
                </small>
              </span>
            </label>
          ))}
        </div>
        <Button disabled={!dirty || busy !== ""} onClick={saveDraft}>
          <Save size={15} />
          {t("Save relay draft")}
        </Button>
      </details>

      <div className="relay-apply-flow">
        <div>
          <span>1</span>
          <strong>{t("Affected Profiles")}</strong>
          {affectedProfileIDs === null ? (
            <small>{t("Prepare exact candidate revisions before preflight")}</small>
          ) : affectedProfileIDs.length === 0 ? (
            <small>{t("No affected Published Profiles")}</small>
          ) : (
            <div className="relay-profile-list">
              {affectedProfileIDs.map((id) => (
                <span key={id}>{profileByID.get(id)?.name ?? id}</span>
              ))}
            </div>
          )}
          <Button
            variant="secondary"
            disabled={busy !== "" || dirty}
            onClick={prepare}
          >
            <Play size={15} />
            {t("Prepare relay update")}
          </Button>
        </div>
        <div>
          <span>2</span>
          <strong>{t("Relay preflight")}</strong>
          {preflightItems.length > 0 ? (
            <div className="relay-diff-list">
              {preflightItems.map((item, index) => (
                <small key={`${item.action}-${item.name}-${index}`}>
                  {t(item.action)} · {item.name}
                </small>
              ))}
            </div>
          ) : (
            <small>{t("No preflight result")}</small>
          )}
          <Button
            variant="secondary"
            disabled={busy !== "" || affectedProfileIDs === null}
            onClick={runPreflight}
          >
            <ShieldCheck size={15} />
            {t("Run relay preflight")}
          </Button>
        </div>
        <div>
          <span>3</span>
          <strong>{t("Apply relay update")}</strong>
          <small>{t("Publishes only after relay health verification")}</small>
          <Button disabled={busy !== "" || !preflight} onClick={apply}>
            <Play size={15} />
            {t("Apply relay update")}
          </Button>
        </div>
      </div>
    </section>
  );
}

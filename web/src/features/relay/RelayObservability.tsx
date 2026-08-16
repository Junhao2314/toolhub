import { RefreshCw } from "lucide-react";
import { useMemo } from "react";
import {
  type ContractGovernanceProjection,
  type DailyToolAggregate,
  type MCPServer,
  type Profile,
  type RelayObservationDrain,
} from "../../api/client";
import { Button, Empty, ErrorNotice, Status } from "../../components/ui";
import { useI18n } from "../../i18n";

export default function RelayObservability({
  live,
  daily,
  profiles,
  servers,
  contracts,
  liveUnavailable,
  reload,
}: {
  live: RelayObservationDrain;
  daily: { items: DailyToolAggregate[]; days: number };
  profiles: Profile[];
  servers: MCPServer[];
  contracts: ContractGovernanceProjection;
  liveUnavailable?: string;
  reload: () => void;
}) {
  const { t } = useI18n();
  const profileNames = useMemo(
    () => new Map(profiles.map((profile) => [profile.id, profile.name])),
    [profiles],
  );
  const serverNames = useMemo(
    () => new Map(servers.map((server) => [server.id, server.name])),
    [servers],
  );
  const toolNames = useMemo(() => {
    const result = new Map<string, string>();
    for (const contract of contracts.items) {
      for (const tool of [
        ...(contract.accepted?.tools ?? []),
        ...(contract.latest?.tools ?? []),
      ]) {
        result.set(tool.id, tool.name);
      }
    }
    return result;
  }, [contracts.items]);
  const name = (mapping: Map<string, string>, id?: string) =>
    (id && mapping.get(id)) || (id ? id.slice(0, 8) : "—");

  return (
    <section className="relay-section" aria-labelledby="relay-observability-title">
      <header className="relay-section-heading">
        <div>
          <h2 id="relay-observability-title">{t("Relay observability")}</h2>
          <p>{t("Payload-free outcomes and bounded latency summaries")}</p>
        </div>
        <Button variant="secondary" onClick={reload}>
          <RefreshCw size={15} />
          {t("Refresh")}
        </Button>
      </header>
      {liveUnavailable && <ErrorNotice message={liveUnavailable} retry={reload} />}

      <div className="relay-observation-grid">
        <section aria-labelledby="live-outcomes-title">
          <header>
            <h3 id="live-outcomes-title">{t("Live outcomes")}</h3>
            <span>{live.items.length}</span>
          </header>
          {live.items.length === 0 ? (
            <Empty title={t("No live outcomes")} />
          ) : (
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>{t("Profile")}</th>
                    <th>{t("Server")}</th>
                    <th>{t("Tool")}</th>
                    <th>{t("Decision")}</th>
                    <th>{t("Outcome")}</th>
                    <th>{t("Latency")}</th>
                  </tr>
                </thead>
                <tbody>
                  {live.items.map((item) => (
                    <tr key={`${item.bootId}-${item.sequence}`}>
                      <td>{name(profileNames, item.profileId)}</td>
                      <td>{name(serverNames, item.serverId)}</td>
                      <td>{name(toolNames, item.toolId)}</td>
                      <td><Status value={item.decision} /></td>
                      <td><Status value={item.outcome} /></td>
                      <td>{t(item.durationBucket.replaceAll("_", " "))}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {live.items.some((item) => item.outcome === "unknown") && (
            <div className="profile-governance-notice">
              {t("Check actual state before trying and confirming again")}
            </div>
          )}
        </section>

        <section aria-labelledby="daily-aggregates-title">
          <header>
            <h3 id="daily-aggregates-title">
              {t("30-day aggregates")}
            </h3>
            <span>{daily.items.length}</span>
          </header>
          {daily.items.length === 0 ? (
            <Empty title={t("No aggregate outcomes")} />
          ) : (
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>{t("Day")}</th>
                    <th>{t("Profile")}</th>
                    <th>{t("Tool")}</th>
                    <th>{t("Outcome")}</th>
                    <th>{t("Calls")}</th>
                    <th>{t("Errors")}</th>
                    <th>{t("Latency")}</th>
                  </tr>
                </thead>
                <tbody>
                  {daily.items.map((item, index) => (
                    <tr
                      key={`${item.day}-${item.profileId}-${item.toolId}-${item.outcome}-${index}`}
                    >
                      <td>{item.day}</td>
                      <td>{name(profileNames, item.profileId)}</td>
                      <td>{name(toolNames, item.toolId)}</td>
                      <td><Status value={item.outcome} /></td>
                      <td>{item.callCount}</td>
                      <td>{item.errorCount}</td>
                      <td>
                        {item.durationBucket
                          ? t(item.durationBucket.replaceAll("_", " "))
                          : "—"}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </section>
      </div>
    </section>
  );
}

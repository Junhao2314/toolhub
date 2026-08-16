import { Check, RefreshCw } from "lucide-react";
import { useState } from "react";
import {
  api,
  type ContractGovernanceProjection,
  type ContractState,
  type ObservedContractTool,
  type Operation,
} from "../../api/client";
import { Button, Empty, ErrorNotice, Field, Status } from "../../components/ui";
import { useI18n } from "../../i18n";

type ReviewTool = ObservedContractTool & { serverName: string };

function contractChanges(contracts: ContractState[]) {
  const added: ReviewTool[] = [];
  const changed: ReviewTool[] = [];
  const removed: ReviewTool[] = [];
  for (const contract of contracts) {
    const latestIDs = new Set(
      (contract.latest?.tools ?? []).map((tool) => tool.id),
    );
    for (const tool of contract.latest?.tools ?? []) {
      if (tool.status === "new_hidden") {
        added.push({ ...tool, serverName: contract.serverName });
      } else if (tool.status !== "unchanged") {
        changed.push({ ...tool, serverName: contract.serverName });
      }
    }
    for (const tool of contract.accepted?.tools ?? []) {
      if (!latestIDs.has(tool.id)) {
        removed.push({ ...tool, serverName: contract.serverName });
      }
    }
  }
  return { added, changed, removed };
}

export default function ContractReview({
  contracts,
  reload,
}: {
  contracts: ContractGovernanceProjection;
  reload: () => void;
}) {
  const { t } = useI18n();
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [ambiguousMappingID, setAmbiguousMappingID] = useState("");
  const changes = contractChanges(contracts.items);
  const suspected = contracts.renames.filter(
    (proposal) => proposal.status === "suspected",
  );
  const ambiguous = contracts.renames.filter(
    (proposal) => proposal.status === "ambiguous",
  );

  const run = async (id: string, action: () => Promise<void>, message: string) => {
    setBusy(id);
    setError("");
    setNotice("");
    try {
      await action();
      setNotice(message);
      reload();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      setBusy("");
    }
  };

  return (
    <section className="relay-section" aria-labelledby="contract-review-title">
      <header className="relay-section-heading">
        <div>
          <h2 id="contract-review-title">{t("Tool definition review")}</h2>
          <p>{t("Latest observations never advance accepted definitions silently")}</p>
        </div>
        <Button
          variant="secondary"
          disabled={busy !== ""}
          onClick={() =>
            run(
              "observe",
              async () => {
                await api.post<Operation>("/relay/contracts/observe");
              },
              t("Contract observation queued"),
            )
          }
        >
          <RefreshCw className={busy === "observe" ? "spin" : ""} size={15} />
          {t("Observe tool definitions")}
        </Button>
      </header>

      {error && <ErrorNotice message={error} />}
      {notice && <div className="inline-notice">{notice}</div>}

      <div className="contract-version-list">
        {contracts.items.map((contract) => (
          <div key={contract.serverId}>
            <span>
              <strong>{contract.serverName}</strong>
              <small>
                {t("Latest")} r{contract.latest?.revision.revision ?? "—"} ·{" "}
                {t("Accepted")} r{contract.accepted?.revision.revision ?? "—"}
              </small>
            </span>
            <Status value={contract.reviewState} />
            {contract.latest &&
              contract.latest.revision.id !== contract.accepted?.revision.id && (
                <Button
                  variant="secondary"
                  disabled={busy !== ""}
                  onClick={() =>
                    run(
                      contract.latest!.revision.id,
                      () =>
                        api.post(
                          `/relay/contracts/${contract.latest!.revision.id}/accept`,
                        ),
                      t("Tool definition accepted"),
                    )
                  }
                >
                  <Check size={14} />
                  {t("Accept tool definition")}
                </Button>
              )}
          </div>
        ))}
      </div>

      <div className="contract-change-grid">
        <ChangeGroup title={t("New tools")} items={changes.added} />
        <ChangeGroup title={t("Changed tools")} items={changes.changed} />
        <ChangeGroup title={t("Removed tools")} items={changes.removed} />
      </div>

      <div className="rename-review">
        <h3>{t("Suspected rename")}</h3>
        {suspected.length === 0 ? (
          <Empty title={t("No suspected renames")} />
        ) : (
          suspected.map((proposal) => (
            <div key={proposal.id}>
              <span>
                <strong>
                  {proposal.removedToolName} → {proposal.addedToolName}
                </strong>
                <small>{t("Matching schema requires operator confirmation")}</small>
              </span>
              <Button
                variant="secondary"
                aria-label={`${t("Confirm rename")} · ${proposal.removedToolName} → ${proposal.addedToolName}`}
                disabled={busy !== ""}
                onClick={() =>
                  run(
                    proposal.id,
                    () => api.post(`/relay/renames/${proposal.id}/confirm`),
                    t("Rename confirmed; candidate revisions created"),
                  )
                }
              >
                <Check size={14} />
                {t("Confirm rename")}
              </Button>
            </div>
          ))
        )}
        {ambiguous.length > 0 && (
          <div className="ambiguous-renames">
            <strong>{t("Explicit mapping required")}</strong>
            <small>
              {t("Ambiguous schema matches remain hidden and cannot be confirmed automatically")}
            </small>
            <div className="ambiguous-mapping-controls">
              <Field label={t("Explicit rename mapping")}>
                <select
                  value={ambiguousMappingID}
                  onChange={(event) => setAmbiguousMappingID(event.target.value)}
                >
                  <option value="">{t("Select a mapping")}</option>
                  {ambiguous.map((proposal) => (
                    <option key={proposal.id} value={proposal.id}>
                      {proposal.removedToolName} → {proposal.addedToolName}
                    </option>
                  ))}
                </select>
              </Field>
              <Button
                variant="secondary"
                disabled={busy !== "" || !ambiguousMappingID}
                onClick={() => {
                  const selected = ambiguous.find(
                    (proposal) => proposal.id === ambiguousMappingID,
                  );
                  if (!selected) return;
                  void run(
                    selected.id,
                    () => api.post(`/relay/renames/${selected.id}/confirm`),
                    t("Rename confirmed; candidate revisions created"),
                  );
                }}
              >
                <Check size={14} />
                {t("Confirm selected mapping")}
              </Button>
            </div>
          </div>
        )}
      </div>
    </section>
  );
}

function ChangeGroup({ title, items }: { title: string; items: ReviewTool[] }) {
  const { t } = useI18n();
  return (
    <section aria-label={title}>
      <header>
        <h3>{title}</h3>
        <span>{items.length}</span>
      </header>
      {items.length === 0 ? (
        <small>{t("None")}</small>
      ) : (
        items.map((tool) => (
          <div key={`${tool.serverId}-${tool.id}`}>
            <strong>{tool.name}</strong>
            <small>{tool.serverName}</small>
            <Status value={tool.status} />
          </div>
        ))
      )}
    </section>
  );
}

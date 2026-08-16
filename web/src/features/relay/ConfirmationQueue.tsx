import { Check, X } from "lucide-react";
import { useState } from "react";
import {
  api,
  APIError,
  type ArgumentSummary,
  type ConfirmationSummary,
} from "../../api/client";
import { Button, Empty, ErrorNotice, Field, Status } from "../../components/ui";
import { useI18n } from "../../i18n";

function argumentSummaryLabel(
  summary: ArgumentSummary,
  translate: (value: string) => string,
) {
  const prefix = translate(summary.sensitive ? "Sensitive" : "Non-sensitive");
  if (summary.valueType === "string")
    return `${prefix} ${translate("string")} · ${summary.stringLength ?? 0} ${translate("characters")}`;
  if (summary.valueType === "array")
    return `${prefix} ${translate("array")} · ${summary.arrayLength ?? 0} ${translate("items")}`;
  return `${prefix} ${translate(summary.valueType)}`;
}

export default function ConfirmationQueue({
  items,
  unavailable,
  reload,
}: {
  items: ConfirmationSummary[];
  unavailable?: string;
  reload: () => void;
}) {
  const { t } = useI18n();
  const [profileNames, setProfileNames] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [unknownOutcome, setUnknownOutcome] = useState(false);

  const decide = async (summary: ConfirmationSummary, approve: boolean) => {
    setBusy(summary.challengeId);
    setError("");
    setNotice("");
    setUnknownOutcome(false);
    try {
      await api.post(
        `/relay/confirmations/${summary.challengeId}/${approve ? "approve" : "reject"}`,
        approve
          ? {
              profileName: profileNames[summary.challengeId] ?? "",
              bindingHash: summary.bindingHash,
            }
          : { bindingHash: summary.bindingHash },
      );
      setNotice(t(approve ? "One-shot grant created" : "Confirmation rejected"));
      reload();
    } catch (reason) {
      if (
        reason instanceof APIError &&
        reason.code === "confirmation_outcome_unknown"
      ) {
        setUnknownOutcome(true);
      }
      setError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      setBusy("");
    }
  };

  return (
    <section className="relay-section" aria-labelledby="confirmation-queue-title">
      <header className="relay-section-heading">
        <div>
          <h2 id="confirmation-queue-title">{t("Pending confirmations")}</h2>
          <p>{t("One-shot grants are bound to exact revisions and argument hashes")}</p>
        </div>
        <span className="count-badge">{items.length}</span>
      </header>
      {unavailable && <ErrorNotice message={unavailable} retry={reload} />}
      {error && <ErrorNotice message={error} />}
      {notice && <div className="inline-notice">{notice}</div>}
      {unknownOutcome && (
        <div className="profile-governance-notice">
          {t("Check actual state before trying and confirming again")}
        </div>
      )}
      {items.length === 0 ? (
        <Empty title={t("No pending confirmations")} />
      ) : (
        <div className="confirmation-list">
          {items.map((summary) => {
            const expired = summary.expiresAt <= Date.now() / 1000;
            const exactName = profileNames[summary.challengeId] ?? "";
            return (
              <section
                key={summary.challengeId}
                role="region"
                aria-label={`${t("Confirmation")} · ${summary.profileName} · ${summary.toolName}`}
              >
                <header>
                  <span>
                    <strong>{summary.toolName}</strong>
                    <small>
                      {summary.serverName} · {summary.clientKind} · {summary.runtimeName}
                    </small>
                  </span>
                  <Status value={expired ? "expired" : summary.decision} />
                </header>
                <dl>
                  <div>
                    <dt>{t("Profile")}</dt>
                    <dd>{summary.profileName}</dd>
                  </div>
                  <div>
                    <dt>{t("Argument hash")}</dt>
                    <dd>
                      <code>{summary.argumentHash}</code>
                    </dd>
                  </div>
                  <div>
                    <dt>{t("Reason chain")}</dt>
                    <dd>
                      {summary.reasonCodes
                        .map((reason) => t(reason.replaceAll("_", " ")))
                        .join(" · ") || "—"}
                    </dd>
                  </div>
                  <div>
                    <dt>{t("Expires")}</dt>
                    <dd>{new Date(summary.expiresAt * 1000).toLocaleString()}</dd>
                  </div>
                </dl>
                <div className="argument-summary-list">
                  {summary.argumentSummary.map((item) => (
                    <span key={item.pointer}>
                      <code>{item.pointer || "/"}</code>
                      {argumentSummaryLabel(item, t)}
                    </span>
                  ))}
                </div>
                <div className="confirmation-actions">
                  <Field label={t("Exact Profile name")}>
                    <input
                      value={exactName}
                      disabled={expired || busy !== ""}
                      onChange={(event) =>
                        setProfileNames((current) => ({
                          ...current,
                          [summary.challengeId]: event.target.value,
                        }))
                      }
                    />
                  </Field>
                  <Button
                    variant="danger"
                    disabled={expired || busy !== ""}
                    onClick={() => decide(summary, false)}
                  >
                    <X size={15} />
                    {t("Reject")}
                  </Button>
                  <Button
                    disabled={
                      expired || busy !== "" || exactName !== summary.profileName
                    }
                    onClick={() => decide(summary, true)}
                  >
                    <Check size={15} />
                    {t("Approve once")}
                  </Button>
                </div>
              </section>
            );
          })}
        </div>
      )}
    </section>
  );
}

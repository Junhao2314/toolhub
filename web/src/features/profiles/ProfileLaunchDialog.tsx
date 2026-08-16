import { Check, Copy, Terminal } from "lucide-react";
import { useState } from "react";
import {
  api,
  type Profile,
  type ProfileLaunchReadiness,
} from "../../api/client";
import {
  Button,
  ErrorNotice,
  Loading,
  Modal,
  Status,
} from "../../components/ui";
import { useData } from "../../hooks/useData";
import { useI18n } from "../../i18n";

function readinessReason(reasonCode = "") {
  if (
    reasonCode === "profile_revision_not_published" ||
    reasonCode === "profile_target_not_applied" ||
    reasonCode === "profile_apply_unfinalized" ||
    reasonCode === "relay_not_applied" ||
    reasonCode === "relay_routing_mismatch" ||
    reasonCode === "relay_routing_unavailable"
  ) {
    return "Configuration mismatch";
  }
  if (reasonCode.includes("contract")) return "Tool definition version";
  if (reasonCode.includes("policy") || reasonCode.includes("rule"))
    return "Tool rules";
  if (reasonCode === "relay_paused") return "Paused";
  return "Unavailable";
}

export default function ProfileLaunchDialog({
  profile,
  close,
}: {
  profile: Profile;
  close: () => void;
}) {
  const { t } = useI18n();
  const state = useData(
    () =>
      api.request<ProfileLaunchReadiness>(`/profiles/${profile.id}/launch`),
    [profile.id, profile.currentRevisionId],
  );
  const [copied, setCopied] = useState(false);

  const copyCommand = () => {
    const display = state.data?.command?.display;
    if (!display) return;
    navigator.clipboard
      .writeText(display)
      .then(() => setCopied(true))
      .catch(() => setCopied(false));
  };

  return (
    <Modal title={`${t("Launch session")} · ${profile.name}`} close={close}>
      {state.loading && <Loading label={t("Checking launch readiness")} />}
      {state.error && <ErrorNotice message={state.error} retry={state.reload} />}
      {state.data && (
        <>
          <div className="launch-readiness">
            <div>
              <span>{t("Profile revision")}</span>
              <code>{state.data.profileRevisionId.slice(0, 12)}</code>
            </div>
            <div>
              <span>{t("Native client")}</span>
              <strong>
                {state.data.nativeClient.clientKind} {state.data.nativeClient.version}
              </strong>
            </div>
            <div>
              <span>{t("Readiness")}</span>
              <Status value={state.data.ready ? "ready" : "blocked"} />
            </div>
          </div>
          {state.data.ready && state.data.command ? (
            <div className="launch-command">
              <Terminal size={18} />
              <code>{state.data.command.display}</code>
            </div>
          ) : (
            <div className="launch-blocked-reason">
              <strong>{t(readinessReason(state.data.reasonCode))}</strong>
              {state.data.reasonCode && <code>{state.data.reasonCode}</code>}
            </div>
          )}
        </>
      )}
      <div className="modal-actions">
        {state.data?.ready && state.data.command && (
          <Button type="button" variant="secondary" onClick={copyCommand}>
            {copied ? <Check size={15} /> : <Copy size={15} />}
            {copied ? t("Copied") : t("Copy launch command")}
          </Button>
        )}
        <Button type="button" onClick={close}>
          {t("Close")}
        </Button>
      </div>
    </Modal>
  );
}

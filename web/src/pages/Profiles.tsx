import {
  Archive,
  Download,
  Eye,
  History,
  KeyRound,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  Trash2,
  Upload,
  UsersRound,
} from "lucide-react";
import { useState } from "react";
import {
  api,
  type BundlePreview,
  type ContractGovernanceProjection,
  type MCPServer,
  type Operation,
  type PendingSecretBinding,
  type Profile,
  type ProfileRevision,
  type Skill,
  type Target,
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
import ProfileGovernanceEditor from "../features/profiles/ProfileGovernanceEditor";
import ProfileLaunchDialog from "../features/profiles/ProfileLaunchDialog";

interface DiffItem {
  kind: string;
  memberId?: string;
  name: string;
  reason?: string;
}
interface PreflightItem {
  targetId: string;
  confirmationToken: string;
  expiresAt: string;
  result: {
    targetRevision: string;
    manifestHash: string;
    diff: {
      add: DiffItem[];
      replace: DiffItem[];
      delete: DiffItem[];
      excluded: DiffItem[];
    };
  };
}

export default function Profiles() {
  const { t } = useI18n();
  const state = useData(async () => {
    const [profiles, skills, servers, targets, contracts] = await Promise.all([
      api.list<Profile>("/profiles?includeArchived=true"),
      api.list<Skill>("/skills"),
      api.list<MCPServer>("/mcp/servers"),
      api.list<Target>("/targets"),
      api.request<ContractGovernanceProjection>("/relay/contracts"),
    ]);
    return {
      profiles: profiles.items,
      skills: skills.items,
      servers: servers.items,
      targets: targets.items,
      contracts,
    };
  }, []);
  const [selected, setSelected] = useState<Profile | null>(null);
  const [editing, setEditing] = useState<Profile | "new" | null>(null);
  const [applying, setApplying] = useState<Profile | null>(null);
  const [launching, setLaunching] = useState<Profile | null>(null);
  const [history, setHistory] = useState<ProfileRevision[] | null>(null);
  const [bundleFile, setBundleFile] = useState<File | null>(null);
  const [bundlePreview, setBundlePreview] = useState<BundlePreview | null>(
    null,
  );
  const [secretExportProfile, setSecretExportProfile] =
    useState<Profile | null>(null);
  const [notice, setNotice] = useState("");
  if (state.loading) return <Loading />;
  if (state.error || !state.data)
    return <ErrorNotice message={state.error} retry={state.reload} />;
  const selectedProfile = selected
    ? (state.data.profiles.find((profile) => profile.id === selected.id) ??
      selected)
    : null;
  const reloadSelected = () => state.reload();
  const remove = (profile: Profile) => {
    if (!confirm(`${t("Archive")} ${profile.name}?`)) return;
    api
      .post(`/profiles/${profile.id}/archive`, { revision: profile.revision })
      .then(() => {
        setSelected(null);
        state.reload();
      })
      .catch((reason: Error) => setNotice(reason.message));
  };
  const exportBundle = (profile: Profile, secrets: boolean) => {
    if (secrets) {
      setSecretExportProfile(profile);
      return;
    }
    api
      .download(
        `/profiles/${profile.id}/bundle/export`,
        { originLabel: "ToolHub" },
      )
      .then((blob) => {
        const url = URL.createObjectURL(blob);
        const anchor = document.createElement("a");
        anchor.href = url;
        anchor.download = `${profile.name}.toolhub-profile`;
        anchor.click();
        URL.revokeObjectURL(url);
      })
      .catch((reason: Error) => setNotice(reason.message));
  };
  const previewBundle = (file: File) => {
    setBundleFile(file);
    api
      .previewBundle(file)
      .then(setBundlePreview)
      .catch((reason: Error) => setNotice(reason.message));
  };
  const importBundle = () => {
    if (!bundleFile || !bundlePreview) return;
    const name = bundlePreview.requiresRename
      ? (bundlePreview.suggestedName ?? `${bundlePreview.profileName} imported`)
      : bundlePreview.profileName;
    api
      .importBundle(bundleFile, {
        confirmationToken: bundlePreview.confirmationToken,
        name,
        confirmDuplicate: bundlePreview.duplicate,
        importAsNew:
          bundlePreview.updateExisting && name !== bundlePreview.profileName,
      })
      .then(() => {
        setBundleFile(null);
        setBundlePreview(null);
        state.reload();
      })
      .catch((reason: Error) => setNotice(reason.message));
  };
  return (
    <>
      <PageHeader
        title={t("Profiles")}
        detail={t("Unified Skill and MCP membership")}
        actions={
          <>
            <label className="button secondary">
              <Upload size={16} />
              {t("Import Bundle")}
              <input
                type="file"
                accept=".toolhub-profile,.toolhub-profile-secrets"
                hidden
                onChange={(event) => {
                  const file = event.target.files?.[0];
                  if (file) previewBundle(file);
                }}
              />
            </label>
            <Button onClick={() => setEditing("new")}>
              <Plus size={16} />
              {t("New Profile")}
            </Button>
          </>
        }
      />
      {notice && <div className="inline-notice">{notice}</div>}
      {state.data.profiles.length === 0 ? (
        <Empty title={t("No Profiles")} />
      ) : (
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>{t("Profile")}</th>
                <th>{t("Revision")}</th>
                <th>{t("Skills")}</th>
                <th>{t("MCP servers")}</th>
                <th>{t("Updated")}</th>
                <th aria-label={t("Actions")} />
              </tr>
            </thead>
            <tbody>
              {state.data.profiles.map((profile) => {
                const archived = Boolean(profile.archivedAt);
                return (
                <tr
                  key={profile.id}
                  className={selected?.id === profile.id ? "selected-row" : ""}
                  onClick={() => setSelected(profile)}
                >
                  <td>
                    <strong>{profile.name}</strong>
                    <small>{profile.description || "—"}</small>
                    <Status
                      value={
                        profile.publishedRevisionId &&
                        profile.publishedRevisionId === profile.currentRevisionId
                          ? "Published"
                          : "Draft"
                      }
                    />
                    {archived && <Status value="archived" />}
                  </td>
                  <td>{profile.revision}</td>
                  <td>{profile.skillIds.length}</td>
                  <td>{profile.mcpServerIds.length}</td>
                  <td>{new Date(profile.updatedAt).toLocaleString()}</td>
                  <td className="row-actions">
                    {!archived && !profile.pendingBindings && <IconButton
                      label={t("Edit")}
                      onClick={(event) => {
                        event.stopPropagation();
                        setEditing(profile);
                      }}
                    >
                      <Pencil size={16} />
                    </IconButton>}
                    {!archived && !profile.pendingBindings && <IconButton
                      label={t("Preflight and Apply")}
                      onClick={(event) => {
                        event.stopPropagation();
                        setApplying(profile);
                      }}
                    >
                      <Play size={16} />
                    </IconButton>}
                    {archived ? <>
                      <IconButton
                        label={t("Restore")}
                        onClick={(event) => {
                          event.stopPropagation();
                          api
                            .post(`/profiles/${profile.id}/restore`, {
                              revision: profile.revision,
                            })
                            .then(state.reload)
                            .catch((reason: Error) => setNotice(reason.message));
                        }}
                      >
                        <RefreshCw size={16} />
                      </IconButton>
                      <IconButton
                        label={t("Purge")}
                        onClick={(event) => {
                          event.stopPropagation();
                          if (!confirm(`${t("Purge")} ${profile.name}?`)) return;
                          api
                            .post(`/profiles/${profile.id}/purge`)
                            .then(() => {
                              if (selected?.id === profile.id) setSelected(null);
                              state.reload();
                            })
                            .catch((reason: Error) => setNotice(reason.message));
                        }}
                      >
                        <Trash2 size={16} />
                      </IconButton>
                    </> : <IconButton
                      label={t("Archive")}
                      onClick={(event) => {
                        event.stopPropagation();
                        remove(profile);
                      }}
                    >
                      <Archive size={16} />
                    </IconButton>}
                  </td>
                </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {selectedProfile && (
        <ProfileDetail
          profile={selectedProfile}
          history={history}
          setHistory={setHistory}
          refresh={reloadSelected}
          edit={() => setEditing(selectedProfile)}
          apply={() => setApplying(selectedProfile)}
          launch={() => setLaunching(selectedProfile)}
          exportBundle={exportBundle}
          restore={() => {
            api
              .post(`/profiles/${selectedProfile.id}/restore`, {
                revision: selectedProfile.revision,
              })
              .then(state.reload)
              .catch((reason: Error) => setNotice(reason.message));
          }}
          purge={() => {
            if (!confirm(`${t("Purge")} ${selectedProfile.name}?`)) return;
            api
              .post(`/profiles/${selectedProfile.id}/purge`)
              .then(() => {
                setSelected(null);
                state.reload();
              })
              .catch((reason: Error) => setNotice(reason.message));
          }}
          clone={() => {
            const name = prompt(t("Clone name"), `${selectedProfile.name} copy`);
            if (name)
              api
                .post(`/profiles/${selectedProfile.id}/clone`, { name })
                .then(state.reload)
                .catch((reason: Error) => setNotice(reason.message));
          }}
        />
      )}
      {editing && (
        <ProfileGovernanceEditor
          profile={editing === "new" ? undefined : editing}
          skills={state.data.skills}
          servers={state.data.servers}
          contracts={state.data.contracts}
          close={() => setEditing(null)}
          saved={(profile) => {
            setEditing(null);
            setSelected(profile);
            state.reload();
          }}
        />
      )}
      {applying && (
        <ProfileApply
          profile={applying}
          targets={state.data.targets}
          close={() => setApplying(null)}
          queued={(operation) => {
            setApplying(null);
            setNotice(`${t("Apply queued")} · ${operation.id.slice(0, 8)}`);
            state.reload();
          }}
        />
      )}
      {launching && (
        <ProfileLaunchDialog
          profile={launching}
          close={() => setLaunching(null)}
        />
      )}
      {bundlePreview && (
        <BundlePreviewDialog
          preview={bundlePreview}
          close={() => {
            setBundlePreview(null);
            setBundleFile(null);
          }}
          confirm={importBundle}
        />
      )}
      {secretExportProfile && (
        <SecretExportDialog
          profile={secretExportProfile}
          close={() => setSecretExportProfile(null)}
          exportBundle={(password) => {
            const profile = secretExportProfile;
            setSecretExportProfile(null);
            api
              .download(`/profiles/${profile.id}/bundle/export-secrets`, {
                currentPassword: password,
                originLabel: "ToolHub",
              })
              .then((blob) => {
                const url = URL.createObjectURL(blob);
                const anchor = document.createElement("a");
                anchor.href = url;
                anchor.download = `${profile.name}.toolhub-profile-secrets`;
                anchor.click();
                URL.revokeObjectURL(url);
              })
              .catch((reason: Error) => setNotice(reason.message));
          }}
        />
      )}
    </>
  );
}

function ProfileDetail({
  profile,
  history,
  setHistory,
  refresh,
  edit,
  apply,
  launch,
  restore,
  purge,
  clone,
  exportBundle,
}: {
  profile: Profile;
  history: ProfileRevision[] | null;
  setHistory: (value: ProfileRevision[] | null) => void;
  refresh: () => void;
  edit: () => void;
  apply: () => void;
  launch: () => void;
  restore: () => void;
  purge: () => void;
  clone: () => void;
  exportBundle: (profile: Profile, secrets: boolean) => void;
}) {
  const { t } = useI18n();
  const loadHistory = () =>
    api
      .list<ProfileRevision>(`/profiles/${profile.id}/history`)
      .then((value) => setHistory(value.items))
      .catch(() => setHistory([]));
  const refreshProfile = () =>
    api
      .post(`/profiles/${profile.id}/refresh`, { revision: profile.revision })
      .then(refresh);
  const archived = Boolean(profile.archivedAt);
  return (
    <section className="target-band profile-detail">
      <header>
        <div>
          <h2>{profile.name}</h2>
          <small>
            {profile.description || "—"} · {t("Revision")} {profile.revision} ·{" "}
            <code>{profile.canonicalHash?.slice(0, 12) || "—"}</code>
          </small>
        </div>
        <div className="row-actions">
          {!archived && !profile.pendingBindings && <Button variant="secondary" onClick={edit}>
            <Pencil size={15} />
            {t("Edit")}
          </Button>}
          {!archived && <Button onClick={apply} disabled={profile.pendingBindings}>
            <Play size={15} />
            {t("Apply")}
          </Button>}
          {!archived && (profile.clientKind === "claude" || profile.clientKind === "codex") && <Button variant="secondary" onClick={launch} disabled={profile.pendingBindings}>
            <Play size={15} />
            {t("Launch session")}
          </Button>}
          {!archived && !profile.pendingBindings && <Button variant="secondary" onClick={refreshProfile}>
            <RefreshCw size={15} />
            {t("Refresh")}
          </Button>}
          {archived && <>
            <Button variant="secondary" onClick={restore}>
              <RefreshCw size={15} />
              {t("Restore")}
            </Button>
            <Button variant="danger" onClick={purge}>
              <Trash2 size={15} />
              {t("Purge")}
            </Button>
          </>}
          <Button variant="secondary" onClick={clone} disabled={profile.pendingBindings}>
            <UsersRound size={15} />
            {t("Clone")}
          </Button>
          <IconButton label={t("History")} onClick={loadHistory}>
            <History size={16} />
          </IconButton>
          <IconButton
            label={t("Export Bundle")}
            onClick={() => exportBundle(profile, false)}
          >
            <Download size={16} />
          </IconButton>
          {!archived && !profile.pendingBindings && <IconButton
            label={t("Export Secret Bundle")}
            onClick={() => exportBundle(profile, true)}
          >
            <KeyRound size={16} />
          </IconButton>}
        </div>
      </header>
      <div className="detail-grid">
        <div>
          <h3>{t("Pinned Skills")}</h3>
          {profile.skills?.map((item) => (
            <div className="detail-row" key={item.skillId}>
              <strong>{item.name}</strong>
              <code>{item.versionId.slice(0, 12)}</code>
              <Status value={item.current ? "current" : "historical"} />
            </div>
          )) || <span>—</span>}
        </div>
        <div>
          <h3>{t("Pinned MCP revisions")}</h3>
          {profile.mcpServers?.map((item) => (
            <div className="detail-row" key={item.serverId}>
              <strong>{item.name}</strong>
              <code>r{item.revision}</code>
              <Status value={item.current ? "current" : "historical"} />
            </div>
          )) || <span>—</span>}
        </div>
      </div>
      <div className="profile-state-strip">
        <div>
          <span>{t("Revision state")}</span>
          <Status
            value={
              profile.publishedRevisionId &&
              profile.publishedRevisionId === profile.currentRevisionId
                ? "Published"
                : "Draft"
            }
          />
        </div>
        <div>
          <span>{t("Client")}</span>
          <strong>{profile.clientKind || "—"}</strong>
        </div>
        <div>
          <span>{t("Effective visibility")}</span>
          <strong>{profile.effectiveVisibleCount ?? 0}</strong>
        </div>
      </div>
      {profile.pendingBindings && archived && (
        <div className="inline-notice">
          {t("Restore this archived Profile before completing secret bindings.")}
        </div>
      )}
      {profile.pendingBindings && !archived && (
        <PendingBindingsForm profile={profile} completed={refresh} />
      )}
      {history && (
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>{t("Revision")}</th>
                <th>{t("Created")}</th>
                <th>{t("Hash")}</th>
                <th>{t("Bindings")}</th>
              </tr>
            </thead>
            <tbody>
              {history.map((item) => (
                <tr key={item.id}>
                  <td>{item.revision}</td>
                  <td>{new Date(item.createdAt).toLocaleString()}</td>
                  <td>
                    <code>{item.canonicalHash?.slice(0, 16) || "—"}</code>
                  </td>
                  <td>{item.pendingBindings ? t("Pending") : t("Bound")}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function PendingBindingsForm({
  profile,
  completed,
}: {
  profile: Profile;
  completed: () => void;
}) {
  const { t } = useI18n();
  const state = useData(
    () =>
      api.list<PendingSecretBinding>(
        `/profiles/${profile.id}/secret-bindings`,
      ),
    [profile.id, profile.revision],
  );
  const [values, setValues] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  if (state.loading) return <Loading />;
  if (state.error || !state.data)
    return <ErrorNotice message={state.error} retry={state.reload} />;
  const items = state.data.items.filter((item) => !item.bound);
  if (items.length === 0) return null;
  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    setError("");
    if (items.some((item) => !values[item.slotHash])) {
      setError(t("Enter a value for every pending secret binding."));
      return;
    }
    setBusy(true);
    api
      .post(`/profiles/${profile.id}/secret-bindings`, {
        revision: profile.revision,
        values,
      })
      .then(() => {
        setValues({});
        completed();
      })
      .catch((reason: Error) => setError(reason.message))
      .finally(() => setBusy(false));
  };
  return (
    <form className="pending-bindings" onSubmit={submit}>
      <header>
        <div>
          <h3>{t("Complete secret bindings")}</h3>
          <small>{t("Values are write-only and encrypted immediately.")}</small>
        </div>
        <Status value="pending" />
      </header>
      {error && <ErrorNotice message={error} />}
      <div className="detail-grid">
        {items.map((item) => (
          <Field
            key={item.slotHash}
            label={`${item.namespace} · ${item.key}`}
          >
            <input
              type="password"
              autoComplete="new-password"
              value={values[item.slotHash] ?? ""}
              onChange={(event) =>
                setValues((current) => ({
                  ...current,
                  [item.slotHash]: event.target.value,
                }))
              }
            />
          </Field>
        ))}
      </div>
      <div className="modal-actions">
        <Button type="submit" disabled={busy}>
          {busy ? t("Saving") : t("Save secret bindings")}
        </Button>
      </div>
    </form>
  );
}

function SecretExportDialog({
  profile,
  close,
  exportBundle,
}: {
  profile: Profile;
  close: () => void;
  exportBundle: (password: string) => void;
}) {
  const { t } = useI18n();
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    if (!password) {
      setError(t("Current password is required."));
      return;
    }
    setBusy(true);
    setError("");
    exportBundle(password);
  };
  return (
    <Modal title={`${t("Export Secret Bundle")} · ${profile.name}`} close={close}>
      <form className="modal-form" onSubmit={submit}>
        <div className="inline-notice warning-notice">
          <KeyRound size={16} />
          {t("This bundle contains plaintext secrets. Store it securely and delete it after use.")}
        </div>
        {error && <ErrorNotice message={error} />}
        <Field label={t("Current password")}>
          <input
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
          />
        </Field>
        <div className="modal-actions">
          <Button type="button" variant="secondary" onClick={close}>
            {t("Cancel")}
          </Button>
          <Button type="submit" disabled={busy}>
            <Download size={15} />
            {t("Export")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function BundlePreviewDialog({
  preview,
  close,
  confirm,
}: {
  preview: BundlePreview;
  close: () => void;
  confirm: () => void;
}) {
  const { t } = useI18n();
  return (
    <Modal
      title={`${t("Bundle Preview")} · ${preview.profileName}`}
      close={close}
    >
      <div className="detail-list">
        <div>
          <dt>{t("Decision")}</dt>
          <dd>
            {preview.duplicate
              ? t("Exact duplicate")
              : preview.requiresRename
                ? t("New name required")
                : preview.updateExisting
                  ? t("Update existing revision")
                  : t("New Profile")}
          </dd>
        </div>
        <div>
          <dt>{t("Components")}</dt>
          <dd>
            {
              preview.components.filter((item) => item.decision === "Reuse")
                .length
            }{" "}
            {t("Reuse")} ·{" "}
            {
              preview.components.filter((item) => item.decision !== "Reuse")
                .length
            }{" "}
            {t("New")}
          </dd>
        </div>
        <div>
          <dt>{t("Pending bindings")}</dt>
          <dd>{preview.pendingBindings}</dd>
        </div>
      </div>
      <div className="table-scroll">
        <table>
          <thead>
            <tr>
              <th>{t("Kind")}</th>
              <th>{t("Name")}</th>
              <th>{t("Decision")}</th>
            </tr>
          </thead>
          <tbody>
            {preview.components.map((item) => (
              <tr key={`${item.kind}:${item.name}`}>
                <td>{item.kind}</td>
                <td>{item.name}</td>
                <td>
                  <Status value={item.decision} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="modal-actions">
        <Button variant="secondary" onClick={close}>
          {t("Cancel")}
        </Button>
        <Button onClick={confirm}>
          <Upload size={15} />
          {t("Confirm import")}
        </Button>
      </div>
    </Modal>
  );
}

function ProfileApply({
  profile,
  targets,
  close,
  queued,
}: {
  profile: Profile;
  targets: Target[];
  close: () => void;
  queued: (operation: Operation) => void;
}) {
  const { t } = useI18n();
  const available = targets.filter((target) => target.writable);
  const [selected, setSelected] = useState(new Set<string>());
  const [items, setItems] = useState<PreflightItem[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const toggle = (id: string) => {
    const next = new Set(selected);
    next.has(id) ? next.delete(id) : next.add(id);
    setSelected(next);
  };
  const preflight = () => {
    setBusy(true);
    setError("");
    api
      .post<{ items: PreflightItem[] }>(`/profiles/${profile.id}/preflight`, {
        targetIds: [...selected],
      })
      .then((value) => setItems(value.items))
      .catch((reason: Error) => setError(reason.message))
      .finally(() => setBusy(false));
  };
  const apply = () => {
    if (!items) return;
    setBusy(true);
    api
      .post<Operation>(`/profiles/${profile.id}/apply`, {
        confirmationTokens: items.map((item) => item.confirmationToken),
      })
      .then(queued)
      .catch((reason: Error) => setError(reason.message))
      .finally(() => setBusy(false));
  };
  return (
    <Modal title={`${t("Apply Profile")} · ${profile.name}`} close={close}>
      {error && <ErrorNotice message={error} />}
      {items ? (
        <div className="preflight-list">
          {items.map((item) => {
            const target = targets.find(
              (candidate) => candidate.id === item.targetId,
            );
            const diff = item.result.diff;
            return (
              <section key={item.targetId}>
                <header>
                  <span>
                    <strong>{target?.targetKey}</strong>
                    <small>
                      {t("Expires")}{" "}
                      {new Date(item.expiresAt).toLocaleTimeString()}
                    </small>
                  </span>
                  <Status value="ready" />
                </header>
                <div className="diff-summary">
                  <span className="add">+{diff.add.length}</span>
                  <span className="replace">~{diff.replace.length}</span>
                  <span className="delete">-{diff.delete.length}</span>
                  <span>
                    {diff.excluded.length} {t("excluded")}
                  </span>
                </div>
                <div className="preflight-diff-items">
                  {[
                    { label: t("Add"), marker: "+", items: diff.add, className: "add" },
                    { label: t("Replace"), marker: "~", items: diff.replace, className: "replace" },
                    { label: t("Delete"), marker: "−", items: diff.delete, className: "delete" },
                    { label: t("Excluded"), marker: "!", items: diff.excluded, className: "excluded" },
                  ].map((group) =>
                    group.items.length > 0 ? (
                      <div className={`preflight-diff-group ${group.className}`} key={group.label}>
                        <strong>{group.label}</strong>
                        <ul>
                          {group.items.map((entry, index) => (
                            <li key={`${entry.kind}:${entry.name}:${index}`}>
                              <b>{group.marker}</b>
                              {entry.kind} / {entry.name}
                              {entry.reason ? ` (${entry.reason})` : ""}
                            </li>
                          ))}
                        </ul>
                      </div>
                    ) : null,
                  )}
                </div>
              </section>
            );
          })}
        </div>
      ) : (
        <div className="target-checklist">
          {available.map((target) => (
            <label key={target.id}>
              <input
                type="checkbox"
                checked={selected.has(target.id)}
                onChange={() => toggle(target.id)}
              />
              <span>
                <strong>{target.targetKey}</strong>
                <small>
                  {target.nodeName} / {target.runtime}
                </small>
              </span>
              <Status value={target.health} />
            </label>
          ))}
        </div>
      )}
      <div className="modal-actions">
        <Button variant="secondary" onClick={close}>
          {t("Cancel")}
        </Button>
        {items ? (
          <Button disabled={busy} onClick={apply}>
            <Play size={16} />
            {t("Confirm Apply")}
          </Button>
        ) : (
          <Button disabled={busy || selected.size === 0} onClick={preflight}>
            <Eye size={16} />
            {t("Run preflight")}
          </Button>
        )}
      </div>
    </Modal>
  );
}

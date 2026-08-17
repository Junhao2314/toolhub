import { useState } from "react";
import { api, type Profile, type ProfileClientKind, type Skill } from "../../api/client";
import { Button, ErrorNotice, Field, Modal } from "../../components/ui";
import { useI18n } from "../../i18n";

/**
 * Profiles intentionally contain Skills only. MCP configuration belongs to
 * the MCP/Shared relay editor, where the browser updates mcpm's one shared
 * configuration through the durable relay operation.
 */
export default function ProfileGovernanceEditor({
  profile,
  skills,
  close,
  saved,
}: {
  profile?: Profile;
  skills: Skill[];
  // Kept optional so old callers can be upgraded without a second modal API.
  servers?: unknown[];
  contracts?: unknown;
  close: () => void;
  saved: (profile: Profile) => void;
}) {
  const { t } = useI18n();
  const [name, setName] = useState(profile?.name ?? "");
  const [description, setDescription] = useState(profile?.description ?? "");
  const [clientKind, setClientKind] = useState<ProfileClientKind>(profile?.clientKind ?? "claude");
  const [category, setCategory] = useState(profile?.category ?? "coding");
  const [variant, setVariant] = useState(profile?.variant ?? "standard");
  const [skillIds, setSkillIds] = useState(() => new Set(profile?.skillIds ?? []));
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const toggleSkill = (id: string) => {
    setSkillIds((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    const selected = skills.filter((skill) => skillIds.has(skill.id));
    const payload = {
      name: name.trim(),
      description: description.trim(),
      clientKind,
      category: category.trim(),
      variant: variant.trim(),
      migrationState: profile?.migrationState ?? "ready",
      revision: profile?.revision ?? 0,
      skillIds: selected.map((skill) => skill.id),
      skillVersionIds: Object.fromEntries(
        selected.map((skill) => [
          skill.id,
          profile?.skills?.find((pin) => pin.skillId === skill.id)?.versionId ??
            skill.currentVersionId,
        ]),
      ),
    };
    const request = profile
      ? api.put<Profile>(`/profiles/${profile.id}`, payload)
      : api.post<Profile>("/profiles", payload);
    request
      .then(saved)
      .catch((reason: Error) => setError(reason.message))
      .finally(() => setBusy(false));
  };

  return (
    <Modal
      title={profile ? `${t("Edit")} · ${profile.name}` : t("New Profile")}
      close={close}
    >
      <form className="profile-governance-editor" onSubmit={submit}>
        {error && <ErrorNotice message={error} />}
        <div className="profile-form-grid">
          <Field label={t("Name")}>
            <input value={name} onChange={(event) => setName(event.target.value)} required />
          </Field>
          <Field label={t("Client")}>
            <select value={clientKind} onChange={(event) => setClientKind(event.target.value as ProfileClientKind)}>
              <option value="claude">Claude</option>
              <option value="codex">Codex</option>
              <option value="shared">{t("Shared")}</option>
              <option value="unknown">{t("Unknown")}</option>
            </select>
          </Field>
          <Field label={t("Category")}>
            <input value={category} onChange={(event) => setCategory(event.target.value)} />
          </Field>
          <Field label={t("Variant")}>
            <input value={variant} onChange={(event) => setVariant(event.target.value)} />
          </Field>
          <Field label={t("Description")}>
            <input value={description} onChange={(event) => setDescription(event.target.value)} />
          </Field>
        </div>
        <section className="profile-editor-section">
          <header>
            <h3>{t("Skills")}</h3>
            <small>{t("MCP configuration is managed in the Shared relay editor")}</small>
          </header>
          {skills.length === 0 ? (
            <p>{t("No Skills in Library")}</p>
          ) : (
            <div className="profile-server-list">
              {skills.map((skill) => (
                <label key={skill.id} className="selection-row">
                  <input
                    type="checkbox"
                    checked={skillIds.has(skill.id)}
                    onChange={() => toggleSkill(skill.id)}
                  />
                  <span>
                    <strong>{skill.name}</strong>
                    <small>
                      {skill.slug} · {skill.currentVersionId.slice(0, 12)}
                    </small>
                  </span>
                </label>
              ))}
            </div>
          )}
        </section>
        <footer className="modal-actions">
          <Button type="button" variant="secondary" onClick={close}>
            {t("Cancel")}
          </Button>
          <Button type="submit" disabled={busy}>
            {busy ? t("Saving") : t("Save")}
          </Button>
        </footer>
      </form>
    </Modal>
  );
}

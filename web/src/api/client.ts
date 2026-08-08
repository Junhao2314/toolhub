export type Dict = Record<string, unknown>;

export interface AccountUser {
  username: string;
  passwordChangeRecommended: boolean;
  passwordChangedAt?: string;
  createdAt?: string;
  updatedAt?: string;
}

export interface Session {
  user: AccountUser;
  csrfToken?: string;
  expiresAt?: string;
}

export interface Node {
  id: string;
  name: string;
  kind: "local" | "salt";
  saltMinionId?: string;
  managedUsernameOverride?: string | null;
  status: "online" | "unavailable";
  saltVersion?: string;
  lastSeenAt?: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface Skill {
  id: string;
  slug: string;
  name: string;
  description: string;
  sourceKind: string;
  sourceUrl?: string;
  sourceCommit?: string;
  currentVersionId: string;
  currentSha256: string;
  currentContentHash: string;
  manifest: Dict;
  scanReport: Dict;
  createdAt: string;
  updatedAt: string;
}

export interface MCPServer {
  id: string;
  name: string;
  description?: string;
  revision: number;
  transport: "stdio" | "http" | "sse";
  command?: string;
  args: string[];
  url?: string;
  envKeys: string[];
  headerKeys: string[];
  contentHash: string;
  createdAt: string;
  updatedAt: string;
}

export interface Profile {
  id: string;
  currentRevisionId?: string;
  name: string;
  description?: string;
  revision: number;
  canonicalHash?: string;
  pendingBindings?: boolean;
  archivedAt?: string | null;
  skillIds: string[];
  mcpServerIds: string[];
  skills?: ProfileSkillPin[];
  mcpServers?: ProfileMCPPin[];
  createdAt: string;
  updatedAt: string;
}

export interface ProfileSkillPin {
  skillId: string;
  versionId: string;
  slug: string;
  name: string;
  sha256: string;
  contentHash: string;
  current: boolean;
}
export interface ProfileMCPPin {
  serverId: string;
  revisionId: string;
  revision: number;
  name: string;
  description?: string;
  transport: string;
  command?: string;
  args?: string[];
  url?: string;
  envKeys: string[];
  headerKeys: string[];
  contentHash: string;
  current: boolean;
}
export interface PendingSecretBinding {
  profileRevisionId: string;
  mcpRevisionId: string;
  namespace: "env" | "header" | string;
  key: string;
  slotHash: string;
  bound: boolean;
  boundAt?: string;
}
export interface ProfileRevision extends Profile {
  profileId: string;
  archivedRestore?: boolean;
  createdAt: string;
}
export interface BundleComponentDecision {
  kind: string;
  name: string;
  hash: string;
  decision: string;
}
export interface BundlePreview {
  bundleHash: string;
  kind: string;
  profileName: string;
  canonicalHash: string;
  origin: { label: string; exportedAt: string };
  components: BundleComponentDecision[];
  duplicate: boolean;
  duplicateReason?: string;
  existingProfileId?: string;
  requiresRename: boolean;
  suggestedName?: string;
  updateExisting: boolean;
  pendingBindings: number;
  confirmationToken: string;
  expiresAt: string;
}

export interface Target {
  id: string;
  targetKey: string;
  nodeId: string;
  nodeName: string;
  nodeKind: "local" | "salt";
  saltMinionId?: string;
  runtime: "claude" | "codex" | "hermes" | "shared-relay";
  managedUsername: string;
  writable: boolean;
  health: string;
  desiredRevision: number;
  targetRevision?: string;
  driftSummary?: Dict;
  lastScannedAt?: string;
  lastReconciledAt?: string;
  errorCode?: string;
  errorReason?: string;
  relayFailureCount: number;
  relayNextRetryAt?: string;
  relaySuspended: boolean;
  relayLastMemberCheckAt?: string;
  relayMemberStatuses: RelayMemberStatus[];
}

export interface RelayCapabilityCounts {
  tools: number;
  resources: number;
  resourceTemplates: number;
  prompts: number;
}

export interface RelayMemberStatus {
  memberId: string;
  name: string;
  status: "ready" | "unavailable";
  capabilityKinds: Array<
    "tools" | "resources" | "resourceTemplates" | "prompts"
  >;
  capabilities: RelayCapabilityCounts;
  checkedAt: string;
  errorCode?: string;
  errorReason?: string;
}

export interface RelayStatus {
  state: string;
  healthy: boolean;
  intentionalPaused: boolean;
  endpoint: string;
  fixedPort: number;
  systemdEnabled: boolean;
  version?: string;
  contract?: "verified" | "incompatible" | "unavailable";
  memberStatuses?: RelayMemberStatus[];
  errorCode?: string;
  errorReason?: string;
}

export interface InventoryMember {
  id: string;
  kind: string;
  name: string;
  contentHash?: string;
  protected: boolean;
  scope?: string;
  revision?: number;
  secretKeys?: string[];
  slug?: string;
  description?: string;
  source?: string;
  provider?: string;
  category?: string;
  trust?: string;
  importable?: boolean;
  eligibilityReason?: string;
  shadowed?: boolean;
  builtin?: boolean;
}

export interface DesiredManifest {
  schemaVersion: number;
  target: {
    id: string;
    nodeId: string;
    nodeKind: string;
    saltMinionId?: string;
    runtime: string;
    managedUsername: string;
  };
  profileId?: string;
  profileRevision?: number;
  skills: Array<{
    memberId: string;
    skillId: string;
    versionId: string;
    slug: string;
    sha256: string;
    contentHash: string;
  }>;
  mcpServers: Array<{
    memberId: string;
    serverId: string;
    revision: number;
    name: string;
    transport: string;
    command?: string;
    args?: string[];
    url?: string;
    envRefs?: Record<string, string>;
    headerRefs?: Record<string, string>;
    contentHash: string;
  }>;
  managedMemberIds: string[];
  relayPort?: number;
}

export interface TargetDetail {
  target: Target;
  targetRevision: string;
  inventory: { members?: InventoryMember[]; relay?: RelayStatus };
  desired?: {
    snapshot: {
      id: string;
      revision: number;
      sourceKind: string;
      profileRevision?: number;
      manifestHash: string;
      createdAt: string;
    };
    manifest: DesiredManifest;
  };
}

export interface OperationTarget {
  id: string;
  targetId: string;
  targetKey: string;
  status: string;
  attempt: number;
  pendingRerun: boolean;
  bridgeOperationId?: string;
  saltJid?: string;
  result?: Dict;
  errorCode?: string;
  errorReason?: string;
  createdAt?: string;
  startedAt?: string;
  finishedAt?: string;
  updatedAt?: string;
}

export interface Operation {
  id: string;
  kind: string;
  status: string;
  sourceId?: string;
  metadata: Dict;
  errorCode?: string;
  errorReason?: string;
  cancelRequested: boolean;
  createdAt: string;
  startedAt?: string;
  finishedAt?: string;
  updatedAt: string;
  targets?: OperationTarget[];
}

export interface Backup {
  id: string;
  bridgeBackupId: string;
  targetId: string;
  sourceOperationId?: string;
  targetRevision: string;
  manifestHash?: string;
  createdAt: string;
  expiresAt: string;
}

export interface LocalMCPServerPreview {
  name: string;
  transport: "stdio" | "http" | "sse";
  command?: string;
  args: string[];
  url?: string;
  envKeys: string[];
  headerKeys: string[];
  contentHash: string;
  confirmationToken: string;
  expiresAt: string;
}

export interface LocalMCPImportPreflight {
  targetRevision: string;
  items: LocalMCPServerPreview[];
}

export interface Settings {
  managedUsername: string;
  updateCron: string;
  timezone: string;
  relayPort: number;
  relayIntentionalPaused: boolean;
  updatedAt: string;
}

export class APIError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public details: Dict = {},
  ) {
    super(message);
    this.name = "APIError";
  }
}

class ToolHubClient {
  private csrf = sessionStorage.getItem("toolhub.csrf") ?? "";

  async request<T>(path: string, init: RequestInit = {}): Promise<T> {
    const headers = new Headers(init.headers);
    const method = (init.method ?? "GET").toUpperCase();
    if (init.body && !(init.body instanceof FormData))
      headers.set("Content-Type", "application/json");
    if (this.csrf && !["GET", "HEAD", "OPTIONS"].includes(method))
      headers.set("X-CSRF-Token", this.csrf);
    if (
      !["GET", "HEAD", "OPTIONS"].includes(method) &&
      !headers.has("Idempotency-Key")
    )
      headers.set("Idempotency-Key", crypto.randomUUID());
    const response = await fetch(`/api/v1${path}`, {
      ...init,
      method,
      headers,
      credentials: "same-origin",
    });
    if (response.status === 204) return undefined as T;
    const payload = (await response.json().catch(() => ({}))) as Dict;
    if (!response.ok) {
      const error = (payload.error ?? {}) as Dict;
      throw new APIError(
        response.status,
        String(error.code ?? "request_failed"),
        String(error.message ?? `HTTP ${response.status}`),
        error,
      );
    }
    return payload as T;
  }

  async login(username: string, password: string): Promise<Session> {
    const session = await this.request<Session>("/auth/login", {
      method: "POST",
      body: JSON.stringify({ username, password }),
    });
    this.setCSRF(session.csrfToken ?? "");
    return session;
  }

  async bootstrap(): Promise<Session> {
    const session = await this.request<Session & { authenticated: boolean }>(
      "/auth/session",
    );
    if (!session.authenticated)
      throw new APIError(401, "unauthenticated", "Authentication is required");
    this.setCSRF(session.csrfToken ?? "");
    return session;
  }

  async logout(): Promise<void> {
    try {
      await this.request("/auth/logout", { method: "POST" });
    } finally {
      this.setCSRF("");
    }
  }

  async updateOwnUsername(
    username: string,
    currentPassword: string,
  ): Promise<void> {
    await this.patch("/account/username", { username, currentPassword });
    this.setCSRF("");
  }

  async updateOwnPassword(
    currentPassword: string,
    newPassword: string,
  ): Promise<void> {
    await this.patch("/account/password", { currentPassword, newPassword });
    this.setCSRF("");
  }

  forgetSession() {
    this.setCSRF("");
  }
  list<T>(path: string): Promise<{ items: T[] }> {
    return this.request(path);
  }
  get<T>(path: string): Promise<T> {
    return this.request(path);
  }
  post<T>(path: string, body: unknown = {}): Promise<T> {
    return this.request(path, { method: "POST", body: JSON.stringify(body) });
  }
  postForm<T>(path: string, form: FormData): Promise<T> {
    return this.request(path, { method: "POST", body: form });
  }
  put<T>(path: string, body: unknown): Promise<T> {
    return this.request(path, { method: "PUT", body: JSON.stringify(body) });
  }
  patch<T>(path: string, body: unknown): Promise<T> {
    return this.request(path, { method: "PATCH", body: JSON.stringify(body) });
  }
  delete(path: string): Promise<void> {
    return this.request(path, { method: "DELETE" });
  }

  async uploadSkill(file: File): Promise<Skill> {
    const form = new FormData();
    form.append("file", file);
    return this.request("/skills/upload", { method: "POST", body: form });
  }

  async download(path: string, body: unknown = {}): Promise<Blob> {
    const headers = new Headers();
    headers.set("Content-Type", "application/json");
    if (this.csrf) headers.set("X-CSRF-Token", this.csrf);
    headers.set("Idempotency-Key", crypto.randomUUID());
    const response = await fetch(`/api/v1${path}`, {
      method: "POST",
      headers,
      credentials: "same-origin",
      body: JSON.stringify(body),
    });
    if (!response.ok) {
      const payload = (await response.json().catch(() => ({}))) as Dict;
      const error = (payload.error ?? {}) as Dict;
      throw new APIError(
        response.status,
        String(error.code ?? "request_failed"),
        String(error.message ?? `HTTP ${response.status}`),
        error,
      );
    }
    return response.blob();
  }

  async previewBundle(file: File): Promise<BundlePreview> {
    const form = new FormData();
    form.append("bundle", file);
    return this.postForm<BundlePreview>("/profiles/bundles/preview", form);
  }

  async importBundle(
    file: File,
    fields: {
      confirmationToken: string;
      name?: string;
      confirmDuplicate?: boolean;
      importAsNew?: boolean;
    },
  ): Promise<Profile> {
    const form = new FormData();
    form.append("bundle", file);
    for (const [key, value] of Object.entries(fields))
      if (value !== undefined) form.append(key, String(value));
    return this.postForm<Profile>("/profiles/bundles/import", form);
  }

  private setCSRF(value: string) {
    this.csrf = value;
    if (value) sessionStorage.setItem("toolhub.csrf", value);
    else sessionStorage.removeItem("toolhub.csrf");
  }
}

export const api = new ToolHubClient();

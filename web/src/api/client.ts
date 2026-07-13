import type {
  AccountInput,
  AccountSummary,
  Attachment,
  ApiErrorEnvelope,
  AuthSession,
  ConversationDetail,
  ConversationQuery,
  ConversationSummary,
  Draft,
  DraftInput,
  ListEnvelope,
  LoginChallenge,
  Mailbox,
  MessageBody,
  MessageDetail,
  Operation,
  OperationKind,
  OutboxStatus,
  OAuthProviderSetting,
  OAuthStartInput,
  SetupEnrollment,
  SetupStatus,
  SystemStatus,
  SystemUpdateStatus,
} from "../types";
import { createDemoApi } from "../demo/api";

export class ApiError extends Error {
  constructor(
    public readonly code: string,
    message: string,
    public readonly requestId?: string,
    public readonly status?: number,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

export interface MailApi {
  getSetupStatus(): Promise<SetupStatus>;
  beginSetup(input: { token: string; username: string }): Promise<SetupEnrollment>;
  completeSetup(input: { token: string; username: string; password: string; totp_code: string }): Promise<{ recovery_codes: string[] }>;
  getSession(): Promise<AuthSession>;
  login(input: { username: string; password: string }): Promise<LoginChallenge>;
  verifyTotp(input: { challenge_token: string; code: string }): Promise<AuthSession>;
  logout(): Promise<void>;
  getSystemStatus(): Promise<SystemStatus>;
  getSystemUpdate(): Promise<SystemUpdateStatus>;
  checkSystemUpdate(): Promise<SystemUpdateStatus>;
  installSystemUpdate(version: string): Promise<SystemUpdateStatus>;
  checkReadiness(): Promise<boolean>;
  getAccounts(): Promise<ListEnvelope<AccountSummary>>;
  getOAuthSettings(): Promise<ListEnvelope<OAuthProviderSetting>>;
  configureOAuth(provider: OAuthProviderSetting["provider"], input: { client_id: string; client_secret: string }): Promise<OAuthProviderSetting>;
  startOAuth(provider: OAuthProviderSetting["provider"], input: OAuthStartInput): Promise<{ authorization_url: string; expires_at: string }>;
  createAccount(input: AccountInput): Promise<AccountSummary>;
  updateAccount(id: string, input: Partial<AccountInput>): Promise<AccountSummary>;
  deleteAccount(id: string): Promise<void>;
  testAccount(id: string): Promise<{ ok: boolean; message?: string }>;
  syncAccount(id: string): Promise<void>;
  getMailboxes(): Promise<ListEnvelope<Mailbox>>;
  updateFolderRole(id: string, role: NonNullable<Mailbox["source_role"]>): Promise<Mailbox>;
  getConversations(query: ConversationQuery): Promise<ListEnvelope<ConversationSummary>>;
  getConversation(id: string): Promise<ConversationDetail>;
  getMessageBody(id: string, allowRemoteImages?: boolean): Promise<MessageBody>;
  getDrafts(): Promise<ListEnvelope<Draft>>;
  getDraft(id: string): Promise<Draft>;
  saveDraft(input: DraftInput): Promise<Draft>;
  uploadDraftAttachment(id: string, file: File): Promise<Attachment>;
  removeDraftAttachment(draftId: string, attachmentId: string): Promise<void>;
  deleteDraft(id: string): Promise<void>;
  sendDraft(id: string): Promise<{ status: Draft["state"] }>;
  getOutboxStatus(id: string): Promise<OutboxStatus>;
  createOperation(input: { kind: OperationKind; conversation_ids: string[]; destination_mailbox_id?: string }): Promise<Operation>;
  undoOperation(id: string): Promise<Operation>;
  attachmentUrl(id: string): string;
}

let csrfToken: string | undefined;

function cookieValue(name: string): string | undefined {
  const prefix = `${encodeURIComponent(name)}=`;
  const item = document.cookie.split(";").map((value) => value.trim()).find((value) => value.startsWith(prefix));
  return item ? decodeURIComponent(item.slice(prefix.length)) : undefined;
}

function isErrorEnvelope(value: unknown): value is ApiErrorEnvelope {
  if (!value || typeof value !== "object") return false;
  const error = (value as { error?: unknown }).error;
  return !!error && typeof error === "object" && typeof (error as { code?: unknown }).code === "string";
}

function asList<T>(value: ListEnvelope<T> | T[]): ListEnvelope<T> {
  return Array.isArray(value) ? { items: value } : value;
}

type Raw = Record<string, any>;

function normalizeSetupStatus(raw: Raw): SetupStatus {
  return {
    setup_required: typeof raw.setup_required === "boolean" ? raw.setup_required : !raw.configured,
    expires_at: raw.expires_at ?? raw.token_expires_at,
  };
}

function normalizeAccount(raw: Raw): AccountSummary {
  const status = raw.status === "ready" ? "connected" : raw.status === "pending" ? "syncing" : raw.status;
  const defaultAuthType = raw.provider === "google" || raw.provider === "microsoft" ? "oauth2" : "password";
  return {
    id: raw.id,
    provider: raw.provider,
    name: raw.name ?? raw.display_name,
    email: raw.email,
    color: raw.color,
    status,
    auth_type: raw.auth_type ?? defaultAuthType,
    username: raw.username ?? raw.email,
    imap: {
      host: raw.imap?.host ?? "",
      port: raw.imap?.port ?? 0,
      tls_mode: raw.imap?.tls_mode === "starttls" ? "starttls" : "implicit",
    },
    smtp: {
      host: raw.smtp?.host ?? "",
      port: raw.smtp?.port ?? 0,
      tls_mode: raw.smtp?.tls_mode === "implicit" ? "implicit" : "starttls",
    },
    status_message: raw.status_message ?? raw.last_error_code,
    unread_count: raw.unread_count ?? 0,
    last_synced_at: raw.last_synced_at ?? raw.last_success_at ?? raw.lastSyncedAt,
    signature_html: raw.signature_html,
  };
}

function normalizeMailbox(raw: Raw): Mailbox {
  const role = raw.role === "other" || raw.role === "junk" || raw.role === "all" ? "custom" : raw.role;
  return {
    id: raw.id,
    account_id: raw.account_id,
    name: raw.name ?? raw.display_name ?? raw.remote_name,
    role,
    source_role: raw.role,
    role_source: raw.role_source,
    unread_count: raw.unread_count ?? 0,
    total_count: raw.total_count,
  };
}

function normalizeAttachment(raw: Raw): Attachment {
  return {
    id: raw.id,
    filename: raw.filename,
    content_type: raw.content_type,
    size: raw.size ?? raw.size_bytes ?? 0,
  };
}

function normalizeConversation(raw: Raw): ConversationSummary {
  return {
    id: raw.id,
    account_id: raw.account_id,
    account_ids: raw.account_ids,
    subject: raw.subject || "(无主题)",
    snippet: raw.snippet ?? raw.preview ?? "",
    participants: raw.participants ?? [],
    last_message_at: raw.last_message_at ?? raw.latest_at,
    unread: typeof raw.unread === "boolean" ? raw.unread : (raw.unread_count ?? 0) > 0,
    starred: !!raw.starred,
    has_attachments: !!raw.has_attachments,
    message_count: raw.message_count ?? 1,
    mailbox_role: raw.mailbox_role,
  };
}

function normalizeMessage(raw: Raw): MessageDetail {
  const from = Array.isArray(raw.from) ? raw.from[0] : raw.from;
  const replyTo = Array.isArray(raw.reply_to) ? raw.reply_to[0] : raw.reply_to;
  return {
    id: raw.id,
    conversation_id: raw.conversation_id,
    account_id: raw.account_id,
    from: from ?? { email: "" },
    to: raw.to ?? [],
    cc: raw.cc ?? [],
    reply_to: replyTo,
    subject: raw.subject || "(无主题)",
    sent_at: raw.sent_at ?? raw.received_at,
    received_at: raw.received_at,
    unread: typeof raw.unread === "boolean" ? raw.unread : !raw.seen,
    starred: !!raw.starred,
    snippet: raw.snippet ?? raw.preview,
    attachments: (raw.attachments ?? []).map(normalizeAttachment),
  };
}

function normalizeConversationDetail(raw: Raw): ConversationDetail {
  const messages = (raw.messages ?? []).map(normalizeMessage);
  if (raw.conversation) return { conversation: normalizeConversation(raw.conversation), messages };
  const latest = messages[messages.length - 1];
  return {
    conversation: normalizeConversation({
      id: raw.id,
      account_id: latest?.account_id ?? "",
      subject: raw.subject,
      preview: latest?.snippet ?? "",
      participants: latest?.from ? [latest.from] : [],
      latest_at: latest?.sent_at ?? new Date(0).toISOString(),
      unread_count: messages.filter((message: MessageDetail) => message.unread).length,
      starred: messages.some((message: MessageDetail) => message.starred),
      has_attachments: messages.some((message: MessageDetail) => message.attachments.length > 0),
      message_count: messages.length,
    }),
    messages,
  };
}

function normalizeDraft(raw: Raw): Draft {
  return {
    id: raw.id,
    account_id: raw.account_id,
    identity_id: raw.identity_id,
    reply_to_message_id: raw.reply_to_message_id,
    forward_message_id: raw.forward_message_id,
    to: raw.to ?? [],
    cc: raw.cc ?? [],
    bcc: raw.bcc ?? [],
    subject: raw.subject ?? "",
    body_html: raw.body_html ?? "",
    body_text: raw.body_text,
    attachments: (raw.attachments ?? []).map(normalizeAttachment),
    updated_at: raw.updated_at,
    state: raw.state ?? "draft",
    version: raw.version,
  };
}

function normalizeOperation(raw: Raw): Operation {
  const statusMap: Record<string, Operation["status"]> = {
    queued: "pending", running: "applying", succeeded: "applied", cancelled: "undone",
    failed: "failed", needs_attention: "needs_attention", unknown: "needs_attention",
  };
  return {
    id: raw.id,
    kind: raw.kind ?? raw.type,
    status: statusMap[raw.status ?? raw.state] ?? raw.status ?? "pending",
    conversation_ids: raw.conversation_ids ?? [],
    undoable_until: raw.undoable_until ?? raw.undo_until,
    error: raw.error,
  };
}

function accountPayload(input: Partial<AccountInput>): Raw {
  const imap = input.imap;
  const smtp = input.smtp;
  return {
    display_name: input.name,
    email: input.email,
    provider: input.provider,
    color: input.color,
    signature_html: input.signature_html,
    auth_type: input.auth_type,
    username: input.username,
    secret: input.secret,
    imap: imap ? { host: imap.host, port: imap.port, tls_mode: imap.tls_mode } : undefined,
    smtp: smtp ? { host: smtp.host, port: smtp.port, tls_mode: smtp.tls_mode } : undefined,
  };
}

export class HttpMailApi implements MailApi {
  constructor(private readonly baseUrl = "/api/v1") {}

  private async request<T>(path: string, init: RequestInit = {}): Promise<T> {
    const headers = new Headers(init.headers);
    headers.set("Accept", "application/json");
    if (init.body && !(init.body instanceof FormData)) headers.set("Content-Type", "application/json");
    if (csrfToken && init.method && !["GET", "HEAD", "OPTIONS"].includes(init.method)) headers.set("X-CSRF-Token", csrfToken);

    const response = await fetch(`${this.baseUrl}${path}`, { ...init, headers, credentials: "same-origin" });
    const contentType = response.headers.get("content-type") ?? "";
    const payload: unknown = response.status === 204
      ? undefined
      : contentType.includes("application/json")
        ? await response.json()
        : await response.text();

    if (!response.ok) {
      if (isErrorEnvelope(payload)) {
        throw new ApiError(payload.error.code, payload.error.message, payload.error.request_id, response.status);
      }
      throw new ApiError("http_error", typeof payload === "string" && payload ? payload : `请求失败 (${response.status})`, response.headers.get("x-request-id") ?? undefined, response.status);
    }
    return payload as T;
  }

  getSetupStatus = async () => normalizeSetupStatus(await this.request<Raw>("/setup/status"));
  beginSetup = (input: { token: string; username: string }) => this.request<SetupEnrollment>("/setup/enroll", { method: "POST", body: JSON.stringify({ bootstrap_token: input.token, username: input.username }) });
  completeSetup = (input: { token: string; username: string; password: string; totp_code: string }) => this.request<{ recovery_codes: string[] }>("/setup/complete", { method: "POST", body: JSON.stringify({ bootstrap_token: input.token, password: input.password, totp_code: input.totp_code }) });
  getSession = async (): Promise<AuthSession> => {
    try {
      const raw = await this.request<AuthSession | { admin_id: string; username: string; expires_at: string; csrf_token?: string }>("/auth/session");
      const session: AuthSession = "authenticated" in raw ? raw : { authenticated: true, user: { username: raw.username }, csrf_token: raw.csrf_token };
      csrfToken = session.csrf_token ?? cookieValue("mailmanager_csrf");
      return session;
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) return { authenticated: false };
      throw error;
    }
  };
  login = (input: { username: string; password: string }) => this.request<LoginChallenge>("/auth/login", { method: "POST", body: JSON.stringify(input) });
  verifyTotp = async (input: { challenge_token: string; code: string }): Promise<AuthSession> => {
    const raw = await this.request<AuthSession | { csrf_token: string; expires_at: string }>("/auth/totp", { method: "POST", body: JSON.stringify(input) });
    const session: AuthSession = "authenticated" in raw ? raw : { authenticated: true, user: { username: "admin" }, csrf_token: raw.csrf_token };
    csrfToken = session.csrf_token ?? cookieValue("mailmanager_csrf");
    return session;
  };
  logout = async (): Promise<void> => {
    await this.request<void>("/auth/session", { method: "DELETE" });
    csrfToken = undefined;
  };
  getSystemStatus = async (): Promise<SystemStatus> => {
    const raw = await this.request<Raw>("/system/status");
    return { status: raw.status ?? (raw.ready ? "ready" : "degraded"), version: raw.version, active_syncs: raw.active_syncs ?? raw.pending_actions };
  };
  getSystemUpdate = () => this.request<SystemUpdateStatus>("/system/update");
  checkSystemUpdate = () => this.request<SystemUpdateStatus>("/system/update/check", { method: "POST" });
  installSystemUpdate = (version: string) => this.request<SystemUpdateStatus>("/system/update/install", { method: "POST", body: JSON.stringify({ version }) });
  checkReadiness = async () => {
    try {
      const response = await fetch("/readyz", { cache: "no-store", credentials: "same-origin" });
      return response.ok;
    } catch {
      return false;
    }
  };
  getAccounts = async () => {
    const list = asList(await this.request<ListEnvelope<Raw> | Raw[]>("/accounts"));
    return { ...list, items: list.items.map(normalizeAccount) };
  };
  getOAuthSettings = async () => asList(await this.request<ListEnvelope<OAuthProviderSetting> | OAuthProviderSetting[]>("/settings/oauth"));
  configureOAuth = (provider: OAuthProviderSetting["provider"], input: { client_id: string; client_secret: string }) => this.request<OAuthProviderSetting>(`/settings/oauth/${provider}`, { method: "PUT", body: JSON.stringify(input) });
  startOAuth = (provider: OAuthProviderSetting["provider"], input: OAuthStartInput) => this.request<{ authorization_url: string; expires_at: string }>(`/oauth/${provider}/start`, { method: "POST", body: JSON.stringify(input) });
  createAccount = async (input: AccountInput) => normalizeAccount(await this.request<Raw>("/accounts", { method: "POST", body: JSON.stringify(accountPayload(input)) }));
  updateAccount = async (id: string, input: Partial<AccountInput>) => normalizeAccount(await this.request<Raw>(`/accounts/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify(accountPayload(input)) }));
  deleteAccount = (id: string) => this.request<void>(`/accounts/${encodeURIComponent(id)}`, { method: "DELETE" });
  testAccount = async (id: string) => {
    const raw = await this.request<Raw>(`/accounts/${encodeURIComponent(id)}/test`, { method: "POST" });
    return { ok: raw.ok ?? raw.status === "ready", message: raw.message };
  };
  syncAccount = (id: string) => this.request<void>(`/accounts/${encodeURIComponent(id)}/sync`, { method: "POST" });
  getMailboxes = async () => {
    const list = asList(await this.request<ListEnvelope<Raw> | Raw[]>("/mailboxes"));
    return { ...list, items: list.items.map(normalizeMailbox) };
  };
  updateFolderRole = async (id: string, role: NonNullable<Mailbox["source_role"]>) => normalizeMailbox(await this.request<Raw>(`/folders/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify({ role }) }));
  getConversations = async (query: ConversationQuery) => {
    const params = new URLSearchParams();
    Object.entries(query).forEach(([key, value]) => {
      if (value !== undefined && value !== "") params.set(key, String(value));
    });
    const list = asList(await this.request<ListEnvelope<Raw> | Raw[]>(`/conversations?${params}`));
    return { ...list, items: list.items.map(normalizeConversation) };
  };
  getConversation = async (id: string) => normalizeConversationDetail(await this.request<Raw>(`/conversations/${encodeURIComponent(id)}`));
  getMessageBody = (id: string, allowRemoteImages = true) => this.request<MessageBody>(`/messages/${encodeURIComponent(id)}/body?remote_images=${allowRemoteImages ? "allow" : "block"}`);
  getDrafts = async () => {
    const list = asList(await this.request<ListEnvelope<Raw> | Raw[]>("/drafts"));
    return { ...list, items: list.items.map(normalizeDraft) };
  };
  getDraft = async (id: string) => normalizeDraft(await this.request<Raw>(`/drafts/${encodeURIComponent(id)}`));
  saveDraft = async (input: DraftInput) => normalizeDraft(await (input.id
    ? this.request<Raw>(`/drafts/${encodeURIComponent(input.id)}`, { method: "PATCH", body: JSON.stringify(input) })
    : this.request<Raw>("/drafts", { method: "POST", body: JSON.stringify(input) })));
  uploadDraftAttachment = (id: string, file: File) => {
    const body = new FormData();
    body.set("file", file);
    return this.request<Raw>(`/drafts/${encodeURIComponent(id)}/attachments`, { method: "POST", body }).then(normalizeAttachment);
  };
  removeDraftAttachment = (draftId: string, attachmentId: string) => this.request<void>(`/drafts/${encodeURIComponent(draftId)}/attachments/${encodeURIComponent(attachmentId)}`, { method: "DELETE" });
  deleteDraft = (id: string) => this.request<void>(`/drafts/${encodeURIComponent(id)}`, { method: "DELETE" });
  sendDraft = async (id: string) => {
    const raw = await this.request<Raw>(`/drafts/${encodeURIComponent(id)}/send`, { method: "POST" });
    return { status: (raw.status ?? raw.state) as Draft["state"] };
  };
  getOutboxStatus = (id: string) => this.request<OutboxStatus>(`/outbox/${encodeURIComponent(id)}`);
  createOperation = async (input: { kind: OperationKind; conversation_ids: string[]; destination_mailbox_id?: string }) => normalizeOperation(await this.request<Raw>("/operations", { method: "POST", body: JSON.stringify(input) }));
  undoOperation = async (id: string) => normalizeOperation(await this.request<Raw>(`/operations/${encodeURIComponent(id)}/undo`, { method: "POST" }));
  attachmentUrl = (id: string) => `${this.baseUrl}/attachments/${encodeURIComponent(id)}`;
}

export const demoMode = import.meta.env.VITE_DEMO_MODE === "true";
export const api: MailApi = demoMode ? createDemoApi() : new HttpMailApi();

export type UUID = string;

export interface MailAddress {
  name?: string;
  email: string;
}

export type AccountProvider = "google" | "microsoft" | "qq" | "163" | "imap";
export type AccountStatus = "connected" | "syncing" | "error" | "reauth_required" | "disabled";
export type AccountAuthType = "password" | "oauth2";
export type TLSMode = "implicit" | "starttls";

export interface AccountEndpointSummary {
  host: string;
  port: number;
  tls_mode: TLSMode;
}

export interface AccountSummary {
  id: UUID;
  provider: AccountProvider;
  name: string;
  email: string;
  color: string;
  status: AccountStatus;
  auth_type: AccountAuthType;
  username: string;
  imap: AccountEndpointSummary;
  smtp: AccountEndpointSummary;
  status_message?: string;
  unread_count: number;
  last_synced_at?: string;
  signature_html?: string;
}

export interface Mailbox {
  id: UUID;
  account_id?: UUID;
  name: string;
  role: "inbox" | "starred" | "sent" | "drafts" | "archive" | "trash" | "custom";
  source_role?: "inbox" | "sent" | "drafts" | "archive" | "trash" | "junk" | "all" | "other";
  role_source?: "special_use" | "provider_preset" | "needs_user" | "user" | "virtual";
  unread_count: number;
  total_count?: number;
}

export interface Attachment {
  id: UUID;
  filename: string;
  content_type: string;
  size: number;
}

export interface ConversationSummary {
  id: UUID;
  account_id: UUID;
  account_ids?: UUID[];
  subject: string;
  snippet: string;
  participants: MailAddress[];
  last_message_at: string;
  unread: boolean;
  starred: boolean;
  has_attachments: boolean;
  message_count: number;
  mailbox_role?: Mailbox["role"];
}

export interface MessageDetail {
  id: UUID;
  conversation_id: UUID;
  account_id: UUID;
  from: MailAddress;
  to: MailAddress[];
  cc?: MailAddress[];
  reply_to?: MailAddress;
  subject: string;
  sent_at: string;
  received_at?: string;
  unread: boolean;
  starred: boolean;
  snippet?: string;
  attachments: Attachment[];
}

export interface ConversationDetail {
  conversation: ConversationSummary;
  messages: MessageDetail[];
}

export interface MessageBody {
  html: string;
  plain_text: string;
  remote_images_blocked: boolean;
}

export interface Draft {
  id: UUID;
  account_id: UUID;
  reply_to_message_id?: UUID;
  forward_message_id?: UUID;
  to: MailAddress[];
  cc: MailAddress[];
  bcc: MailAddress[];
  subject: string;
  body_html: string;
  body_text?: string;
  attachments: Attachment[];
  updated_at: string;
  state: "draft" | "queued" | "sending" | "sent" | "failed" | "unknown";
  version?: number;
  identity_id?: UUID;
}

export type OperationKind = "mark_read" | "mark_unread" | "star" | "unstar" | "archive" | "move" | "trash" | "delete";
export type OperationStatus = "pending" | "applying" | "applied" | "undone" | "failed" | "needs_attention";

export interface Operation {
  id: UUID;
  kind: OperationKind;
  status: OperationStatus;
  conversation_ids: UUID[];
  undoable_until?: string;
  error?: string;
}

export interface OutboxStatus {
  id: UUID;
  draft_id?: UUID;
  status: Draft["state"];
  updated_at: string;
  error?: string;
}

export interface SyncEvent {
  type: "account" | "conversation" | "operation" | "outbox" | "resync_required";
  resource_id?: UUID;
  version?: number;
  status?: string;
}

export interface ApiErrorEnvelope {
  error: {
    code: string;
    message: string;
    request_id?: string;
  };
}

export interface ListEnvelope<T> {
  items: T[];
  next_cursor?: string;
}

export interface SetupStatus {
  setup_required: boolean;
  expires_at?: string;
}

export interface SetupEnrollment {
  secret: string;
  provisioning_uri: string;
  expires_at: string;
}

export interface AuthSession {
  authenticated: boolean;
  user?: { username: string };
  csrf_token?: string;
}

export interface LoginChallenge {
  challenge_token: string;
  expires_at: string;
}

export interface SystemStatus {
  status: "ready" | "degraded" | "syncing";
  version?: string;
  active_syncs?: number;
}

export type SystemUpdateState = "idle" | "queued" | "downloading" | "installing" | "restarting" | "succeeded" | "failed";

export interface SystemUpdateRelease {
  version: string;
  tag_name: string;
  name: string;
  published_at: string;
  release_notes: string;
  html_url: string;
}

export interface SystemUpdateStatus {
  current_version: string;
  current_commit?: string;
  current_build_time?: string;
  enabled: boolean;
  install_supported: boolean;
  state: SystemUpdateState;
  available: boolean;
  checked_at?: string;
  latest?: SystemUpdateRelease;
  target_version?: string;
  message?: string;
  rollback_performed?: boolean;
}

export interface OAuthProviderSetting {
  provider: "google" | "microsoft";
  configured: boolean;
  client_id?: string;
}

export interface ConversationQuery {
  cursor?: string;
  limit?: number;
  mailbox?: Mailbox["role"] | string;
  account_id?: UUID;
  query?: string;
  unread?: boolean;
  starred?: boolean;
  has_attachments?: boolean;
}

export interface AccountInput {
  provider: AccountProvider;
  auth_type: "password";
  name: string;
  email: string;
  color: string;
  username: string;
  secret: string;
  signature_html?: string;
  imap: AccountEndpointSummary;
  smtp: AccountEndpointSummary;
}

export interface OAuthStartInput {
  account_id?: UUID;
  display_name?: string;
  email?: string;
  color?: string;
  signature_html?: string;
}

export interface DraftInput {
  id?: UUID;
  account_id: UUID;
  reply_to_message_id?: UUID;
  forward_message_id?: UUID;
  to: MailAddress[];
  cc: MailAddress[];
  bcc: MailAddress[];
  subject: string;
  body_html: string;
}

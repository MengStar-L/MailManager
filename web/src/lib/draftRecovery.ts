import { openDB, type DBSchema } from "idb";
import type { Attachment, Draft, MailAddress } from "../types";

export const draftRecoveryDatabaseName = "mailmanager-draft-recovery";
export const draftRecoveryDatabaseVersion = 1;
const recoveryKeyId = "draft-recovery-v1";

export interface RecoveryDraft {
  local_id: string;
  server_id?: string;
  revision: number;
  updated_at: string;
  account_id: string;
  reply_to_message_id?: string;
  forward_message_id?: string;
  to: MailAddress[];
  cc: MailAddress[];
  bcc: MailAddress[];
  subject: string;
  body_html: string;
  body_text: string;
  remote_attachments: Attachment[];
  pending_files: File[];
}

export type RecoveryRecord =
  | { status: "ready"; draft: RecoveryDraft }
  | { status: "unreadable"; local_id: string; updated_at: string };

export interface DraftListItem {
  id: string;
  source: "server" | "local" | "unreadable";
  account_id?: string;
  subject: string;
  to: MailAddress[];
  updated_at: string;
  pending_sync: boolean;
  draft?: Draft;
  recovery?: RecoveryDraft;
}

interface EncryptedValue {
  iv: Uint8Array<ArrayBuffer>;
  ciphertext: ArrayBuffer;
}

interface StoredFile extends EncryptedValue {
  name: string;
  type: string;
  lastModified: number;
}

interface StoredRecovery {
  localId: string;
  revision?: number;
  updatedAt: string;
  payload: EncryptedValue;
  files: StoredFile[];
}

interface RecoveryDatabase extends DBSchema {
  keys: { key: string; value: CryptoKey };
  drafts: { key: string; value: StoredRecovery };
}

type RecoveryPayload = Omit<RecoveryDraft, "local_id" | "updated_at" | "pending_files">;

async function openRecoveryDatabase() {
  return openDB<RecoveryDatabase>(draftRecoveryDatabaseName, draftRecoveryDatabaseVersion, {
    upgrade(database) {
      if (!database.objectStoreNames.contains("keys")) database.createObjectStore("keys");
      if (!database.objectStoreNames.contains("drafts")) database.createObjectStore("drafts", { keyPath: "localId" });
    },
  });
}

function requireCrypto() {
  if (!globalThis.crypto?.subtle) throw new Error("当前浏览器不支持加密草稿恢复");
  return globalThis.crypto;
}

async function readRecoveryKey() {
  const database = await openRecoveryDatabase();
  try { return await database.get("keys", recoveryKeyId); } finally { database.close(); }
}

async function getOrCreateRecoveryKey() {
  const existing = await readRecoveryKey();
  if (existing) return existing;
  const key = await requireCrypto().subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
  const database = await openRecoveryDatabase();
  try { await database.put("keys", key, recoveryKeyId); } finally { database.close(); }
  return key;
}

async function encryptBytes(key: CryptoKey, bytes: BufferSource): Promise<EncryptedValue> {
  const iv = new Uint8Array(new ArrayBuffer(12));
  requireCrypto().getRandomValues(iv);
  return { iv, ciphertext: await requireCrypto().subtle.encrypt({ name: "AES-GCM", iv }, key, bytes) };
}

async function decryptBytes(key: CryptoKey, value: EncryptedValue) {
  return requireCrypto().subtle.decrypt({ name: "AES-GCM", iv: value.iv }, key, value.ciphertext);
}

async function decryptStoredRecovery(key: CryptoKey, stored: StoredRecovery): Promise<RecoveryDraft> {
  const payloadBuffer = await decryptBytes(key, stored.payload);
  const payload = JSON.parse(new TextDecoder().decode(payloadBuffer)) as RecoveryPayload;
  const pendingFiles = await Promise.all(stored.files.map(async (file) => {
    const contents = await decryptBytes(key, file);
    return new File([contents], file.name, { type: file.type, lastModified: file.lastModified });
  }));
  return { ...payload, local_id: stored.localId, updated_at: stored.updatedAt, pending_files: pendingFiles };
}

export function hasMeaningfulDraftContent(draft: Pick<RecoveryDraft, "server_id" | "to" | "cc" | "bcc" | "subject" | "body_text" | "pending_files" | "reply_to_message_id" | "forward_message_id">) {
  return !!draft.server_id || draft.to.length > 0 || draft.cc.length > 0 || draft.bcc.length > 0 || draft.subject.trim() !== "" || draft.body_text.trim() !== "" || draft.pending_files.length > 0 || !!draft.reply_to_message_id || !!draft.forward_message_id;
}

export async function saveDraftRecovery(draft: RecoveryDraft) {
  if (!hasMeaningfulDraftContent(draft)) {
    await deleteDraftRecovery(draft.local_id);
    return;
  }
  const key = await getOrCreateRecoveryKey();
  const { local_id, updated_at, pending_files, ...payload } = draft;
  const encryptedPayload = await encryptBytes(key, new TextEncoder().encode(JSON.stringify(payload)));
  const files = await Promise.all(pending_files.map(async (file): Promise<StoredFile> => ({
    name: file.name,
    type: file.type,
    lastModified: file.lastModified,
    ...await encryptBytes(key, await file.arrayBuffer()),
  })));
  const database = await openRecoveryDatabase();
  try {
    const transaction = database.transaction("drafts", "readwrite");
    const existing = await transaction.store.get(local_id);
    const existingRevision = existing?.revision ?? -1;
    if (!existing || existingRevision < draft.revision || (existingRevision === draft.revision && existing.updatedAt <= updated_at)) {
      await transaction.store.put({ localId: local_id, revision: draft.revision, updatedAt: updated_at, payload: encryptedPayload, files });
    }
    await transaction.done;
  } finally { database.close(); }
}

export async function listDraftRecoveries(): Promise<RecoveryRecord[]> {
  const database = await openRecoveryDatabase();
  const stored = await database.getAll("drafts");
  database.close();
  if (!stored.length) return [];
  const key = await readRecoveryKey();
  const records = await Promise.all(stored.map(async (item): Promise<RecoveryRecord> => {
    if (!key) return { status: "unreadable", local_id: item.localId, updated_at: item.updatedAt };
    try { return { status: "ready", draft: await decryptStoredRecovery(key, item) }; }
    catch { return { status: "unreadable", local_id: item.localId, updated_at: item.updatedAt }; }
  }));
  return records.sort((left, right) => {
    const leftTime = left.status === "ready" ? left.draft.updated_at : left.updated_at;
    const rightTime = right.status === "ready" ? right.draft.updated_at : right.updated_at;
    return rightTime.localeCompare(leftTime);
  });
}

export async function readDraftRecovery(localId: string) {
  const records = await listDraftRecoveries();
  return records.find((record) => record.status === "ready" && record.draft.local_id === localId);
}

export async function deleteDraftRecovery(localId: string) {
  const database = await openRecoveryDatabase();
  try { await database.delete("drafts", localId); } finally { database.close(); }
}

export async function clearDraftRecoveries(options: { includeKey?: boolean } = {}) {
  const database = await openRecoveryDatabase();
  try {
    if (options.includeKey) {
      const transaction = database.transaction(["drafts", "keys"], "readwrite");
      await transaction.objectStore("drafts").clear();
      await transaction.objectStore("keys").clear();
      await transaction.done;
    } else await database.clear("drafts");
  } finally { database.close(); }
}

export function buildDraftListItems(serverDrafts: Draft[], recoveries: RecoveryRecord[]): DraftListItem[] {
  const recoveredServerIds = new Set(recoveries.flatMap((record) => record.status === "ready" && record.draft.server_id ? [record.draft.server_id] : []));
  const serverItems: DraftListItem[] = serverDrafts.filter((draft) => !recoveredServerIds.has(draft.id)).map((draft) => ({
    id: draft.id,
    source: "server",
    account_id: draft.account_id,
    subject: draft.subject,
    to: draft.to,
    updated_at: draft.updated_at,
    pending_sync: false,
    draft,
  }));
  const recoveryItems: DraftListItem[] = recoveries.map((record) => record.status === "ready" ? ({
    id: record.draft.local_id,
    source: "local",
    account_id: record.draft.account_id,
    subject: record.draft.subject,
    to: record.draft.to,
    updated_at: record.draft.updated_at,
    pending_sync: true,
    recovery: record.draft,
  }) : ({
    id: record.local_id,
    source: "unreadable",
    subject: "无法恢复的本地草稿",
    to: [],
    updated_at: record.updated_at,
    pending_sync: true,
  }));
  return [...serverItems, ...recoveryItems].sort((left, right) => right.updated_at.localeCompare(left.updated_at));
}

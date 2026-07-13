import "fake-indexeddb/auto";
import { openDB } from "idb";
import { afterEach, describe, expect, it } from "vitest";
import type { Draft } from "../types";
import {
  buildDraftListItems,
  clearDraftRecoveries,
  draftRecoveryDatabaseName,
  draftRecoveryDatabaseVersion,
  hasMeaningfulDraftContent,
  listDraftRecoveries,
  saveDraftRecovery,
  type RecoveryDraft,
} from "./draftRecovery";

const recovery = (overrides: Partial<RecoveryDraft> = {}): RecoveryDraft => ({
  local_id: "local-1",
  revision: 3,
  updated_at: "2026-07-13T12:00:00.000Z",
  account_id: "account-1",
  to: [{ name: "周宁", email: "zhou@example.com" }],
  cc: [],
  bcc: [],
  subject: "季度计划",
  body_html: "<p>机密恢复正文</p>",
  body_text: "机密恢复正文",
  remote_attachments: [],
  pending_files: [new File(["attachment secret"], "plan.txt", { type: "text/plain", lastModified: 123 })],
  ...overrides,
});

afterEach(async () => {
  await clearDraftRecoveries({ includeKey: true });
});

describe("draft recovery", () => {
  it("encrypts metadata and attachment bytes while preserving a complete round trip", async () => {
    await saveDraftRecovery(recovery());

    const database = await openDB(draftRecoveryDatabaseName, draftRecoveryDatabaseVersion);
    const stored = await database.get("drafts", "local-1") as { payload: { ciphertext: ArrayBuffer }; files: Array<{ ciphertext: ArrayBuffer }> };
    database.close();
    expect(new TextDecoder().decode(stored.payload.ciphertext)).not.toContain("机密恢复正文");
    expect(new TextDecoder().decode(stored.files[0].ciphertext)).not.toContain("attachment secret");

    const records = await listDraftRecoveries();
    expect(records).toHaveLength(1);
    expect(records[0].status).toBe("ready");
    if (records[0].status !== "ready") throw new Error("expected readable recovery");
    expect(records[0].draft.body_html).toContain("机密恢复正文");
    expect(records[0].draft.pending_files[0]).toBeInstanceOf(File);
    expect(await records[0].draft.pending_files[0].text()).toBe("attachment secret");
  });

  it("surfaces unreadable encrypted records without crashing the draft list", async () => {
    await saveDraftRecovery(recovery());
    const database = await openDB(draftRecoveryDatabaseName, draftRecoveryDatabaseVersion);
    await database.clear("keys");
    database.close();

    const records = await listDraftRecoveries();
    expect(records).toEqual([{ status: "unreadable", local_id: "local-1", updated_at: "2026-07-13T12:00:00.000Z" }]);
  });

  it("does not let an older server response overwrite a newer local revision", async () => {
    await saveDraftRecovery(recovery({ revision: 4, updated_at: "2026-07-13T12:01:00.000Z", subject: "最新编辑" }));
    await saveDraftRecovery(recovery({ revision: 3, updated_at: "2026-07-13T12:02:00.000Z", subject: "过期响应" }));

    const records = await listDraftRecoveries();
    expect(records).toHaveLength(1);
    expect(records[0].status).toBe("ready");
    if (records[0].status !== "ready") throw new Error("expected readable recovery");
    expect(records[0].draft.revision).toBe(4);
    expect(records[0].draft.subject).toBe("最新编辑");
  });

  it("deduplicates a server draft behind its newer local recovery and sorts newest first", () => {
    const serverDraft: Draft = {
      id: "server-1", account_id: "account-1", to: [], cc: [], bcc: [], subject: "服务器旧标题", body_html: "<p>old</p>", attachments: [], updated_at: "2026-07-13T11:00:00.000Z", state: "draft",
    };
    const items = buildDraftListItems([serverDraft], [
      { status: "ready", draft: recovery({ server_id: "server-1", subject: "本地新标题" }) },
      { status: "ready", draft: recovery({ local_id: "local-2", updated_at: "2026-07-13T13:00:00.000Z", subject: "最新本地草稿" }) },
    ]);

    expect(items.map((item) => item.subject)).toEqual(["最新本地草稿", "本地新标题"]);
    expect(items.every((item) => item.source === "local")).toBe(true);
  });

  it("does not persist a truly blank draft", () => {
    expect(hasMeaningfulDraftContent(recovery({ to: [], subject: "", body_html: "<p></p>", body_text: "", pending_files: [] }))).toBe(false);
    expect(hasMeaningfulDraftContent(recovery({ to: [], subject: "", body_html: "<p></p>", body_text: "", pending_files: [], reply_to_message_id: "message-1" }))).toBe(true);
    expect(hasMeaningfulDraftContent(recovery({ to: [], subject: "", body_html: "<p></p>", body_text: "", pending_files: [new File(["x"], "x.txt")] }))).toBe(true);
  });
});

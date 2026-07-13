import type { MailApi } from "../api/client";
import type {
  AccountInput,
  AccountSummary,
  ConversationDetail,
  ConversationQuery,
  ConversationSummary,
  Draft,
  DraftInput,
  MailAddress,
  Mailbox,
  MessageBody,
  MessageDetail,
  Operation,
  SetupEnrollment,
  SystemUpdateStatus,
} from "../types";

const oauthSettings = [
  { provider: "google" as const, configured: true, client_id: "demo-google-client.apps.googleusercontent.com" },
  { provider: "microsoft" as const, configured: false },
];

const wait = async <T,>(value: T, delay = 120): Promise<T> => {
  await new Promise((resolve) => window.setTimeout(resolve, delay));
  return structuredClone(value);
};

const now = Date.now();
const isoAgo = (minutes: number) => new Date(now - minutes * 60_000).toISOString();
const makeId = () => globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random()}`;

const accounts: AccountSummary[] = [
  { id: "acc-work", provider: "google", auth_type: "oauth2", name: "工作", email: "lin@atlas.studio", username: "lin@atlas.studio", color: "#2563a6", status: "connected", unread_count: 18, last_synced_at: isoAgo(1), imap: { host: "imap.gmail.com", port: 993, tls_mode: "implicit" }, smtp: { host: "smtp.gmail.com", port: 587, tls_mode: "starttls" } },
  { id: "acc-personal", provider: "microsoft", auth_type: "oauth2", name: "个人", email: "lin@example.com", username: "lin@example.com", color: "#dd7155", status: "syncing", unread_count: 7, last_synced_at: isoAgo(3), imap: { host: "outlook.office365.com", port: 993, tls_mode: "implicit" }, smtp: { host: "smtp.office365.com", port: 587, tls_mode: "starttls" } },
  { id: "acc-qq", provider: "qq", auth_type: "password", name: "QQ 邮箱", email: "70425@qq.com", username: "70425@qq.com", color: "#168477", status: "reauth_required", status_message: "授权码已失效", unread_count: 3, last_synced_at: isoAgo(840), imap: { host: "imap.qq.com", port: 993, tls_mode: "implicit" }, smtp: { host: "smtp.qq.com", port: 465, tls_mode: "implicit" } },
];

const mailboxes: Mailbox[] = [
  { id: "all-inbox", name: "统一收件箱", role: "inbox", unread_count: 28, total_count: 1432 },
  { id: "all-starred", name: "已加星标", role: "starred", unread_count: 2, total_count: 42 },
  { id: "all-sent", name: "已发送", role: "sent", unread_count: 0, total_count: 607 },
  { id: "all-drafts", name: "草稿", role: "drafts", unread_count: 0, total_count: 3 },
  { id: "all-archive", name: "归档", role: "archive", unread_count: 0, total_count: 2380 },
  { id: "all-trash", name: "回收站", role: "trash", unread_count: 0, total_count: 19 },
];

const people: MailAddress[] = [
  { name: "Maya Chen", email: "maya@notion.so" },
  { name: "周宁", email: "zhou.ning@northstar.cn" },
  { name: "Linear", email: "updates@linear.app" },
  { name: "Alex Morgan", email: "alex@atelier.co" },
  { name: "GitHub", email: "notifications@github.com" },
  { name: "许知行", email: "zhixing@cloudlake.io" },
  { name: "Figma", email: "team@figma.com" },
  { name: "赵若安", email: "ruoan@paperplane.cn" },
];

const subjects = [
  "设计系统评审：最后一轮调整",
  "下周项目节奏与交付节点",
  "Your weekly workspace digest",
  "Re: Q3 产品策略讨论",
  "[MailManager] Dependency update summary",
  "南京客户访谈记录与下一步",
  "A new comment in Brand refresh",
  "周五晚餐预订确认",
];

const snippets = [
  "我把你提到的留白和层级问题都整理进最新版，桌面与移动端的标注也已经补齐……",
  "团队已确认本周的三个关键节点。请重点看一下测试窗口，我们需要在周四之前完成……",
  "A calm week: 12 pages edited, 4 decisions closed, and two projects moved forward.",
  "赞同这个方向。关于首屏的信息密度，我建议保留一处更明确的视觉锚点，其余尽量克制……",
  "Six dependencies have updates available. No known security advisories were found in this group.",
  "访谈对象普遍认同统一入口的价值，但对首次同步时间比较敏感，建议增加可感知的进度……",
  "Mina left feedback on the account status component and mentioned you in the responsive layout frame.",
  "已为 4 位预留靠窗位置。餐厅将在当天 17:00 前再次确认，如有变化请直接回复本邮件。",
];

let conversations: ConversationSummary[] = Array.from({ length: 48 }, (_, index) => {
  const variant = index % subjects.length;
  return {
    id: `conversation-${index + 1}`,
    account_id: accounts[index % accounts.length].id,
    subject: index > subjects.length ? `${subjects[variant]} · ${Math.floor(index / subjects.length) + 1}` : subjects[variant],
    snippet: snippets[variant],
    participants: [people[variant]],
    last_message_at: isoAgo(index < 5 ? index * 19 + 4 : index * 240),
    unread: index < 8 || index % 9 === 0,
    starred: index === 1 || index === 7 || index === 18,
    has_attachments: index % 6 === 0,
    message_count: index % 4 === 0 ? 3 : index % 3 === 0 ? 2 : 1,
    mailbox_role: "inbox",
  };
});

let drafts: Draft[] = [
  { id: "draft-1", account_id: "acc-work", to: [{ name: "周宁", email: "zhou.ning@northstar.cn" }], cc: [], bcc: [], subject: "Re: 下周项目节奏与交付节点", body_html: "<p>周宁你好，</p><p>测试窗口没有问题，我会在周四上午同步最终结果。</p>", attachments: [], updated_at: isoAgo(26), state: "draft" },
  { id: "draft-2", account_id: "acc-personal", to: [], cc: [], bcc: [], subject: "旅行计划", body_html: "<p>需要确认：车次、酒店、周六晚餐。</p>", attachments: [], updated_at: isoAgo(310), state: "draft" },
  { id: "draft-3", account_id: "acc-work", to: [{ name: "Alex Morgan", email: "alex@atelier.co" }], cc: [], bcc: [], subject: "Q3 product notes", body_html: "<p>Hi Alex, here is the concise version of our discussion.</p>", attachments: [], updated_at: isoAgo(1280), state: "draft" },
];

let demoUpdateStartedAt = 0;
const demoUpdateRelease = {
  version: "1.1.0",
  tag_name: "v1.1.0",
  name: "MailManager 1.1.0",
  published_at: new Date(now - 86_400_000).toISOString(),
  release_notes: "新增网页一键更新与安全重启流程。\n优化邮件列表和阅读区的交互细节。",
  html_url: "https://github.com/MengStar-L/MailManager/releases/tag/v1.1.0",
};

function demoUpdateStatus(): SystemUpdateStatus {
  if (!demoUpdateStartedAt) {
    return {
      current_version: "1.0.0-demo",
      current_commit: "demo123",
      current_build_time: new Date(now - 7 * 86_400_000).toISOString(),
      enabled: true,
      install_supported: true,
      state: "idle",
      available: true,
      checked_at: new Date().toISOString(),
      latest: demoUpdateRelease,
    };
  }
  const elapsed = Date.now() - demoUpdateStartedAt;
  const state = elapsed < 700 ? "queued" : elapsed < 1_400 ? "downloading" : elapsed < 2_100 ? "installing" : elapsed < 2_800 ? "restarting" : "succeeded";
  const succeeded = state === "succeeded";
  return {
    current_version: succeeded ? demoUpdateRelease.version : "1.0.0-demo",
    current_commit: succeeded ? "demo456" : "demo123",
    current_build_time: succeeded ? new Date().toISOString() : new Date(now - 7 * 86_400_000).toISOString(),
    enabled: true,
    install_supported: true,
    state,
    available: !succeeded,
    checked_at: new Date().toISOString(),
    latest: demoUpdateRelease,
    target_version: demoUpdateRelease.version,
    message: succeeded ? "更新已安装，服务已恢复" : undefined,
  };
}

function messageFor(conversation: ConversationSummary, index: number): MessageDetail {
  return {
    id: `${conversation.id}-message-${index + 1}`,
    conversation_id: conversation.id,
    account_id: conversation.account_id,
    from: conversation.participants[0],
    to: [{ name: "林墨", email: accounts.find((item) => item.id === conversation.account_id)?.email ?? "lin@example.com" }],
    cc: index === 0 && conversation.message_count > 1 ? [{ name: "项目组", email: "project@atlas.studio" }] : [],
    subject: conversation.subject,
    sent_at: new Date(new Date(conversation.last_message_at).getTime() - (conversation.message_count - index - 1) * 3_600_000).toISOString(),
    unread: index === conversation.message_count - 1 && conversation.unread,
    starred: conversation.starred,
    snippet: conversation.snippet,
    attachments: conversation.has_attachments && index === conversation.message_count - 1
      ? [{ id: `${conversation.id}-attachment`, filename: "项目评审稿.pdf", content_type: "application/pdf", size: 2_840_372 }]
      : [],
  };
}

function bodyFor(messageId: string, allowRemoteImages: boolean): MessageBody {
  const conversation = conversations.find((item) => messageId.startsWith(`${item.id}-message-`));
  return {
    html: `<p>你好，</p><p>${conversation?.snippet ?? "这是邮件正文。"}</p><p>我把关键内容收拢为三点：</p><ol><li>本周完成范围确认与最终评审；</li><li>移动端保持完整能力，同时减少不必要的层级；</li><li>所有风险项在交付前明确责任人与时间。</li></ol><p>如有遗漏，请直接回复补充。</p><p>祝好<br>${conversation?.participants[0]?.name ?? "MailManager"}</p>${allowRemoteImages ? '<p style="color:#64706f">远程图片已由浏览器直接加载。</p>' : ""}`,
    plain_text: conversation?.snippet ?? "这是邮件正文。",
    remote_images_blocked: !allowRemoteImages && messageId.includes("message-1"),
  };
}

export function createDemoApi(): MailApi {
  return {
    getSetupStatus: () => wait({ setup_required: false }),
    beginSetup: () => wait<SetupEnrollment>({ secret: "JBSWY3DPEHPK3PXP", provisioning_uri: "otpauth://totp/MailManager:admin?secret=JBSWY3DPEHPK3PXP&issuer=MailManager", expires_at: isoAgo(-30) }),
    completeSetup: () => wait({ recovery_codes: ["WILLOW-8M2Q", "PAPER-4K9D", "NORTH-7T6A"] }),
    getSession: () => wait({ authenticated: true, user: { username: "admin" }, csrf_token: "demo-csrf" }),
    login: () => wait({ challenge_token: "demo-challenge", expires_at: isoAgo(-5) }),
    verifyTotp: () => wait({ authenticated: true, user: { username: "admin" } }),
    logout: () => wait(undefined),
    getSystemStatus: () => wait({ status: "ready", version: "0.1.0-demo", active_syncs: 1 }),
    getSystemUpdate: () => wait(demoUpdateStatus(), 80),
    checkSystemUpdate: () => wait(demoUpdateStatus(), 320),
    installSystemUpdate: (version) => {
      if (version !== demoUpdateRelease.version) return Promise.reject(new Error("目标版本已经变化，请重新检查更新"));
      demoUpdateStartedAt = Date.now();
      return wait(demoUpdateStatus(), 100);
    },
    checkReadiness: () => wait(true, 40),
    getAccounts: () => wait({ items: accounts }),
    getOAuthSettings: () => wait({ items: oauthSettings }),
    async configureOAuth(provider, input) {
      const setting = oauthSettings.find((item) => item.provider === provider);
      if (setting) Object.assign(setting, { configured: true, client_id: input.client_id });
      return wait({ provider, configured: true, client_id: input.client_id });
    },
    async startOAuth(_provider, input) {
      if (input.account_id) {
        const account = accounts.find((item) => item.id === input.account_id);
        if (!account || account.auth_type !== "oauth2") throw new Error("账户无法重新授权");
        account.status = "syncing";
      }
      return wait({ authorization_url: `${window.location.origin}/?oauth_demo=complete`, expires_at: isoAgo(-10) });
    },
    async createAccount(input: AccountInput) {
      const account: AccountSummary = {
        id: makeId(), provider: input.provider, auth_type: input.auth_type, name: input.name, email: input.email, color: input.color,
        status: "connected", username: input.username, imap: { ...input.imap }, smtp: { ...input.smtp }, unread_count: 0,
        last_synced_at: new Date().toISOString(), signature_html: input.signature_html,
      };
      accounts.push(account);
      return wait(account);
    },
    async updateAccount(id, input) {
      const account = accounts.find((item) => item.id === id);
      if (!account) throw new Error("账户不存在");
      if (input.name !== undefined) account.name = input.name;
      if (input.email !== undefined) account.email = input.email;
      if (input.color !== undefined) account.color = input.color;
      if (input.signature_html !== undefined) account.signature_html = input.signature_html;
      if (input.username !== undefined) account.username = input.username;
      if (input.imap !== undefined) account.imap = { ...input.imap };
      if (input.smtp !== undefined) account.smtp = { ...input.smtp };
      account.status = "syncing";
      return wait(account);
    },
    async deleteAccount(id) {
      const index = accounts.findIndex((item) => item.id === id);
      if (index >= 0) accounts.splice(index, 1);
      return wait(undefined);
    },
    testAccount: () => wait({ ok: true, message: "IMAP 与 SMTP 连接正常" }, 400),
    syncAccount: async (id) => {
      const account = accounts.find((item) => item.id === id);
      if (account) account.last_synced_at = new Date().toISOString();
      return wait(undefined, 500);
    },
    getMailboxes: () => wait({ items: mailboxes }),
    async updateFolderRole(id, role) {
      const mailbox = mailboxes.find((item) => item.id === id);
      if (!mailbox) throw new Error("文件夹不存在");
      mailbox.source_role = role;
      mailbox.role_source = "user";
      mailbox.role = role === "junk" || role === "all" || role === "other" ? "custom" : role;
      return wait(mailbox);
    },
    async getConversations(query: ConversationQuery) {
      let items = [...conversations];
      if (query.account_id) items = items.filter((item) => item.account_id === query.account_id);
      if (query.mailbox === "starred") items = items.filter((item) => item.starred);
      if (query.unread) items = items.filter((item) => item.unread);
      if (query.starred) items = items.filter((item) => item.starred);
      if (query.has_attachments) items = items.filter((item) => item.has_attachments);
      if (query.query) {
        const needle = query.query.toLocaleLowerCase();
        items = items.filter((item) => `${item.subject} ${item.snippet} ${item.participants.map((person) => `${person.name} ${person.email}`).join(" ")}`.toLocaleLowerCase().includes(needle));
      }
      return wait({ items: items.slice(0, query.limit ?? 100) });
    },
    async getConversation(id): Promise<ConversationDetail> {
      const conversation = conversations.find((item) => item.id === id);
      if (!conversation) throw new Error("会话不存在");
      return wait({ conversation, messages: Array.from({ length: conversation.message_count }, (_, index) => messageFor(conversation, index)) });
    },
    getMessageBody: (id, allowRemoteImages = true) => wait(bodyFor(id, allowRemoteImages)),
    getDrafts: () => wait({ items: drafts }),
    async getDraft(id) {
      const draft = drafts.find((item) => item.id === id);
      if (!draft) throw new Error("草稿不存在");
      return wait(draft);
    },
    async saveDraft(input: DraftInput) {
      const existing = input.id ? drafts.find((item) => item.id === input.id) : undefined;
      const draft: Draft = {
        id: existing?.id ?? makeId(),
        account_id: input.account_id,
        reply_to_message_id: input.reply_to_message_id,
        forward_message_id: input.forward_message_id,
        to: input.to,
        cc: input.cc,
        bcc: input.bcc,
        subject: input.subject,
        body_html: input.body_html,
        attachments: existing?.attachments ?? [],
        updated_at: new Date().toISOString(),
        state: "draft",
      };
      if (existing) Object.assign(existing, draft); else drafts.unshift(draft);
      return wait(draft);
    },
    async uploadDraftAttachment(id, file) {
      const attachment = { id: makeId(), filename: file.name, content_type: file.type || "application/octet-stream", size: file.size };
      const draft = drafts.find((item) => item.id === id);
      if (draft) draft.attachments.push(attachment);
      return wait(attachment);
    },
    async removeDraftAttachment(draftId, attachmentId) {
      const draft = drafts.find((item) => item.id === draftId);
      if (draft) draft.attachments = draft.attachments.filter((item) => item.id !== attachmentId);
      return wait(undefined);
    },
    async deleteDraft(id) {
      drafts = drafts.filter((item) => item.id !== id);
      return wait(undefined);
    },
    async sendDraft(id) {
      const draft = drafts.find((item) => item.id === id);
      if (draft) draft.state = "sent";
      return wait({ status: "sent" as const }, 650);
    },
    async getOutboxStatus(id) {
      return wait({ id, status: "sent" as const, updated_at: new Date().toISOString() });
    },
    async createOperation(input) {
      if (input.kind === "archive" || input.kind === "trash" || input.kind === "delete") {
        conversations = conversations.filter((item) => !input.conversation_ids.includes(item.id));
      } else {
        conversations = conversations.map((item) => input.conversation_ids.includes(item.id)
          ? { ...item, unread: input.kind === "mark_unread" ? true : input.kind === "mark_read" ? false : item.unread, starred: input.kind === "star" ? true : input.kind === "unstar" ? false : item.starred }
          : item);
      }
      const operation: Operation = { id: makeId(), kind: input.kind, status: "pending", conversation_ids: input.conversation_ids, undoable_until: isoAgo(-0.08) };
      return wait(operation, 180);
    },
    async undoOperation(id) {
      return wait({ id, kind: "archive", status: "undone", conversation_ids: [] });
    },
    attachmentUrl: () => "#demo-attachment",
  };
}

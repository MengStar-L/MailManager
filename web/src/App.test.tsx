import * as Tooltip from "@radix-ui/react-tooltip";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import App from "./App";
import { HttpMailApi } from "./api/client";
import { AuthGate } from "./auth/AuthGate";
import { conversationRowHeight } from "./components/ConversationList";
import { LazyOverlayBoundary } from "./components/LazyOverlayBoundary";
import { makeForwardSeed, messageBodyContent } from "./components/Reader";
import { SettingsDialog } from "./components/SettingsDialog";
import { shouldTimeUpdateRecovery, systemUpdatePollInterval, updateRecoveryTimeout, UpdateRestartOverlay } from "./components/SystemUpdate";
import { server } from "./test/server";
import type { AccountSummary, SystemUpdateStatus } from "./types";

function TestProviders({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return <QueryClientProvider client={client}><Tooltip.Provider delayDuration={0}>{children}</Tooltip.Provider></QueryClientProvider>;
}

const account = { id: "account-1", provider: "google", name: "工作", email: "lin@example.com", color: "#2563a6", status: "connected", unread_count: 2, last_synced_at: "2026-07-11T08:00:00Z" };
const conversation = { id: "conversation-1", account_id: account.id, subject: "设计系统最终评审", snippet: "我已经补齐移动端标注。", participants: [{ name: "周宁", email: "zhou@example.com" }], last_message_at: "2026-07-11T08:30:00Z", unread: true, starred: false, has_attachments: false, message_count: 1, mailbox_role: "inbox" as const };
const availableUpdate: SystemUpdateStatus = {
  current_version: "1.0.0",
  current_commit: "abc1234",
  current_build_time: "2026-07-10T08:00:00Z",
  enabled: true,
  install_supported: true,
  state: "idle",
  available: true,
  checked_at: "2026-07-13T08:00:00Z",
  latest: {
    version: "1.1.0",
    tag_name: "v1.1.0",
    name: "MailManager 1.1.0",
    published_at: "2026-07-12T08:00:00Z",
    release_notes: "新增网页自动更新。",
    html_url: "https://github.com/MengStar-L/MailManager/releases/tag/v1.1.0",
  },
};

function useWorkspaceHandlers() {
  server.use(
    http.get("/api/v1/accounts", () => HttpResponse.json({ items: [account] })),
    http.get("/api/v1/mailboxes", () => HttpResponse.json({ items: [{ id: "inbox", name: "统一收件箱", role: "inbox", unread_count: 2 }] })),
    http.get("/api/v1/system/status", () => HttpResponse.json({ status: "ready" })),
    http.get("/api/v1/system/update", () => HttpResponse.json({ current_version: "1.0.0", enabled: true, install_supported: true, state: "idle", available: false })),
    http.get("/api/v1/conversations", () => HttpResponse.json({ items: [conversation] })),
    http.get("/api/v1/conversations/:id", () => HttpResponse.json({ conversation, messages: [{ id: "message-1", conversation_id: conversation.id, account_id: account.id, from: conversation.participants[0], to: [{ email: account.email }], subject: conversation.subject, sent_at: conversation.last_message_at, unread: true, starred: false, snippet: conversation.snippet, attachments: [] }] })),
    http.get("/api/v1/messages/:id/body", () => HttpResponse.json({ html: "<p>这是经过清理的正文。</p>", plain_text: "这是经过清理的正文。", remote_images_blocked: false })),
    http.post("/api/v1/operations", async ({ request }) => { const body = await request.json() as { kind: string; conversation_ids: string[] }; return HttpResponse.json({ id: "operation-1", kind: body.kind, status: "applied", conversation_ids: body.conversation_ids }); }),
    http.get("/api/v1/drafts", () => HttpResponse.json({ items: [] })),
  );
}

describe("MailManager frontend", () => {
  it("uses deterministic conversation row heights for every density and viewport", () => {
    expect(conversationRowHeight(false, false)).toBe(113);
    expect(conversationRowHeight(false, true)).toBe(108);
    expect(conversationRowHeight(true, false)).toBe(93);
    expect(conversationRowHeight(true, true)).toBe(93);
  });

  it("completes password and TOTP login before revealing the workspace", async () => {
    let authenticated = false;
    useWorkspaceHandlers();
    server.use(
      http.get("/api/v1/setup/status", () => HttpResponse.json({ setup_required: false })),
      http.get("/api/v1/auth/session", () => authenticated ? HttpResponse.json({ admin_id: "admin-1", username: "admin", expires_at: "2026-07-12T00:00:00Z" }) : HttpResponse.json({ error: { code: "unauthorized", message: "请登录", request_id: "req-1" } }, { status: 401 })),
      http.post("/api/v1/auth/login", () => HttpResponse.json({ challenge_token: "challenge", expires_at: "2026-07-11T09:00:00Z", totp_required: true })),
      http.post("/api/v1/auth/totp", () => { authenticated = true; return HttpResponse.json({ csrf_token: "csrf", expires_at: "2026-07-12T00:00:00Z" }); }),
    );
    const user = userEvent.setup();
    render(<TestProviders><AuthGate><App /></AuthGate></TestProviders>);
    expect(await screen.findByRole("heading", { name: "欢迎回来" })).toBeInTheDocument();
    await user.type(screen.getByLabelText("密码"), "a-strong-password");
    await user.click(screen.getByRole("button", { name: "继续" }));
    expect(await screen.findByRole("heading", { name: "验证你的身份" })).toBeInTheDocument();
    await user.type(screen.getByLabelText("6 位验证码"), "123456");
    await user.click(screen.getByRole("button", { name: "打开邮箱" }));
    expect(await screen.findByRole("heading", { name: "统一收件箱" })).toBeInTheDocument();
  });

  it("renders the real list envelope and opens a conversation", async () => {
    useWorkspaceHandlers();
    const user = userEvent.setup();
    render(<TestProviders><App /></TestProviders>);
    expect(await screen.findByText("设计系统最终评审")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "打开 设计系统最终评审" }));
    await waitFor(() => expect(screen.getAllByText("设计系统最终评审").length).toBeGreaterThan(1));
    expect(screen.getAllByText("周宁").length).toBeGreaterThan(0);
    await waitFor(() => expect(screen.getAllByRole("button", { name: "回复" })).toHaveLength(1));
    expect(document.querySelector(".reader__toolbar-title")).toHaveTextContent(conversation.subject);
    expect(document.querySelector(".reader-title")).not.toBeInTheDocument();
    expect(document.querySelector(".message-card__body .message-body-frame")).toBeInTheDocument();
    expect(document.querySelector(".message-card__body + .message-card__actions")).toBeInTheDocument();
  });

  it("opens the composer without replacing the workspace", async () => {
    useWorkspaceHandlers();
    const user = userEvent.setup();
    render(<TestProviders><App /></TestProviders>);
    await screen.findByRole("heading", { name: "统一收件箱" });
    await user.click(document.querySelector<HTMLButtonElement>(".compose-button")!);
    expect(await screen.findByRole("region", { name: "新邮件" })).toBeInTheDocument();
    expect(screen.queryByRole("dialog", { name: "新邮件" })).not.toBeInTheDocument();
    expect(document.querySelector(".dialog-overlay")).not.toBeInTheDocument();
    expect(document.querySelector(".detail-slot .composer")).toBeInTheDocument();
    expect(document.querySelector(".mail-list__header h1")).toHaveTextContent("统一收件箱");
  });

  it("opens system update settings from the sidebar notice", async () => {
    useWorkspaceHandlers();
    server.use(
      http.get("/api/v1/system/update", () => HttpResponse.json(availableUpdate)),
      http.get("/api/v1/settings/oauth", () => HttpResponse.json({ items: [] })),
    );
    const user = userEvent.setup();
    render(<TestProviders><App /></TestProviders>);

    await screen.findByRole("heading", { name: "统一收件箱" });
    const updateNotice = await screen.findByRole("button", { name: "可更新 v1.1.0" });
    const settingsButton = screen.getByRole("button", { name: "账户与设置" });
    expect(settingsButton).toHaveClass("has-notice");
    await user.click(settingsButton);
    expect(await screen.findByRole("dialog", { name: "设置" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "邮箱账户" })).toHaveAttribute("data-state", "active");
    await user.click(screen.getByRole("button", { name: "关闭设置" }));

    await user.click(within(screen.getByRole("navigation", { name: "主要导航" })).getByRole("button", { name: "设置" }));
    expect(await screen.findByRole("dialog", { name: "设置" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "邮箱账户" })).toHaveAttribute("data-state", "active");
    await user.click(screen.getByRole("button", { name: "关闭设置" }));

    await user.click(updateNotice);

    expect(await screen.findByRole("dialog", { name: "设置" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "系统更新" })).toHaveAttribute("data-state", "active");
    expect(screen.getByRole("heading", { name: "系统更新" })).toBeInTheDocument();
    expect(screen.getByText("发现新版本 v1.1.0")).toBeInTheDocument();
  });

  it("uses update polling intervals and starts recovery timing only when needed", () => {
    const unchecked: SystemUpdateStatus = { current_version: "1.0.0", enabled: true, install_supported: true, state: "idle", available: false };
    const installing: SystemUpdateStatus = { ...availableUpdate, state: "installing", target_version: "1.1.0" };
    expect(systemUpdatePollInterval()).toBe(5_000);
    expect(systemUpdatePollInterval(unchecked)).toBe(5_000);
    expect(systemUpdatePollInterval(installing)).toBe(1_000);
    expect(systemUpdatePollInterval(availableUpdate)).toBe(30 * 60_000);
    expect(shouldTimeUpdateRecovery({ ...availableUpdate, state: "queued" }, false)).toBe(false);
    expect(shouldTimeUpdateRecovery({ ...availableUpdate, state: "downloading" }, false)).toBe(false);
    expect(shouldTimeUpdateRecovery({ ...availableUpdate, state: "installing" }, false)).toBe(false);
    expect(shouldTimeUpdateRecovery({ ...availableUpdate, state: "restarting" }, false)).toBe(true);
    expect(shouldTimeUpdateRecovery({ ...availableUpdate, state: "installing" }, true)).toBe(true);
    expect(updateRecoveryTimeout).toBe(120_000);
  });

  it("shows a neutral state before the first update check", async () => {
    server.use(
      http.get("/api/v1/settings/oauth", () => HttpResponse.json({ items: [] })),
      http.get("/api/v1/mailboxes", () => HttpResponse.json({ items: [] })),
      http.get("/api/v1/system/update", () => HttpResponse.json({ current_version: "1.0.0", enabled: true, install_supported: true, state: "idle", available: false })),
    );
    render(<TestProviders><SettingsDialog open onOpenChange={() => undefined} accounts={[]} initialTab="update" /></TestProviders>);
    expect((await screen.findAllByText("尚未检查更新")).length).toBeGreaterThan(0);
    expect(document.querySelector(".update-summary")).toHaveClass("update-summary--neutral");
    expect(screen.queryByText("当前已是最新版本")).not.toBeInTheDocument();
  });

  it("checks and installs an available update without duplicate submissions", async () => {
    let installs = 0;
    let installPayload: Record<string, unknown> = {};
    server.use(
      http.get("/api/v1/settings/oauth", () => HttpResponse.json({ items: [] })),
      http.get("/api/v1/mailboxes", () => HttpResponse.json({ items: [] })),
      http.get("/api/v1/system/update", () => HttpResponse.json(availableUpdate)),
      http.post("/api/v1/system/update/check", async () => {
        await new Promise((resolve) => setTimeout(resolve, 80));
        return HttpResponse.json({ ...availableUpdate, checked_at: "2026-07-13T09:00:00Z" });
      }),
      http.post("/api/v1/system/update/install", async ({ request }) => {
        installs += 1;
        installPayload = await request.json() as Record<string, unknown>;
        await new Promise((resolve) => setTimeout(resolve, 80));
        return HttpResponse.json({ ...availableUpdate, state: "queued", target_version: "1.1.0" });
      }),
    );
    const user = userEvent.setup();
    render(<TestProviders><SettingsDialog open onOpenChange={() => undefined} accounts={[]} initialTab="update" /></TestProviders>);

    expect(await screen.findByText("发现新版本 v1.1.0")).toBeInTheDocument();
    const installButton = screen.getByRole("button", { name: "更新并重启到 v1.1.0" });
    await user.click(screen.getByRole("button", { name: "检查更新" }));
    expect(installButton).toBeDisabled();
    await waitFor(() => expect(screen.getByRole("button", { name: "检查更新" })).toBeEnabled());
    await user.dblClick(installButton);
    expect(screen.getByRole("button", { name: "检查更新" })).toBeDisabled();

    await waitFor(() => expect(installs).toBe(1));
    expect(installPayload).toEqual({ version: "1.1.0" });
    expect(await screen.findByText("等待更新器 v1.1.0")).toBeInTheDocument();
  });

  it("probes readiness while the service is restarting", async () => {
    const refetch = vi.fn(async () => undefined);
    server.use(http.get("/readyz", () => new HttpResponse(null, { status: 200 })));
    render(<UpdateRestartOverlay status={{ ...availableUpdate, state: "restarting", target_version: "1.1.0" }} queryFailed refetch={refetch} />);

    expect(await screen.findByRole("status")).toHaveTextContent("正在重启 MailManager");
    const modal = screen.getByRole("dialog", { name: "正在重启 MailManager" });
    expect(modal).toHaveAttribute("aria-modal", "true");
    fireEvent.keyDown(modal, { key: "Escape" });
    fireEvent.pointerDown(document.querySelector(".update-restart-overlay")!);
    expect(screen.getByRole("dialog", { name: "正在重启 MailManager" })).toBeInTheDocument();
    await waitFor(() => expect(refetch).toHaveBeenCalledTimes(1));
  });

  it("lets early update stages continue in the background and reopens for restart", async () => {
    const user = userEvent.setup();
    const refetch = vi.fn(async () => undefined);
    server.use(http.get("/readyz", () => new HttpResponse(null, { status: 503 })));
    const { rerender } = render(<UpdateRestartOverlay status={{ ...availableUpdate, state: "queued", target_version: "1.1.0" }} queryFailed={false} refetch={refetch} />);
    expect(await screen.findByRole("dialog", { name: "等待更新器" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "在后台进行" }));
    expect(screen.queryByRole("dialog", { name: "等待更新器" })).not.toBeInTheDocument();

    rerender(<UpdateRestartOverlay status={{ ...availableUpdate, state: "restarting", target_version: "1.1.0" }} queryFailed={false} refetch={refetch} />);
    expect(await screen.findByRole("dialog", { name: "正在重启 MailManager" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "在后台进行" })).not.toBeInTheDocument();
  });

  it("keeps every checked conversation row visibly marked", async () => {
    useWorkspaceHandlers();
    const secondConversation = { ...conversation, id: "conversation-2", subject: "第二封邮件", unread: false };
    server.use(http.get("/api/v1/conversations", () => HttpResponse.json({ items: [conversation, secondConversation] })));
    const user = userEvent.setup();
    render(<TestProviders><App /></TestProviders>);
    await screen.findByText(secondConversation.subject);

    await user.click(screen.getByRole("checkbox", { name: "选择当前列表" }));
    const rows = Array.from(document.querySelectorAll(".conversation-row"));
    expect(rows).toHaveLength(2);
    rows.forEach((row) => expect(row).toHaveClass("is-checked"));

    await user.click(within(rows[0] as HTMLElement).getByRole("checkbox"));
    expect(rows[0]).not.toHaveClass("is-checked");
    expect(rows[1]).toHaveClass("is-checked");
  });

  it("selects the composer sender account with the custom select", async () => {
    useWorkspaceHandlers();
    const personalAccount = { ...account, id: "account-2", name: "个人", email: "me@example.com", color: "#dd7155", unread_count: 0 };
    server.use(http.get("/api/v1/accounts", () => HttpResponse.json({ items: [account, personalAccount] })));
    const user = userEvent.setup();
    render(<TestProviders><App /></TestProviders>);
    await screen.findByRole("heading", { name: "统一收件箱" });
    await user.click(document.querySelector<HTMLButtonElement>(".compose-button")!);

    const sender = await screen.findByRole("combobox", { name: "发件人" });
    await user.click(sender);
    await user.click(screen.getByRole("option", { name: "个人 · me@example.com" }));
    expect(sender).toHaveTextContent("个人 · me@example.com");
    expect(sender.querySelector<HTMLElement>(".account-dot")?.style.getPropertyValue("--dot-color")).toBe("#dd7155");
    expect(screen.getByText("本地保存中…")).toBeInTheDocument();
  });

  it("recovers an offline draft from the draft list after closing the inline composer", async () => {
    useWorkspaceHandlers();
    vi.spyOn(navigator, "onLine", "get").mockReturnValue(false);
    server.use(http.get("/api/v1/mailboxes", () => HttpResponse.json({ items: [
      { id: "inbox", name: "统一收件箱", role: "inbox", unread_count: 2 },
      { id: "drafts", name: "草稿", role: "drafts", unread_count: 0 },
    ] })));
    const user = userEvent.setup();
    render(<TestProviders><App /></TestProviders>);
    await screen.findByRole("heading", { name: "统一收件箱" });
    await user.click(document.querySelector<HTMLButtonElement>(".compose-button")!);
    await user.type(await screen.findByLabelText("主题"), "误触恢复草稿");
    await user.click(screen.getByRole("button", { name: "关闭写信" }));
    await waitFor(() => expect(screen.queryByRole("region", { name: "新邮件" })).not.toBeInTheDocument());

    const folderNavigation = screen.getByRole("navigation", { name: "邮箱文件夹" });
    await user.click(within(folderNavigation).getByRole("button", { name: "草稿" }));
    expect(document.querySelector(".mail-list")).not.toHaveAttribute("data-transitioning");
    expect(await screen.findByText("本地待同步")).toBeInTheDocument();
    const draftSubject = screen.getByText("误触恢复草稿");
    await user.click(draftSubject.closest("button")!);
    expect(await screen.findByLabelText("主题")).toHaveValue("误触恢复草稿");
    expect(screen.getByRole("region", { name: "编辑草稿" })).toBeInTheDocument();
  });

  it("keeps the workspace mounted when a lazy overlay fails", async () => {
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const dismiss = vi.fn();
    const BrokenOverlay = () => { throw new Error("chunk failed"); };
    render(<><h1>工作台</h1><LazyOverlayBoundary label="写信窗口" resetKey="open" onDismiss={dismiss}><BrokenOverlay /></LazyOverlayBoundary></>);
    expect(screen.getByRole("heading", { name: "工作台" })).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("写信窗口加载失败，工作台仍可使用");
    await userEvent.click(screen.getByRole("button", { name: "关闭写信窗口错误提示" }));
    expect(dismiss).toHaveBeenCalledOnce();
    consoleError.mockRestore();
  });

  it("keeps persistent reader controls mounted while switching conversations", async () => {
    useWorkspaceHandlers();
    const secondConversation = { ...conversation, id: "conversation-2", subject: "第二封邮件", snippet: "第二封邮件正文", unread: false };
    let releaseSecond: (() => void) | undefined;
    const secondReady = new Promise<void>((resolve) => { releaseSecond = resolve; });
    server.use(
      http.get("/api/v1/conversations", () => HttpResponse.json({ items: [conversation, secondConversation] })),
      http.get("/api/v1/conversations/:id", async ({ params }) => {
        const selected = params.id === secondConversation.id ? secondConversation : conversation;
        if (selected.id === secondConversation.id) await secondReady;
        return HttpResponse.json({ conversation: selected, messages: [{ id: `message-${selected.id}`, conversation_id: selected.id, account_id: account.id, from: selected.participants[0], to: [{ email: account.email }], subject: selected.subject, sent_at: selected.last_message_at, unread: selected.unread, starred: false, snippet: selected.snippet, attachments: [] }] });
      }),
    );
    const user = userEvent.setup();
    render(<TestProviders><App /></TestProviders>);
    await user.click(await screen.findByRole("button", { name: "打开 设计系统最终评审" }));
    await waitFor(() => expect(document.querySelector(".reader__toolbar-end")).toBeInTheDocument());
    const toolbarEnd = document.querySelector(".reader__toolbar-end");
    expect(document.querySelectorAll(".conversation-row__selection")).toHaveLength(1);
    expect(document.querySelector(".conversation-row__selection")?.closest(".conversation-row")).toHaveTextContent(conversation.subject);

    await user.click(screen.getByRole("button", { name: "打开 第二封邮件" }));
    await waitFor(() => expect(document.querySelector(".reader")).toHaveAttribute("aria-busy", "true"));
    expect(document.querySelector(".reader__toolbar-title")).toHaveTextContent(conversation.subject);
    expect(document.querySelector(".reader__toolbar-end")).toBe(toolbarEnd);
    expect(document.querySelector(".reader-loader")).not.toBeInTheDocument();
    expect(document.querySelector(".reader__progress")).toBeInTheDocument();
    expect(document.querySelectorAll(".conversation-row__selection")).toHaveLength(1);
    expect(document.querySelector(".conversation-row__selection")?.closest(".conversation-row")).toHaveTextContent(secondConversation.subject);

    await act(async () => { releaseSecond?.(); });
    await waitFor(() => expect(document.querySelector(".reader__toolbar-title")).toHaveTextContent(secondConversation.subject));
    expect(document.querySelector(".reader__toolbar-end")).toBe(toolbarEnd);
    expect([...document.querySelectorAll(".reader__toolbar-title-content")].some((element) => element.textContent?.includes(secondConversation.subject))).toBe(true);
    expect(document.querySelector(".reader__stage .reader__scroll")).toBeInTheDocument();
    expect(document.querySelector<HTMLElement>(".reader__toolbar-end")?.style.opacity).toBe("");
    expect(document.querySelector<HTMLElement>(".reader__toolbar-end")?.style.transform).toBe("");
    await waitFor(() => expect(document.querySelectorAll(".reader__toolbar-title-content")).toHaveLength(1), { timeout: 600 });
    await waitFor(() => expect(document.querySelectorAll(".reader__stage .reader__scroll")).toHaveLength(1), { timeout: 600 });
  });

  it("animates historical message expansion without forcing an accordion", async () => {
    useWorkspaceHandlers();
    const olderMessage = { id: "message-old", conversation_id: conversation.id, account_id: account.id, from: conversation.participants[0], to: [{ email: account.email }], subject: conversation.subject, sent_at: "2026-07-11T07:30:00Z", unread: false, starred: false, snippet: "较早的邮件", attachments: [] };
    const latestMessage = { ...olderMessage, id: "message-latest", sent_at: conversation.last_message_at, snippet: conversation.snippet };
    server.use(http.get("/api/v1/conversations/:id", () => HttpResponse.json({ conversation: { ...conversation, message_count: 2 }, messages: [olderMessage, latestMessage] })));
    const user = userEvent.setup();
    render(<TestProviders><App /></TestProviders>);
    await user.click(await screen.findByRole("button", { name: "打开 设计系统最终评审" }));
    await waitFor(() => expect(document.querySelectorAll(".message-card")).toHaveLength(2));
    const cards = document.querySelectorAll<HTMLElement>(".message-card");
    expect(cards[0]).not.toHaveClass("is-open");
    expect(cards[1]).toHaveClass("is-open");

    await user.click(cards[0].querySelector<HTMLButtonElement>(".message-card__header")!);
    expect(cards[0]).toHaveClass("is-open");
    expect(cards[1]).toHaveClass("is-open");
    await waitFor(() => expect(cards[0].querySelector(".message-card__content")).toBeInTheDocument());

    await user.click(cards[0].querySelector<HTMLButtonElement>(".message-card__header")!);
    await waitFor(() => expect(cards[0]).not.toHaveClass("is-open"));
    expect(cards[1]).toHaveClass("is-open");
  });

  it("keeps the previous conversation list until an account view is ready", async () => {
    useWorkspaceHandlers();
    const personalAccount = { ...account, id: "account-2", name: "个人", email: "me@example.com", unread_count: 0 };
    const personalConversation = { ...conversation, id: "conversation-personal", account_id: personalAccount.id, subject: "个人账号邮件", unread: false };
    let releasePersonal: (() => void) | undefined;
    const personalReady = new Promise<void>((resolve) => { releasePersonal = resolve; });
    server.use(
      http.get("/api/v1/accounts", () => HttpResponse.json({ items: [account, personalAccount] })),
      http.get("/api/v1/conversations", async ({ request }) => {
        if (new URL(request.url).searchParams.get("account_id") === personalAccount.id) {
          await personalReady;
          return HttpResponse.json({ items: [personalConversation] });
        }
        return HttpResponse.json({ items: [conversation] });
      }),
    );
    const user = userEvent.setup();
    render(<TestProviders><App /></TestProviders>);
    const originalRow = await screen.findByRole("button", { name: "打开 设计系统最终评审" });

    await user.click(screen.getByRole("button", { name: /个人/ }));
    await waitFor(() => expect(document.querySelector(".mail-list")).toHaveAttribute("data-transitioning", "true"));
    expect(document.querySelector(".mail-list")).toHaveAttribute("aria-busy", "true");
    expect(originalRow).toBeInTheDocument();
    expect(document.querySelector(".list-skeleton")).not.toBeInTheDocument();

    await act(async () => { releasePersonal?.(); });
    expect(await screen.findByText(personalConversation.subject)).toBeInTheDocument();
    await waitFor(() => expect(document.querySelector(".mail-list")).not.toHaveAttribute("data-transitioning"));
    await waitFor(() => expect(document.querySelectorAll(".virtual-row--enter").length).toBeGreaterThan(0));
  });

  it("loads remote email resources by default in a full-height frame", async () => {
    useWorkspaceHandlers();
    let bodyRequestURL = "";
    server.use(http.get("/api/v1/messages/:id/body", ({ request }) => {
      bodyRequestURL = request.url;
      return HttpResponse.json({ html: "<style>.hero{color:red}</style><p class=\"hero\">正文</p>", plain_text: "正文", remote_images_blocked: false });
    }));
    const user = userEvent.setup();
    render(<TestProviders><App /></TestProviders>);
    await screen.findByText(conversation.subject);
    await user.click(screen.getByRole("button", { name: new RegExp(conversation.subject) }));
    await waitFor(() => expect(document.querySelector(".message-body-frame")).toBeInTheDocument());
    expect(new URL(bodyRequestURL).searchParams.get("remote_images")).toBe("allow");
    expect(screen.queryByText("加载图片")).not.toBeInTheDocument();
  });

  it("applies and persists message body scale without reloading the iframe", async () => {
    useWorkspaceHandlers();
    localStorage.setItem("mailmanager-body-scale", "invalid");
    const user = userEvent.setup();
    render(<TestProviders><App /></TestProviders>);
    await user.click(await screen.findByRole("button", { name: "打开 设计系统最终评审" }));
    await waitFor(() => expect(document.querySelector(".message-body-frame")).toBeInTheDocument());

    const frame = document.querySelector<HTMLIFrameElement>(".message-body-frame")!;
    expect(screen.getByLabelText("邮件正文缩放")).toHaveTextContent("85%");
    expect(frame.style.width).toBe("117.6471%");
    expect(frame.style.height).toBe("117.6471%");
    expect(frame.style.zoom).toBe("0.85");
    expect(frame.style.transform).toBe("");

    const load = vi.fn();
    const srcDoc = frame.srcdoc;
    frame.addEventListener("load", load);
    await user.click(screen.getByRole("button", { name: "放大邮件正文" }));
    expect(screen.getByLabelText("邮件正文缩放")).toHaveTextContent("90%");
    expect(document.querySelector(".message-body-frame")).toBe(frame);
    expect(frame.style.zoom).toBe("0.9");
    expect(frame.srcdoc).toBe(srcDoc);
    expect(load).not.toHaveBeenCalled();
    await waitFor(() => expect(localStorage.getItem("mailmanager-body-scale")).toBe("90"));
  });

  it("normalizes message body scale and disables controls at both limits", async () => {
    useWorkspaceHandlers();
    localStorage.setItem("mailmanager-body-scale", "67");
    const user = userEvent.setup();
    render(<TestProviders><App /></TestProviders>);
    await user.click(await screen.findByRole("button", { name: "打开 设计系统最终评审" }));
    await waitFor(() => expect(document.querySelector(".message-body-frame")).toBeInTheDocument());

    const zoomOut = screen.getByRole("button", { name: "缩小邮件正文" });
    const zoomIn = screen.getByRole("button", { name: "放大邮件正文" });
    expect(screen.getByLabelText("邮件正文缩放")).toHaveTextContent("70%");
    expect(document.querySelector<HTMLIFrameElement>(".message-body-frame")?.style.zoom).toBe("0.7");
    expect(zoomOut).toBeDisabled();
    expect(zoomIn).toBeEnabled();

    for (let index = 0; index < 3; index += 1) await user.click(zoomIn);
    expect(screen.getByLabelText("邮件正文缩放")).toHaveTextContent("85%");
    expect(document.querySelector<HTMLIFrameElement>(".message-body-frame")?.style.zoom).toBe("0.85");
    for (let index = 0; index < 3; index += 1) await user.click(zoomIn);
    expect(screen.getByLabelText("邮件正文缩放")).toHaveTextContent("100%");
    expect(document.querySelector<HTMLIFrameElement>(".message-body-frame")?.style.zoom).toBe("1");
    expect(document.querySelector<HTMLIFrameElement>(".message-body-frame")?.style.width).toBe("100%");
    for (let index = 0; index < 4; index += 1) await user.click(zoomIn);
    expect(screen.getByLabelText("邮件正文缩放")).toHaveTextContent("120%");
    expect(document.querySelector<HTMLIFrameElement>(".message-body-frame")?.style.zoom).toBe("1.2");
    expect(document.querySelector<HTMLIFrameElement>(".message-body-frame")?.style.width).toBe("83.3333%");
    expect(zoomOut).toBeEnabled();
    expect(zoomIn).toBeDisabled();
  });

  it("preserves the standard API error code and request id", async () => {
    server.use(http.get("http://localhost/api/v1/system/status", () => HttpResponse.json({ error: { code: "service_degraded", message: "索引暂不可用", request_id: "req-42" } }, { status: 503 })));
    const client = new HttpMailApi("http://localhost/api/v1");
    await expect(client.getSystemStatus()).rejects.toMatchObject({ code: "service_degraded", message: "索引暂不可用", requestId: "req-42", status: 503 });
  });

  it("normalizes backend domain fields at the API boundary", async () => {
    server.use(
      http.get("http://localhost/api/v1/setup/status", () => HttpResponse.json({ configured: false, token_expires_at: "2026-07-11T09:00:00Z" })),
      http.get("http://localhost/api/v1/accounts", () => HttpResponse.json({ items: [{ id: "a1", provider: "imap", display_name: "Work", email: "work@example.com", color: "#315B7D", status: "ready", unread_count: 3, last_success_at: "2026-07-11T08:00:00Z" }] })),
      http.get("http://localhost/api/v1/conversations", () => HttpResponse.json({ items: [{ id: "c1", account_id: "a1", subject: "Status", preview: "Done", latest_at: "2026-07-11T08:00:00Z", unread_count: 1, starred: 1, has_attachments: 0, message_count: 2, participants: [] }] })),
    );
    const client = new HttpMailApi("http://localhost/api/v1");
    await expect(client.getSetupStatus()).resolves.toMatchObject({ setup_required: true });
    await expect(client.getAccounts()).resolves.toMatchObject({ items: [{ name: "Work", status: "connected", last_synced_at: "2026-07-11T08:00:00Z" }] });
    await expect(client.getConversations({ mailbox: "inbox" })).resolves.toMatchObject({ items: [{ snippet: "Done", unread: true, starred: true }] });
  });

  it("does not show unsynced for connected accounts when the sync timestamp is missing", async () => {
    useWorkspaceHandlers();
    server.use(http.get("/api/v1/accounts", () => HttpResponse.json({ items: [{ ...account, last_synced_at: undefined }] })));
    render(<TestProviders><App /></TestProviders>);
    await screen.findByText("已同步");
    expect(screen.queryByText("尚未同步")).not.toBeInTheDocument();
  });

  it("sends explicit custom STARTTLS and reconnect payloads", async () => {
    let createPayload: Record<string, unknown> = {};
    let updatePayload: Record<string, unknown> = {};
    let oauthPayload: Record<string, unknown> = {};
    server.use(
      http.post("http://localhost/api/v1/accounts", async ({ request }) => {
        createPayload = await request.json() as Record<string, unknown>;
        return HttpResponse.json({ id: "custom-1", provider: "imap", display_name: "Custom", email: "custom@example.com", color: "#2563A6", status: "pending", auth_type: "password", username: "custom-user", imap: createPayload.imap, smtp: createPayload.smtp });
      }),
      http.patch("http://localhost/api/v1/accounts/custom-1", async ({ request }) => {
        updatePayload = await request.json() as Record<string, unknown>;
        return HttpResponse.json({ id: "custom-1", provider: "imap", display_name: "Custom", email: "custom@example.com", color: "#2563A6", status: "pending", auth_type: "password", username: "custom-user", imap: updatePayload.imap, smtp: updatePayload.smtp });
      }),
      http.post("http://localhost/api/v1/oauth/google/start", async ({ request }) => {
        oauthPayload = await request.json() as Record<string, unknown>;
        return HttpResponse.json({ authorization_url: "https://accounts.example/authorize", expires_at: "2026-07-12T10:00:00Z" });
      }),
    );
    const client = new HttpMailApi("http://localhost/api/v1");
    await client.createAccount({
      provider: "imap", auth_type: "password", name: "Custom", email: "custom@example.com", color: "#2563A6",
      username: "custom-user", secret: "app-password",
      imap: { host: "imap.example.com", port: 993, tls_mode: "implicit" },
      smtp: { host: "smtp.example.com", port: 587, tls_mode: "starttls" },
    });
    expect(createPayload).toMatchObject({
      auth_type: "password", username: "custom-user", secret: "app-password",
      imap: { host: "imap.example.com", port: 993, tls_mode: "implicit" },
      smtp: { host: "smtp.example.com", port: 587, tls_mode: "starttls" },
    });
    await client.updateAccount("custom-1", {
      auth_type: "password", username: "custom-user", secret: "",
      imap: { host: "imap.example.com", port: 993, tls_mode: "implicit" },
      smtp: { host: "smtp.example.com", port: 587, tls_mode: "starttls" },
    });
    expect(updatePayload).toMatchObject({ username: "custom-user", secret: "", smtp: { port: 587, tls_mode: "starttls" } });
    await client.startOAuth("google", { account_id: "oauth-1" });
    expect(oauthPayload).toEqual({ account_id: "oauth-1" });
  });

  it("edits password connections and starts OAuth reauthorization from settings", async () => {
    const settingsAccounts: AccountSummary[] = [
      {
        id: "password-1", provider: "imap", auth_type: "password", name: "Custom", email: "custom@example.com", color: "#2563a6", status: "connected", unread_count: 0,
        username: "custom-user", imap: { host: "imap.example.com", port: 993, tls_mode: "implicit" }, smtp: { host: "smtp.example.com", port: 587, tls_mode: "starttls" },
      },
      {
        id: "oauth-1", provider: "google", auth_type: "oauth2", name: "Work", email: "oauth@example.com", color: "#dd7155", status: "reauth_required", unread_count: 0,
        username: "oauth@example.com", imap: { host: "imap.gmail.com", port: 993, tls_mode: "implicit" }, smtp: { host: "smtp.gmail.com", port: 587, tls_mode: "starttls" },
      },
    ];
    let passwordPayload: Record<string, unknown> = {};
    let reconnectPayload: Record<string, unknown> = {};
    server.use(
      http.get("/api/v1/settings/oauth", () => HttpResponse.json({ items: [{ provider: "google", configured: true, client_id: "client-id" }, { provider: "microsoft", configured: false }] })),
      http.get("/api/v1/mailboxes", () => HttpResponse.json({ items: [] })),
      http.patch("/api/v1/accounts/password-1", async ({ request }) => {
        passwordPayload = await request.json() as Record<string, unknown>;
        return HttpResponse.json({ ...settingsAccounts[0], display_name: settingsAccounts[0].name, status: "pending" });
      }),
      http.post("/api/v1/oauth/google/start", async ({ request }) => {
        reconnectPayload = await request.json() as Record<string, unknown>;
        return HttpResponse.json({ error: { code: "oauth_test", message: "测试已记录", request_id: "req-oauth" } }, { status: 409 });
      }),
    );
    const user = userEvent.setup();
    render(<TestProviders><SettingsDialog open onOpenChange={() => undefined} accounts={settingsAccounts} /></TestProviders>);
    await user.click(await screen.findByRole("button", { name: "编辑连接 custom@example.com" }));
    await user.clear(screen.getByLabelText("登录用户名"));
    await user.type(screen.getByLabelText("登录用户名"), "unsaved-user");
    await user.click(screen.getByRole("button", { name: "取消" }));
    await user.click(screen.getByRole("button", { name: "编辑连接 custom@example.com" }));
    expect(screen.getByLabelText("登录用户名")).toHaveValue("custom-user");
    await user.click(screen.getByRole("combobox", { name: "SMTP TLS" }));
    await user.click(screen.getByRole("option", { name: "SSL / TLS" }));
    await user.click(screen.getByRole("button", { name: "保存连接" }));
    await waitFor(() => expect(passwordPayload).toMatchObject({ username: "custom-user", secret: "", smtp: { port: 587, tls_mode: "implicit" } }));
    expect(screen.getByText("需要重新授权")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "重新授权 oauth@example.com" }));
    await waitFor(() => expect(reconnectPayload).toEqual({ account_id: "oauth-1" }));
  });

  it("saves a folder role selected with the custom select", async () => {
    const mailbox = { id: "folder-1", account_id: account.id, name: "Archive 2025", role: "other", role_source: "needs_user", unread_count: 0 };
    let folderPayload: Record<string, unknown> = {};
    server.use(
      http.get("/api/v1/settings/oauth", () => HttpResponse.json({ items: [] })),
      http.get("/api/v1/mailboxes", () => HttpResponse.json({ items: [mailbox] })),
      http.patch("/api/v1/folders/folder-1", async ({ request }) => {
        folderPayload = await request.json() as Record<string, unknown>;
        return HttpResponse.json({ ...mailbox, role: folderPayload.role, role_source: "user" });
      }),
    );
    const user = userEvent.setup();
    render(<TestProviders><SettingsDialog open onOpenChange={() => undefined} accounts={[account as AccountSummary]} /></TestProviders>);

    const roleSelect = await screen.findByRole("combobox", { name: "设置 Archive 2025 用途" });
    expect(roleSelect).toHaveTextContent("选择用途");
    await user.click(roleSelect);
    await user.click(screen.getByRole("option", { name: "归档" }));
    await user.click(screen.getByRole("button", { name: "确认" }));
    await waitFor(() => expect(folderPayload).toEqual({ role: "archive" }));
  });

  it("opens account settings and reports a successful OAuth callback", async () => {
    useWorkspaceHandlers();
    server.use(http.get("/api/v1/settings/oauth", () => HttpResponse.json({ items: [] })));
    window.history.pushState(null, "", "/settings/accounts?oauth=success");
    render(<TestProviders><App /></TestProviders>);
    expect(await screen.findByRole("dialog", { name: "设置" })).toBeInTheDocument();
    expect(screen.getByText("邮箱授权已更新")).toBeInTheDocument();
    expect(window.location.pathname).toBe("/");
  });

  it("renders plain-text bodies and forwards the complete loaded body", () => {
    const body = { html: "", plain_text: "第一行\n第二行 <安全>", remote_images_blocked: false };
    expect(messageBodyContent(body)).toContain("第二行 &lt;安全&gt;");
    const message = { id: "message-plain", conversation_id: conversation.id, account_id: account.id, from: conversation.participants[0], to: [{ email: account.email }], subject: conversation.subject, sent_at: conversation.last_message_at, unread: false, starred: false, attachments: [] };
    const seed = makeForwardSeed({ conversation, messages: [message] }, message, body);
    expect(seed.body_html).toContain("第一行");
    expect(seed.body_html).toContain("&lt;安全&gt;");
  });

  it("loads the next cursor page when the virtual list reaches its end", async () => {
    useWorkspaceHandlers();
    const second = { ...conversation, id: "conversation-2", subject: "第二页会话", last_message_at: "2026-07-11T08:00:00Z" };
    server.use(http.get("/api/v1/conversations", ({ request }) => {
      const cursor = new URL(request.url).searchParams.get("cursor");
      return HttpResponse.json(cursor ? { items: [second] } : { items: [conversation], next_cursor: "page-2" });
    }));
    render(<TestProviders><App /></TestProviders>);
    expect(await screen.findByText("设计系统最终评审")).toBeInTheDocument();
    const nextPageItem = await screen.findByText("第二页会话");
    expect(nextPageItem.closest(".virtual-row")).not.toHaveClass("virtual-row--enter");
  });

  it("animates only the first virtual window and does not replay on refresh", async () => {
    useWorkspaceHandlers();
    const user = userEvent.setup();
    render(<TestProviders><App /></TestProviders>);
    await screen.findByText(conversation.subject);

    await waitFor(() => expect(document.querySelectorAll(".virtual-row--enter").length).toBeGreaterThan(0));
    expect(document.querySelector<HTMLElement>(".virtual-row")?.style.opacity).toBe("");
    await waitFor(() => expect(document.querySelector(".virtual-row--enter")).not.toBeInTheDocument(), { timeout: 600 });

    await user.click(screen.getByRole("button", { name: "刷新列表" }));
    await waitFor(() => expect(document.querySelector(".list-refreshing")).not.toBeInTheDocument());
    expect(document.querySelector(".virtual-row--enter")).not.toBeInTheDocument();

    await user.type(screen.getByRole("textbox", { name: "搜索邮件" }), "设计");
    await waitFor(() => expect(document.querySelectorAll(".virtual-row--enter").length).toBeGreaterThan(0), { timeout: 1_000 });
  });
});

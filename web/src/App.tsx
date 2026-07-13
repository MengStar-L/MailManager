import { keepPreviousData, useInfiniteQuery, useMutation, useQuery, useQueryClient, type InfiniteData, type QueryKey } from "@tanstack/react-query";
import { AnimatePresence, motion } from "motion/react";
import { FilePenLine, Inbox, Menu, Plus, Settings, Star, Undo2, X } from "lucide-react";
import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError, demoMode } from "./api/client";
import type { ComposeSeed, ComposerHandle, ComposerLeaveResult } from "./components/Composer";
import { ConversationList, type ListFilters } from "./components/ConversationList";
import { LazyOverlayBoundary } from "./components/LazyOverlayBoundary";
import { Reader } from "./components/Reader";
import { Sidebar } from "./components/Sidebar";
import { systemUpdatePollInterval, systemUpdateQueryKey, UpdateRestartOverlay } from "./components/SystemUpdate";
import { Button, IconButton } from "./components/ui";
import { buildDraftListItems, deleteDraftRecovery, listDraftRecoveries, saveDraftRecovery, type DraftListItem, type RecoveryDraft, type RecoveryRecord } from "./lib/draftRecovery";
import type { ConversationSummary, ListEnvelope, Mailbox, OperationKind } from "./types";

const Composer = lazy(() => import("./components/Composer").then((module) => ({ default: module.Composer })));
const SettingsDialog = lazy(() => import("./components/SettingsDialog").then((module) => ({ default: module.SettingsDialog })));
const preloadRecoveryKey = "mailmanager-preload-recovery";
const preloadRecoveryWindow = 30_000;

const mailboxTitles: Record<Mailbox["role"], { title: string; subtitle: string }> = {
  inbox: { title: "统一收件箱", subtitle: "今天" },
  starred: { title: "已加星标", subtitle: "重要会话" },
  sent: { title: "已发送", subtitle: "所有账户" },
  drafts: { title: "草稿", subtitle: "自动保存与恢复" },
  archive: { title: "归档", subtitle: "已整理" },
  trash: { title: "回收站", subtitle: "待清理" },
  custom: { title: "邮件", subtitle: "文件夹" },
};

type ConversationPages = InfiniteData<ListEnvelope<ConversationSummary>, string>;
type ConversationSnapshot = [QueryKey, ConversationPages | undefined];
type Notice = { key: number; message: string; tone: "success" | "error" | "neutral"; operationId?: string; snapshots?: ConversationSnapshot[] };

function composeSeedFromDraft(item: DraftListItem): ComposeSeed | undefined {
  if (item.source === "server" && item.draft) return item.draft;
  if (item.source !== "local" || !item.recovery) return undefined;
  const draft = item.recovery;
  return {
    id: draft.server_id,
    recovery_id: draft.local_id,
    recovery_revision: draft.revision,
    account_id: draft.account_id,
    reply_to_message_id: draft.reply_to_message_id,
    forward_message_id: draft.forward_message_id,
    to: draft.to,
    cc: draft.cc,
    bcc: draft.bcc,
    subject: draft.subject,
    body_html: draft.body_html,
    body_text: draft.body_text,
    attachments: draft.remote_attachments,
    pending_files: draft.pending_files,
  };
}

async function syncRecoveryDraft(draft: RecoveryDraft) {
  const saved = await api.saveDraft({
    id: draft.server_id,
    account_id: draft.account_id,
    reply_to_message_id: draft.reply_to_message_id,
    forward_message_id: draft.forward_message_id,
    to: draft.to,
    cc: draft.cc,
    bcc: draft.bcc,
    subject: draft.subject,
    body_html: draft.body_html,
  });
  let remoteAttachments = saved.attachments;
  let pendingFiles = [...draft.pending_files];
  let working = { ...draft, server_id: saved.id, remote_attachments: remoteAttachments, pending_files: pendingFiles };
  if (pendingFiles.length) await saveDraftRecovery(working);
  for (const file of [...pendingFiles]) {
    const attachment = await api.uploadDraftAttachment(saved.id, file);
    remoteAttachments = [...remoteAttachments, attachment];
    pendingFiles = pendingFiles.filter((item) => item !== file);
    working = { ...working, remote_attachments: remoteAttachments, pending_files: pendingFiles };
    await saveDraftRecovery(working);
  }
  await deleteDraftRecovery(draft.local_id);
}

function oauthResultNotice(result: string): Pick<Notice, "message" | "tone"> {
  switch (result) {
    case "success": return { message: "邮箱授权已更新", tone: "success" };
    case "authorization_cancelled": return { message: "邮箱授权已取消", tone: "neutral" };
    case "account_exists": return { message: "该邮箱已经连接", tone: "error" };
    case "account_mismatch": return { message: "授权邮箱与原账户不匹配", tone: "error" };
    case "exchange_failed": return { message: "授权交换失败，请重新连接", tone: "error" };
    default: return { message: "无法完成邮箱授权，请重试", tone: "error" };
  }
}

function useDebouncedValue<T>(value: T, delay: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => { const timer = window.setTimeout(() => setDebounced(value), delay); return () => window.clearTimeout(timer); }, [value, delay]);
  return debounced;
}

function useLiveEvents() {
  const queryClient = useQueryClient();
  useEffect(() => {
    if (demoMode || typeof EventSource === "undefined") return;
    const events = new EventSource("/api/v1/events", { withCredentials: true });
    events.onmessage = (event) => {
      try {
        const payload = JSON.parse(event.data) as { type?: string };
        const type = payload.type ?? "";
        if (type === "resync_required") queryClient.invalidateQueries();
        else if (type.startsWith("account.") || type.startsWith("sync.")) {
          queryClient.invalidateQueries({ queryKey: ["accounts"] });
          queryClient.invalidateQueries({ queryKey: ["mailboxes"] });
          queryClient.invalidateQueries({ queryKey: ["conversations"] });
        } else if (type.startsWith("draft.") || type.startsWith("outbox.")) {
          queryClient.invalidateQueries({ queryKey: ["drafts"] });
          if (type.startsWith("outbox.")) queryClient.invalidateQueries({ queryKey: ["conversations"] });
        } else queryClient.invalidateQueries({ queryKey: ["conversations"] });
      } catch { /* Ignore keep-alive or unknown event payloads. */ }
    };
    return () => events.close();
  }, [queryClient]);
}

export default function App() {
  const queryClient = useQueryClient();
  const [role, setRole] = useState<Mailbox["role"]>("inbox");
  const [accountId, setAccountId] = useState<string>();
  const [selectedId, setSelectedId] = useState<string>();
  const [checkedIds, setCheckedIds] = useState<Set<string>>(new Set());
  const [search, setSearch] = useState("");
  const debouncedSearch = useDebouncedValue(search, 240);
  const [filters, setFilters] = useState<ListFilters>({ unread: false, starred: false, attachments: false });
  const [composerOpen, setComposerOpen] = useState(false);
  const [composeSeed, setComposeSeed] = useState<ComposeSeed>();
  const [composeSession, setComposeSession] = useState(0);
  const [settingsOpen, setSettingsOpen] = useState(() => window.location.pathname === "/settings/accounts");
  const [settingsTab, setSettingsTab] = useState<"accounts" | "update">("accounts");
  const [oauthFeedback, setOAuthFeedback] = useState<Pick<Notice, "message" | "tone"> | undefined>(() => {
    if (window.location.pathname !== "/settings/accounts") return undefined;
    const result = new URLSearchParams(window.location.search).get("oauth");
    return result ? oauthResultNotice(result) : undefined;
  });
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const [notice, setNotice] = useState<Notice>();
  const composerRef = useRef<ComposerHandle>(null);
  const composerTransitionRef = useRef(false);
  const recoverySyncRef = useRef(false);

  useLiveEvents();
  const accountsQuery = useQuery({ queryKey: ["accounts"], queryFn: api.getAccounts });
  const mailboxesQuery = useQuery({ queryKey: ["mailboxes"], queryFn: api.getMailboxes });
  const statusQuery = useQuery({ queryKey: ["system-status"], queryFn: api.getSystemStatus, refetchInterval: 30_000 });
  const updateQuery = useQuery({
    queryKey: systemUpdateQueryKey,
    queryFn: api.getSystemUpdate,
    refetchOnWindowFocus: true,
    refetchInterval: (query) => systemUpdatePollInterval(query.state.data),
    retry: false,
  });
  const draftsQuery = useQuery({ queryKey: ["drafts"], queryFn: api.getDrafts, enabled: role === "drafts" || composerOpen });
  const recoveryQuery = useQuery({ queryKey: ["draft-recoveries"], queryFn: listDraftRecoveries });
  const conversationsQuery = useInfiniteQuery({
    queryKey: ["conversations", { role, accountId, search: debouncedSearch, ...filters }],
    queryFn: ({ pageParam }) => api.getConversations({ mailbox: role, account_id: accountId, query: debouncedSearch, unread: filters.unread || undefined, starred: filters.starred || undefined, has_attachments: filters.attachments || undefined, cursor: pageParam || undefined, limit: 100 }),
    initialPageParam: "",
    getNextPageParam: (lastPage) => lastPage.next_cursor,
    enabled: role !== "drafts",
    placeholderData: keepPreviousData,
  });

  const accounts = accountsQuery.data?.items ?? [];
  const mailboxes = mailboxesQuery.data?.items ?? [];
  const conversations = conversationsQuery.data?.pages.flatMap((page) => page.items) ?? [];
  const drafts = useMemo(() => buildDraftListItems(draftsQuery.data?.items ?? [], recoveryQuery.data ?? []), [draftsQuery.data?.items, recoveryQuery.data]);
  const title = accountId ? accounts.find((account) => account.id === accountId)?.name ?? mailboxTitles[role].title : mailboxTitles[role].title;
  const subtitle = accountId ? mailboxTitles[role].title : mailboxTitles[role].subtitle;
  const conversationViewKey = JSON.stringify([role, accountId ?? "", debouncedSearch, filters.unread, filters.starred, filters.attachments]);

  const showNotice = useCallback((message: string, tone: Notice["tone"] = "neutral") => setNotice({ key: Date.now(), message, tone }), []);
  const refreshRecoveries = useCallback(async () => {
    const recoveries = await listDraftRecoveries();
    queryClient.setQueryData(["draft-recoveries"], recoveries);
  }, [queryClient]);
  const syncRecoveries = useCallback(async (records?: RecoveryRecord[]) => {
    if (composerOpen || recoverySyncRef.current || !navigator.onLine) return;
    const currentRecords = records ?? queryClient.getQueryData<RecoveryRecord[]>(["draft-recoveries"]) ?? [];
    const recoveries = currentRecords.flatMap((record) => record.status === "ready" ? [record.draft] : []);
    if (!recoveries.length) return;
    recoverySyncRef.current = true;
    let synced = 0;
    try {
      for (const recovery of recoveries) {
        try { await syncRecoveryDraft(recovery); synced += 1; }
        catch { break; }
      }
    } finally {
      recoverySyncRef.current = false;
      if (synced > 0) {
        await queryClient.invalidateQueries({ queryKey: ["draft-recoveries"] });
        await queryClient.invalidateQueries({ queryKey: ["drafts"] });
      }
    }
  }, [composerOpen, queryClient]);
  useEffect(() => { void syncRecoveries(recoveryQuery.data); }, [recoveryQuery.data, syncRecoveries]);
  useEffect(() => {
    const retry = () => {
      if (composerOpen) return;
      void refreshRecoveries().then(() => syncRecoveries());
    };
    window.addEventListener("online", retry);
    const timer = window.setInterval(retry, 30_000);
    return () => {
      window.removeEventListener("online", retry);
      window.clearInterval(timer);
    };
  }, [composerOpen, refreshRecoveries, syncRecoveries]);
  useEffect(() => {
    const recover = (event: Event) => {
      event.preventDefault();
      let lastRecovery = 0;
      try { lastRecovery = Number(sessionStorage.getItem(preloadRecoveryKey) ?? 0); } catch { /* Storage may be unavailable in restricted browsing modes. */ }
      if (Date.now() - lastRecovery > preloadRecoveryWindow) {
        try { sessionStorage.setItem(preloadRecoveryKey, String(Date.now())); } catch { /* Reload once even when storage is unavailable. */ }
        window.location.reload();
        return;
      }
      showNotice("页面资源加载失败，请重新加载后继续", "error");
    };
    window.addEventListener("vite:preloadError", recover);
    return () => window.removeEventListener("vite:preloadError", recover);
  }, [showNotice]);
  useEffect(() => {
    if (window.location.pathname !== "/settings/accounts") return;
    window.history.replaceState(null, "", "/");
  }, []);
  useEffect(() => { if (!notice) return; const timer = window.setTimeout(() => setNotice((current) => current?.key === notice.key ? undefined : current), notice.operationId ? 5_200 : 3_500); return () => window.clearTimeout(timer); }, [notice]);
  useEffect(() => { setCheckedIds(new Set()); setSelectedId(undefined); }, [role, accountId]);
  useEffect(() => {
    document.documentElement.dataset.density = localStorage.getItem("mailmanager-density") ?? "comfortable";
    document.documentElement.dataset.reduceMotion = localStorage.getItem("mailmanager-reduce-motion") ?? "false";
  }, []);
  const operation = useMutation({
    mutationFn: (input: { kind: OperationKind; ids: string[] }) => api.createOperation({ kind: input.kind, conversation_ids: input.ids }),
    onMutate: async (input) => {
      await queryClient.cancelQueries({ queryKey: ["conversations"] });
      const snapshots = queryClient.getQueriesData<ConversationPages>({ queryKey: ["conversations"] });
      queryClient.setQueriesData<ConversationPages>({ queryKey: ["conversations"] }, (old) => {
        if (!old) return old;
        return { ...old, pages: old.pages.map((page) => ({ ...page, items: ["archive", "trash", "delete"].includes(input.kind) ? page.items.filter((item) => !input.ids.includes(item.id)) : page.items.map((item) => input.ids.includes(item.id) ? { ...item, unread: input.kind === "mark_unread" ? true : input.kind === "mark_read" ? false : item.unread, starred: input.kind === "star" ? true : input.kind === "unstar" ? false : item.starred } : item) })) };
      });
      return { snapshots };
    },
    onError: (error, _input, context) => {
      context?.snapshots.forEach(([key, data]) => queryClient.setQueryData(key, data));
      showNotice(error instanceof ApiError ? error.message : "操作失败，列表已恢复", "error");
    },
    onSuccess: (result, input, context) => {
      setCheckedIds(new Set());
      if (["archive", "trash"].includes(input.kind) && result.undoable_until) {
        setNotice({ key: Date.now(), message: input.kind === "archive" ? "会话已归档" : "会话已移到回收站", tone: "success", operationId: result.id, snapshots: context?.snapshots });
        if (input.ids.includes(selectedId ?? "")) setSelectedId(undefined);
      } else queryClient.invalidateQueries({ queryKey: ["conversations"] });
    },
  });

  const runOperation = (kind: OperationKind, ids = [...checkedIds]) => {
    if (!ids.length) return;
    if (kind === "delete" && !window.confirm("此操作会永久删除邮件，且无法撤销。确定继续？")) return;
    operation.mutate({ kind, ids });
  };

  const undo = async () => {
    if (!notice?.operationId) return;
    try {
      await api.undoOperation(notice.operationId);
      notice.snapshots?.forEach(([key, data]) => queryClient.setQueryData(key, data));
      setNotice({ key: Date.now(), message: "操作已撤销", tone: "neutral" });
    } catch (error) { showNotice(error instanceof Error ? error.message : "无法撤销操作", "error"); }
  };

  const completeComposer = useCallback(() => { setComposerOpen(false); setComposeSeed(undefined); }, []);
  const runAfterComposerSaved = useCallback(async (action: () => void | Promise<void>) => {
    if (!composerOpen) { await action(); return; }
    if (composerTransitionRef.current) return;
    composerTransitionRef.current = true;
    try {
      const result: ComposerLeaveResult = await composerRef.current?.saveBeforeLeave() ?? "empty";
      if (result === "blocked") return;
      completeComposer();
      await action();
      if (result === "local-saved") showNotice("草稿已安全保存在本机，将在后台同步", "neutral");
    } finally { composerTransitionRef.current = false; }
  }, [completeComposer, composerOpen, showNotice]);
  const openCompose = useCallback((seed?: ComposeSeed) => {
    if (composerOpen && !seed) {
      void runAfterComposerSaved(() => { setComposeSeed(undefined); setComposeSession((value) => value + 1); setComposerOpen(true); });
      return;
    }
    void runAfterComposerSaved(() => { setComposeSeed(seed); setComposeSession((value) => value + 1); setComposerOpen(true); });
  }, [composerOpen, runAfterComposerSaved]);
  const navigateMailbox = useCallback((nextRole: Mailbox["role"], nextAccountId?: string) => {
    void runAfterComposerSaved(() => { setRole(nextRole); setAccountId(nextAccountId); setMobileNavOpen(false); });
  }, [runAfterComposerSaved]);
  const selectConversation = useCallback((id: string) => {
    void runAfterComposerSaved(() => {
      setSelectedId(id);
      const item = conversations.find((conversation) => conversation.id === id);
      if (item?.unread) runOperation("mark_read", [id]);
    });
  }, [conversations, runAfterComposerSaved]);
  const openDraft = useCallback((item: DraftListItem) => {
    if (item.source === "unreadable") {
      if (window.confirm("这份本地草稿无法解密。确定删除恢复记录？")) void deleteDraftRecovery(item.id).then(refreshRecoveries);
      return;
    }
    openCompose(composeSeedFromDraft(item));
  }, [openCompose, refreshRecoveries]);
  const openSettings = useCallback((tab: "accounts" | "update" = "accounts") => { void runAfterComposerSaved(() => { setSettingsTab(tab); setSettingsOpen(true); }); }, [runAfterComposerSaved]);
  const logout = useCallback(() => {
    void runAfterComposerSaved(async () => { await api.logout(); queryClient.clear(); window.location.reload(); });
  }, [queryClient, runAfterComposerSaved]);
  useEffect(() => {
    const handler = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement;
      const typing = target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.isContentEditable;
      if (!typing && event.key === "/") { event.preventDefault(); document.querySelector<HTMLInputElement>("[aria-label='搜索邮件']")?.focus(); }
      if (!typing && event.key.toLocaleLowerCase() === "c") {
        event.preventDefault();
        if (composerOpen) composerRef.current?.focus(); else openCompose();
      }
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, [composerOpen, openCompose]);
  const convError = conversationsQuery.error instanceof Error ? conversationsQuery.error.message : conversationsQuery.isError ? "未知错误" : undefined;

  return (
    <div className={`app-shell ${selectedId || composerOpen ? "has-reader" : ""} ${composerOpen ? "has-composer" : ""} ${mobileNavOpen ? "mobile-nav-open" : ""}`}>
      {demoMode && <div className="demo-ribbon">演示数据</div>}
      <Sidebar accounts={accounts} mailboxes={mailboxes} selectedMailbox={role} selectedAccount={accountId} status={statusQuery.data} updateStatus={updateQuery.data} onNavigate={navigateMailbox} onCompose={() => openCompose()} onSettings={() => openSettings("accounts")} onUpdate={() => openSettings("update")} onLogout={logout} />
      {mobileNavOpen && <button className="mobile-nav-overlay" aria-label="关闭导航" onClick={() => setMobileNavOpen(false)} />}
      <header className="mobile-header"><IconButton label="打开导航" onClick={() => setMobileNavOpen(true)}><Menu size={20} /></IconButton><div className="mobile-header__brand"><div className="brand-mark">M</div><strong>MailManager</strong></div><IconButton label="写邮件" onClick={() => openCompose()}><Plus size={20} /></IconButton></header>
      <main className="workspace">
        <ConversationList title={title} subtitle={subtitle} role={role} viewKey={conversationViewKey} items={conversations} drafts={drafts} accounts={accounts} selectedId={selectedId} checkedIds={checkedIds} search={search} filters={filters} loading={conversationsQuery.isPending && role !== "drafts"} transitioning={role !== "drafts" && conversationsQuery.isPlaceholderData} error={convError} refreshing={conversationsQuery.isFetching && !conversationsQuery.isPending && !conversationsQuery.isFetchingNextPage && !conversationsQuery.isPlaceholderData} hasNextPage={!!conversationsQuery.hasNextPage} loadingMore={conversationsQuery.isFetchingNextPage} onLoadMore={() => { void conversationsQuery.fetchNextPage(); }} onSearch={setSearch} onFilters={setFilters} onSelect={selectConversation} onCheck={(id, checked) => setCheckedIds((current) => { const next = new Set(current); if (checked) next.add(id); else next.delete(id); return next; })} onCheckAll={(checked) => setCheckedIds(checked ? new Set(conversations.map((item) => item.id)) : new Set())} onOperation={runOperation} onRefresh={() => { conversationsQuery.refetch(); accountsQuery.refetch(); mailboxesQuery.refetch(); recoveryQuery.refetch(); }} onDraft={openDraft} onCompose={() => openCompose()} />
        <div className="detail-slot">
          <AnimatePresence initial={false}>
            {composerOpen ? <motion.div className="detail-layer" key={`composer-${composeSession}`} initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: 0.16 }}>
              <LazyOverlayBoundary resetKey={String(composeSession)} label="写信页面" onDismiss={completeComposer}>
                <Suspense fallback={null}><Composer ref={composerRef} accounts={accounts} seed={composeSeed} onRequestClose={() => { void runAfterComposerSaved(() => undefined); }} onComplete={completeComposer} onRecoveryChange={refreshRecoveries} onNotice={showNotice} /></Suspense>
              </LazyOverlayBoundary>
            </motion.div> : <motion.div className="detail-layer" key="reader" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: 0.16 }}><Reader conversationId={selectedId} mailboxRole={role} accounts={accounts} onBack={() => setSelectedId(undefined)} onOperation={runOperation} onCompose={openCompose} onNotice={showNotice} /></motion.div>}
          </AnimatePresence>
        </div>
      </main>
      <nav className="mobile-tabs" aria-label="主要导航"><button className={role === "inbox" ? "is-active" : ""} onClick={() => navigateMailbox("inbox")}><Inbox size={19} /><span>收件箱</span></button><button className={role === "starred" ? "is-active" : ""} onClick={() => navigateMailbox("starred")}><Star size={19} /><span>星标</span></button><button className={role === "drafts" ? "is-active" : ""} onClick={() => navigateMailbox("drafts")}><FilePenLine size={19} /><span>草稿</span></button><button className={updateQuery.data?.available ? "has-notice" : ""} onClick={() => openSettings("accounts")}><Settings size={19} /><span>设置</span></button></nav>
      <LazyOverlayBoundary resetKey={String(settingsOpen)} label="设置窗口" onDismiss={() => setSettingsOpen(false)}>
        <Suspense fallback={null}>
          {settingsOpen && <SettingsDialog open={settingsOpen} onOpenChange={(value) => { setSettingsOpen(value); if (!value) setOAuthFeedback(undefined); }} accounts={accounts} oauthFeedback={oauthFeedback} initialTab={settingsTab} />}
        </Suspense>
      </LazyOverlayBoundary>
      <AnimatePresence>{notice && <motion.div key={notice.key} className={`toast toast--${notice.tone}`} initial={{ opacity: 0, y: 18, scale: 0.98 }} animate={{ opacity: 1, y: 0, scale: 1 }} exit={{ opacity: 0, y: 10 }} role={notice.tone === "error" ? "alert" : "status"}><span>{notice.message}</span>{notice.operationId && <Button variant="ghost" size="sm" onClick={undo}><Undo2 size={15} />撤销</Button>}<button aria-label="关闭提示" onClick={() => setNotice(undefined)}><X size={15} /></button></motion.div>}</AnimatePresence>
      <UpdateRestartOverlay status={updateQuery.data} queryFailed={updateQuery.isRefetchError} refetch={updateQuery.refetch} />
    </div>
  );
}

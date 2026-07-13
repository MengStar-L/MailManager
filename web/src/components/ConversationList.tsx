import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { useVirtualizer } from "@tanstack/react-virtual";
import { AnimatePresence, LayoutGroup, motion } from "motion/react";
import { Archive, CheckCheck, ChevronDown, Filter, Mail, MailOpen, Paperclip, RefreshCw, Search, Star, Trash2, X } from "lucide-react";
import { memo, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { displayName, formatMailboxDate } from "../lib/format";
import type { DraftListItem } from "../lib/draftRecovery";
import type { AccountSummary, ConversationSummary, Mailbox, OperationKind } from "../types";
import { AccountDot, Avatar, Button, Checkbox, EmptyState, ErrorState, IconButton, Spinner } from "./ui";

export interface ListFilters { unread: boolean; starred: boolean; attachments: boolean }

const narrowViewportQuery = "(max-width: 390px)";

export function conversationRowHeight(compact: boolean, narrow: boolean) {
  if (compact) return 93;
  return narrow ? 108 : 113;
}

function ListSkeleton() {
  return <div className="list-skeleton" aria-label="正在加载会话">{Array.from({ length: 7 }, (_, index) => <div key={index}><span /><div><i /><i /><i /></div></div>)}</div>;
}

const ConversationRow = memo(function ConversationRow({ item, account, selected, checked, trashMode, onSelect, onCheck, onOperation }: {
  item: ConversationSummary;
  account?: AccountSummary;
  selected: boolean;
  checked: boolean;
  trashMode: boolean;
  onSelect: (id: string) => void;
  onCheck: (id: string, value: boolean) => void;
  onOperation: (kind: OperationKind, ids?: string[]) => void;
}) {
  const sender = displayName(item.participants[0]);
  const reduceMotion = document.documentElement.dataset.reduceMotion === "true";
  return (
    <article className={`conversation-row ${selected ? "is-selected" : ""} ${checked ? "is-checked" : ""} ${item.unread ? "is-unread" : ""}`} aria-current={selected ? "true" : undefined}>
      {selected && <motion.div className="conversation-row__selection" layoutId="conversation-selection" transition={{ duration: reduceMotion ? 0 : 0.19, ease: [0.22, 1, 0.36, 1] }} aria-hidden="true" />}
      <button className="conversation-row__open" aria-label={`打开 ${item.subject || "无主题"}`} onClick={() => onSelect(item.id)} />
      <div className="conversation-row__select" onClick={(event) => event.stopPropagation()}><Checkbox label={`选择 ${item.subject}`} checked={checked} onCheckedChange={(value) => onCheck(item.id, value)} /></div>
      <Avatar name={sender} color={account?.color} size={38} />
      <div className="conversation-row__body">
        <div className="conversation-row__meta"><strong>{sender}{item.message_count > 1 && <small>{item.message_count}</small>}</strong><time>{formatMailboxDate(item.last_message_at)}</time></div>
        <div className="conversation-row__subject"><span>{item.subject || "（无主题）"}</span>{item.has_attachments && <Paperclip size={13} />}</div>
        <p>{item.snippet}</p>
        <div className="conversation-row__account"><AccountDot color={account?.color ?? "#6f7c7b"} /><span>{account?.name ?? "邮箱"}</span></div>
      </div>
      <div className="conversation-row__quick" onClick={(event) => event.stopPropagation()}>
        <IconButton label={item.starred ? "取消星标" : "加星标"} active={item.starred} onClick={() => onOperation(item.starred ? "unstar" : "star", [item.id])}><Star size={16} fill={item.starred ? "currentColor" : "none"} /></IconButton>
        <IconButton label={trashMode ? "永久删除" : "归档"} onClick={() => onOperation(trashMode ? "delete" : "archive", [item.id])}>{trashMode ? <Trash2 size={16} /> : <Archive size={16} />}</IconButton>
      </div>
    </article>
  );
});

function DraftRows({ drafts, accounts, search, onDraft, onCompose }: { drafts: DraftListItem[]; accounts: AccountSummary[]; search: string; onDraft: (draft: DraftListItem) => void; onCompose: () => void }) {
  const filtered = drafts.filter((draft) => `${draft.subject} ${draft.to.map((person) => person.name || person.email).join(" ")}`.toLocaleLowerCase().includes(search.toLocaleLowerCase()));
  if (!filtered.length) return <EmptyState title={search ? "没有匹配的草稿" : "草稿箱是空的"} message={search ? "尝试更换搜索词。" : "写下想法，MailManager 会自动保存到你的服务器。"} action={!search ? <Button onClick={onCompose}>写一封邮件</Button> : undefined} />;
  return <div className="draft-rows">{filtered.map((draft) => { const account = accounts.find((item) => item.id === draft.account_id); return <button className={`draft-row draft-row--${draft.source}`} key={`${draft.source}-${draft.id}`} onClick={() => onDraft(draft)}><div className="draft-row__badge">{draft.source === "local" ? "本地待同步" : draft.source === "unreadable" ? "无法恢复" : "草稿"}</div><div><strong>{draft.subject || "（无主题）"}</strong><p>{draft.source === "unreadable" ? "本地加密密钥或数据不可用" : draft.to.length ? `收件人：${draft.to.map((person) => displayName(person)).join("、")}` : "还没有收件人"}</p><span><AccountDot color={account?.color ?? "#687675"} />{account?.name ?? "邮箱"}</span></div><time>{formatMailboxDate(draft.updated_at)}</time></button>; })}</div>;
}

export function ConversationList({ title, subtitle, role, viewKey, items, drafts, accounts, selectedId, checkedIds, search, filters, loading, transitioning, error, refreshing, hasNextPage, loadingMore, onLoadMore, onSearch, onFilters, onSelect, onCheck, onCheckAll, onOperation, onRefresh, onDraft, onCompose }: {
  title: string;
  subtitle: string;
  role: Mailbox["role"];
  viewKey: string;
  items: ConversationSummary[];
  drafts: DraftListItem[];
  accounts: AccountSummary[];
  selectedId?: string;
  checkedIds: Set<string>;
  search: string;
  filters: ListFilters;
  loading: boolean;
  transitioning: boolean;
  error?: string;
  refreshing: boolean;
  hasNextPage: boolean;
  loadingMore: boolean;
  onLoadMore: () => void;
  onSearch: (value: string) => void;
  onFilters: (value: ListFilters) => void;
  onSelect: (id: string) => void;
  onCheck: (id: string, checked: boolean) => void;
  onCheckAll: (checked: boolean) => void;
  onOperation: (kind: OperationKind, ids?: string[]) => void;
  onRefresh: () => void;
  onDraft: (draft: DraftListItem) => void;
  onCompose: () => void;
}) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const animatedViewRef = useRef<string | undefined>(undefined);
  const narrowViewport = useMemo(() => window.matchMedia(narrowViewportQuery), []);
  const virtualizer = useVirtualizer({
    count: items.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => conversationRowHeight(document.documentElement.dataset.density === "compact", narrowViewport.matches),
    overscan: 4,
    initialRect: { width: 416, height: 720 },
    useAnimationFrameWithResizeObserver: true,
  });
  const [enteringRows, setEnteringRows] = useState<ReadonlyMap<string, number>>(() => new Map());
  const filterCount = Number(filters.unread) + Number(filters.starred) + Number(filters.attachments);
  const visibleIds = useMemo(() => items.map((item) => item.id), [items]);
  const accountsById = useMemo(() => new Map(accounts.map((account) => [account.id, account])), [accounts]);
  const virtualItems = virtualizer.getVirtualItems();
  const lastVirtualIndex = virtualItems.length ? virtualItems[virtualItems.length - 1].index : -1;
  useLayoutEffect(() => {
    if (role === "drafts" || loading || transitioning || !items.length || animatedViewRef.current === viewKey) return;
    const nextRows = new Map<string, number>();
    virtualizer.getVirtualItems().forEach((virtualItem, order) => {
      const item = items[virtualItem.index];
      if (item) nextRows.set(item.id, order);
    });
    if (!nextRows.size) return;
    animatedViewRef.current = viewKey;
    setEnteringRows(nextRows);
  }, [items, loading, role, transitioning, viewKey, virtualizer]);
  useEffect(() => {
    if (!enteringRows.size) return;
    const timer = window.setTimeout(() => setEnteringRows(new Map()), 180);
    return () => window.clearTimeout(timer);
  }, [enteringRows]);
  useEffect(() => {
    const measure = () => virtualizer.measure();
    const densityObserver = new MutationObserver(measure);
    densityObserver.observe(document.documentElement, { attributes: true, attributeFilter: ["data-density"] });
    narrowViewport.addEventListener("change", measure);
    return () => {
      densityObserver.disconnect();
      narrowViewport.removeEventListener("change", measure);
    };
  }, [narrowViewport, virtualizer]);
  useEffect(() => {
    if (role !== "drafts" && hasNextPage && !loadingMore && lastVirtualIndex >= items.length - 6) onLoadMore();
  }, [hasNextPage, items.length, lastVirtualIndex, loadingMore, onLoadMore, role]);
  const allChecked = visibleIds.length > 0 && visibleIds.every((id) => checkedIds.has(id));
  const someChecked = visibleIds.some((id) => checkedIds.has(id));

  return (
    <section className="mail-list" aria-label={title} aria-busy={loading || transitioning} data-transitioning={transitioning ? "true" : undefined}>
      <header className="mail-list__header"><div className="mail-list__identity" key={viewKey}><span className="eyebrow">{subtitle}</span><h1>{title}</h1></div><IconButton label="刷新列表" onClick={onRefresh}><RefreshCw className={refreshing ? "spin" : ""} size={18} /></IconButton></header>
      <div className="search-box"><Search size={17} /><input value={search} onChange={(event) => onSearch(event.target.value)} placeholder="搜索所有账户" aria-label="搜索邮件" />{search && <button aria-label="清除搜索" onClick={() => onSearch("")}><X size={15} /></button>}<kbd>/</kbd></div>
      {role !== "drafts" && <div className="list-toolbar">
        <Checkbox label="选择当前列表" checked={allChecked ? true : someChecked ? "indeterminate" : false} onCheckedChange={onCheckAll} />
        <DropdownMenu.Root><DropdownMenu.Trigger asChild><button className={`filter-button ${filterCount ? "is-active" : ""}`}><Filter size={15} /><span>筛选</span>{filterCount > 0 && <em>{filterCount}</em>}<ChevronDown size={13} /></button></DropdownMenu.Trigger><DropdownMenu.Portal><DropdownMenu.Content className="menu-content" align="start" sideOffset={6}><DropdownMenu.CheckboxItem checked={filters.unread} onCheckedChange={(value) => onFilters({ ...filters, unread: value === true })}><DropdownMenu.ItemIndicator>✓</DropdownMenu.ItemIndicator>仅未读</DropdownMenu.CheckboxItem><DropdownMenu.CheckboxItem checked={filters.starred} onCheckedChange={(value) => onFilters({ ...filters, starred: value === true })}><DropdownMenu.ItemIndicator>✓</DropdownMenu.ItemIndicator>仅星标</DropdownMenu.CheckboxItem><DropdownMenu.CheckboxItem checked={filters.attachments} onCheckedChange={(value) => onFilters({ ...filters, attachments: value === true })}><DropdownMenu.ItemIndicator>✓</DropdownMenu.ItemIndicator>有附件</DropdownMenu.CheckboxItem>{filterCount > 0 && <><DropdownMenu.Separator /><DropdownMenu.Item onSelect={() => onFilters({ unread: false, starred: false, attachments: false })}>清除筛选</DropdownMenu.Item></>}</DropdownMenu.Content></DropdownMenu.Portal></DropdownMenu.Root>
        <span className="list-toolbar__count">{items.length} 个会话</span>
      </div>}
        <AnimatePresence>{checkedIds.size > 0 && role !== "drafts" && <motion.div className="bulk-toolbar" role="toolbar" aria-label="批量操作" initial={{ opacity: 0, y: -8 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: -8 }}><strong>已选择 {checkedIds.size} 项</strong><div><IconButton label="标为已读" onClick={() => onOperation("mark_read")}><MailOpen size={17} /></IconButton><IconButton label="标为未读" onClick={() => onOperation("mark_unread")}><Mail size={17} /></IconButton><IconButton label="加星标" onClick={() => onOperation("star")}><Star size={17} /></IconButton>{role !== "trash" && <IconButton label="归档" onClick={() => onOperation("archive")}><Archive size={17} /></IconButton>}<IconButton label={role === "trash" ? "永久删除" : "移到回收站"} onClick={() => onOperation(role === "trash" ? "delete" : "trash")}><Trash2 size={17} /></IconButton><Button variant="ghost" size="sm" onClick={() => onCheckAll(false)}>取消</Button></div></motion.div>}</AnimatePresence>
      {role === "drafts" ? <div className="mail-list__scroll"><DraftRows drafts={drafts} accounts={accounts} search={search} onDraft={onDraft} onCompose={onCompose} /></div> : loading ? <ListSkeleton /> : error ? <ErrorState title="无法加载邮件" message={error} onRetry={onRefresh} /> : items.length === 0 ? <EmptyState title={search || filterCount ? "没有匹配的邮件" : role === "inbox" ? "收件箱已清空" : "这里还没有邮件"} message={search || filterCount ? "试试更换关键词或清除筛选条件。" : "新的邮件到达后会出现在这里。"} action={(search || filterCount > 0) ? <Button variant="secondary" onClick={() => { onSearch(""); onFilters({ unread: false, starred: false, attachments: false }); }}>清除条件</Button> : undefined} /> : (
        <div className="mail-list__scroll" ref={scrollRef}>
          <LayoutGroup id={`conversation-list-${viewKey}`}>
            <div className="virtual-list" style={{ height: virtualizer.getTotalSize() }}>
              {virtualItems.map((virtualItem) => {
                const item = items[virtualItem.index];
                const entryOrder = enteringRows.get(item.id);
                return <div key={item.id} ref={virtualizer.measureElement} data-index={virtualItem.index} className={`virtual-row${entryOrder === undefined ? "" : " virtual-row--enter"}`} style={{ transform: `translateY(${virtualItem.start}px)`, animationDelay: entryOrder === undefined ? undefined : `${Math.min(entryOrder, 6) * 8}ms` }}><ConversationRow item={item} account={accountsById.get(item.account_id)} selected={selectedId === item.id} checked={checkedIds.has(item.id)} trashMode={role === "trash"} onSelect={onSelect} onCheck={onCheck} onOperation={onOperation} /></div>;
              })}
            </div>
          </LayoutGroup>
        </div>
      )}
      {loadingMore && <div className="list-refreshing"><Spinner label="正在加载更多会话" /></div>}
      {refreshing && !loading && <div className="list-refreshing"><Spinner label="同步最新邮件" /></div>}
    </section>
  );
}

import {
  Archive,
  FilePenLine,
  Inbox,
  LogOut,
  MailPlus,
  RefreshCw,
  Send,
  Settings,
  Star,
  Trash2,
} from "lucide-react";
import type { AccountSummary, Mailbox, SystemStatus, SystemUpdateStatus } from "../types";
import { relativeSync } from "../lib/format";
import { AccountDot, Button, IconButton } from "./ui";

const mailboxIcons = { inbox: Inbox, starred: Star, sent: Send, drafts: FilePenLine, archive: Archive, trash: Trash2, custom: Inbox };
const preferredRoles: Mailbox["role"][] = ["inbox", "starred", "sent", "drafts", "archive", "trash"];

export function Sidebar({ accounts, mailboxes, selectedMailbox, selectedAccount, status, updateStatus, onNavigate, onCompose, onSettings, onUpdate, onLogout }: {
  accounts: AccountSummary[];
  mailboxes: Mailbox[];
  selectedMailbox: Mailbox["role"];
  selectedAccount?: string;
  status?: SystemStatus;
  updateStatus?: SystemUpdateStatus;
  onNavigate: (role: Mailbox["role"], accountId?: string) => void;
  onCompose: () => void;
  onSettings: () => void;
  onUpdate: () => void;
  onLogout: () => void;
}) {
  const globalMailboxes = preferredRoles.map((role) => mailboxes.find((mailbox) => mailbox.role === role && !mailbox.account_id)).filter(Boolean) as Mailbox[];
  const updateAvailable = !!updateStatus?.available && !!updateStatus.latest;

  return (
    <aside className="sidebar">
      <header className="sidebar__brand"><div className="brand-mark">M</div><div><strong>MailManager</strong><span>私人邮件台</span></div></header>
      <Button className="compose-button" onClick={onCompose}><MailPlus size={18} /><span>写邮件</span><kbd>C</kbd></Button>
      <nav className="sidebar__nav" aria-label="邮箱文件夹">
        {globalMailboxes.map((mailbox) => {
          const Icon = mailboxIcons[mailbox.role];
          return (
            <button key={mailbox.role} className={`nav-item ${selectedMailbox === mailbox.role && !selectedAccount ? "is-active" : ""}`} onClick={() => onNavigate(mailbox.role)}>
              <Icon size={18} /><span>{mailbox.name}</span>{mailbox.unread_count > 0 && <em>{mailbox.unread_count > 99 ? "99+" : mailbox.unread_count}</em>}
            </button>
          );
        })}
      </nav>
      <div className="sidebar__section-heading"><span>邮箱账户</span><IconButton label="刷新账户状态"><RefreshCw size={15} /></IconButton></div>
      <div className="account-list">
        {accounts.map((account) => (
          <button key={account.id} className={`account-item ${selectedAccount === account.id ? "is-active" : ""}`} onClick={() => onNavigate("inbox", account.id)}>
            <AccountDot color={account.color} status={account.status} />
            <span className="account-item__copy"><strong>{account.name}</strong><small>{account.status === "reauth_required" ? "需要重新认证" : account.status === "error" ? "同步异常" : relativeSync(account.last_synced_at, account.status === "connected" ? "已同步" : undefined)}</small></span>
            {account.unread_count > 0 && <em>{account.unread_count}</em>}
          </button>
        ))}
        {accounts.length === 0 && <button className="account-list__empty" onClick={onSettings}>添加第一个邮箱</button>}
      </div>
      <footer className="sidebar__footer">
        <button type="button" className={`system-pill system-pill--${updateAvailable ? "update" : status?.status ?? "ready"}`} onClick={updateAvailable ? onUpdate : undefined} disabled={!updateAvailable}><span />{updateAvailable ? `可更新 v${updateStatus.latest?.version}` : status?.status === "syncing" ? "正在同步" : status?.status === "degraded" ? "部分服务异常" : "服务正常"}</button>
        <IconButton label="账户与设置" className={updateAvailable ? "has-notice" : ""} onClick={onSettings}><Settings size={18} /></IconButton>
        <IconButton label="退出登录" onClick={onLogout}><LogOut size={18} /></IconButton>
      </footer>
    </aside>
  );
}

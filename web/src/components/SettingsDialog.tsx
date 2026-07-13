import * as Dialog from "@radix-ui/react-dialog";
import * as Switch from "@radix-ui/react-switch";
import * as Tabs from "@radix-ui/react-tabs";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AnimatePresence, motion } from "motion/react";
import { Check, ChevronRight, Cloud, Folder, Plus, RefreshCw, Server, ShieldCheck, Trash2, X } from "lucide-react";
import { type FormEvent, useEffect, useMemo, useState } from "react";
import { api } from "../api/client";
import type { AccountEndpointSummary, AccountInput, AccountProvider, AccountSummary, Mailbox, OAuthProviderSetting } from "../types";
import { relativeSync } from "../lib/format";
import { Select } from "./Select";
import { AccountDot, Button, ErrorState, Field, IconButton, Spinner } from "./ui";
import { SystemUpdatePanel } from "./SystemUpdate";

const providerNames: Record<AccountProvider, string> = { google: "Gmail", microsoft: "Outlook", qq: "QQ 邮箱", "163": "163 邮箱", imap: "其他邮箱" };
const accountColors = ["#2563a6", "#168477", "#dd7155", "#7b61a8", "#b48217"];
const tlsOptions = [{ value: "implicit", label: "SSL / TLS" }, { value: "starttls", label: "STARTTLS" }];

function EndpointEditor({ protocol, value, onChange }: { protocol: "IMAP" | "SMTP"; value: AccountEndpointSummary; onChange: (value: AccountEndpointSummary) => void }) {
  return (
    <fieldset className="connection-endpoint">
      <legend>{protocol}</legend>
      <div className="connection-endpoint__fields">
        <Field label="服务器"><input value={value.host} onChange={(event) => onChange({ ...value, host: event.target.value })} placeholder={`${protocol.toLowerCase()}.example.com`} required /></Field>
        <Field label="端口"><input type="number" min="1" max="65535" value={value.port} onChange={(event) => onChange({ ...value, port: Number(event.target.value) })} required /></Field>
        <Field label="TLS"><Select ariaLabel={`${protocol} TLS`} value={value.tls_mode} options={tlsOptions} onValueChange={(tlsMode) => onChange({ ...value, tls_mode: tlsMode as AccountEndpointSummary["tls_mode"] })} /></Field>
      </div>
    </fieldset>
  );
}

function AccountRow({ account }: { account: AccountSummary }) {
  const queryClient = useQueryClient();
  const [result, setResult] = useState<string>();
  const [editing, setEditing] = useState(false);
  const [username, setUsername] = useState(account.username);
  const [secret, setSecret] = useState("");
  const [imap, setImap] = useState(account.imap);
  const [smtp, setSmtp] = useState(account.smtp);
  const canReauthorize = account.auth_type === "oauth2" && (account.provider === "google" || account.provider === "microsoft");
  const sync = useMutation({ mutationFn: () => api.syncAccount(account.id), onSuccess: () => { setResult("已开始同步"); queryClient.invalidateQueries({ queryKey: ["accounts"] }); } });
  const test = useMutation({ mutationFn: () => api.testAccount(account.id), onSuccess: (value) => setResult(value.message || "连接正常"), onError: (error) => setResult(error instanceof Error ? error.message : "连接失败") });
  const remove = useMutation({ mutationFn: () => api.deleteAccount(account.id), onSuccess: () => { queryClient.invalidateQueries({ queryKey: ["accounts"] }); queryClient.invalidateQueries({ queryKey: ["conversations"] }); } });
  const save = useMutation({
    mutationFn: () => api.updateAccount(account.id, { auth_type: "password", username: username.trim(), secret, imap, smtp }),
    onSuccess: () => { setSecret(""); setResult("连接已保存，正在同步"); queryClient.invalidateQueries({ queryKey: ["accounts"] }); },
    onError: (error) => setResult(error instanceof Error ? error.message : "保存失败"),
  });
  const reauthorize = useMutation({
    mutationFn: () => api.startOAuth(account.provider as "google" | "microsoft", { account_id: account.id }),
    onSuccess: (value) => window.location.assign(value.authorization_url),
    onError: (error) => setResult(error instanceof Error ? error.message : "无法开始授权"),
  });
  useEffect(() => {
    setUsername(account.username);
    setSecret("");
    setImap(account.imap);
    setSmtp(account.smtp);
  }, [account.id, account.username, account.imap, account.smtp]);
  const closeEditor = () => {
    setUsername(account.username);
    setSecret("");
    setImap(account.imap);
    setSmtp(account.smtp);
    setEditing(false);
  };
  return (
    <div className={`settings-account ${account.status === "reauth_required" ? "needs-reauth" : ""} ${editing ? "is-expanded" : ""}`}>
      <AccountDot color={account.color} status={account.status} />
      <div className="settings-account__identity"><strong>{account.name}</strong><span>{account.email}</span><small>{account.status_message || `${providerNames[account.provider]} · ${relativeSync(account.last_synced_at, account.status === "connected" ? "已同步" : undefined)}`}</small>{account.status === "reauth_required" && <em>{canReauthorize ? "需要重新授权" : "需要更新凭据"}</em>}</div>
      <div className="settings-account__actions">
        {account.auth_type === "password" && <Button type="button" variant="ghost" size="sm" aria-label={`编辑连接 ${account.email}`} aria-expanded={editing} onClick={() => editing ? closeEditor() : setEditing(true)}><Server size={15} />{editing ? "收起" : "编辑连接"}</Button>}
        {canReauthorize && <Button type="button" variant={account.status === "reauth_required" ? "secondary" : "ghost"} size="sm" aria-label={`重新授权 ${account.email}`} onClick={() => reauthorize.mutate()} disabled={reauthorize.isPending}><ShieldCheck size={15} />{reauthorize.isPending ? "正在跳转…" : "重新授权"}</Button>}
        <Button variant="ghost" size="sm" onClick={() => test.mutate()} disabled={test.isPending}>{test.isPending ? "测试中…" : "测试"}</Button>
        <IconButton label="立即同步" onClick={() => sync.mutate()} disabled={sync.isPending}><RefreshCw className={sync.isPending ? "spin" : ""} size={16} /></IconButton>
        <IconButton label="删除账户" onClick={() => { if (window.confirm(`确定删除 ${account.email}？本地索引会被清理，但不会删除远端邮件。`)) remove.mutate(); }} disabled={remove.isPending}><Trash2 size={16} /></IconButton>
      </div>
      {result && <div className="settings-account__result"><Check size={13} />{result}</div>}
      <AnimatePresence initial={false}>
        {editing && account.auth_type === "password" && <motion.form className="account-connection-editor" onSubmit={(event) => { event.preventDefault(); save.mutate(); }} initial={{ opacity: 0, y: -6 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: -4 }}>
          <div className="form-grid"><Field label="登录用户名"><input value={username} onChange={(event) => setUsername(event.target.value)} required /></Field><Field label="新应用密码" hint="留空则保持当前密码"><input type="password" value={secret} onChange={(event) => setSecret(event.target.value)} autoComplete="new-password" /></Field></div>
          <div className="connection-endpoints"><EndpointEditor protocol="IMAP" value={imap} onChange={setImap} /><EndpointEditor protocol="SMTP" value={smtp} onChange={setSmtp} /></div>
          {save.isError && <p className="form-error">{save.error instanceof Error ? save.error.message : "保存失败"}</p>}
          <div className="account-connection-editor__actions"><Button type="submit" size="sm" disabled={save.isPending}>{save.isPending ? "保存中…" : "保存连接"}</Button><Button type="button" variant="ghost" size="sm" onClick={closeEditor}>取消</Button></div>
        </motion.form>}
      </AnimatePresence>
    </div>
  );
}

function OAuthConfig({ settings }: { settings: OAuthProviderSetting[] }) {
  const queryClient = useQueryClient();
  return <div className="oauth-config"><div className="oauth-config__heading"><strong>OAuth 应用</strong><span>Secret 只写入加密存储，保存后不会回显。</span></div>{(["google", "microsoft"] as const).map((provider) => <OAuthProviderRow key={provider} provider={provider} setting={settings.find((item) => item.provider === provider)} onSaved={() => queryClient.invalidateQueries({ queryKey: ["oauth-settings"] })} />)}</div>;
}

function OAuthProviderRow({ provider, setting, onSaved }: { provider: "google" | "microsoft"; setting?: OAuthProviderSetting; onSaved: () => void }) {
  const [clientId, setClientId] = useState(setting?.client_id ?? "");
  const [secret, setSecret] = useState("");
  const [editing, setEditing] = useState(!setting?.configured);
  const save = useMutation({ mutationFn: () => api.configureOAuth(provider, { client_id: clientId.trim(), client_secret: secret }), onSuccess: () => { setSecret(""); setEditing(false); onSaved(); } });
  useEffect(() => { if (setting?.configured) { setClientId(setting.client_id ?? ""); setEditing(false); } }, [setting?.configured, setting?.client_id]);
  return <div className="oauth-provider"><div className="oauth-provider__identity"><Cloud size={17} /><div><strong>{providerNames[provider]}</strong><span>{setting?.configured ? `已配置 · ${setting.client_id ?? "Client ID 已保存"}` : "尚未配置"}</span></div><span className={`status-badge ${setting?.configured ? "status-badge--ok" : ""}`}>{setting?.configured ? "可连接" : "待配置"}</span></div>{editing ? <div className="oauth-provider__form"><Field label="Client ID"><input value={clientId} onChange={(event) => setClientId(event.target.value)} autoComplete="off" /></Field><Field label="Client Secret"><input type="password" value={secret} onChange={(event) => setSecret(event.target.value)} autoComplete="new-password" placeholder="保存后不会再次显示" /></Field>{save.error && <p className="form-error">{save.error instanceof Error ? save.error.message : "保存失败"}</p>}<div><Button size="sm" onClick={() => save.mutate()} disabled={!clientId.trim() || !secret || save.isPending}>{save.isPending ? "正在保存…" : "保存配置"}</Button>{setting?.configured && <Button variant="ghost" size="sm" onClick={() => setEditing(false)}>取消</Button>}</div></div> : <Button variant="ghost" size="sm" onClick={() => setEditing(true)}>更新凭据</Button>}</div>;
}

const folderRoleNames: Record<NonNullable<Mailbox["source_role"]>, string> = {
  inbox: "收件箱", sent: "已发送", drafts: "草稿", archive: "归档",
  trash: "回收站", junk: "垃圾邮件", all: "全部邮件", other: "普通文件夹",
};
const folderRoleOptions = Object.entries(folderRoleNames).map(([value, label]) => ({ value, label }));

function FolderRoleRow({ mailbox }: { mailbox: Mailbox }) {
  const queryClient = useQueryClient();
  const [role, setRole] = useState<NonNullable<Mailbox["source_role"]> | "">("");
  const save = useMutation({
    mutationFn: () => api.updateFolderRole(mailbox.id, role as NonNullable<Mailbox["source_role"]>),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["mailboxes"] }),
  });
  return <div className="folder-mapping__row"><Folder size={17} /><div><strong>{mailbox.name}</strong><span>需要确认这个远端文件夹的用途</span></div><Select ariaLabel={`设置 ${mailbox.name} 用途`} size="compact" value={role} placeholder="选择用途" options={folderRoleOptions} onValueChange={(value) => setRole(value as NonNullable<Mailbox["source_role"]>)} /><Button size="sm" variant="secondary" disabled={!role || save.isPending} onClick={() => save.mutate()}>{save.isPending ? "保存中…" : "确认"}</Button>{save.isError && <p className="form-error">{save.error instanceof Error ? save.error.message : "无法保存文件夹用途"}</p>}</div>;
}

function FolderMappings({ mailboxes }: { mailboxes: Mailbox[] }) {
  const pending = mailboxes.filter((mailbox) => mailbox.account_id && mailbox.role_source === "needs_user");
  if (!pending.length) return null;
  return <div className="folder-mapping"><div className="oauth-config__heading"><strong>确认文件夹用途</strong><span>用于归档、已发送与回收站操作；确认后不会被下次同步覆盖。</span></div>{pending.map((mailbox) => <FolderRoleRow key={mailbox.id} mailbox={mailbox} />)}</div>;
}

function AddAccount({ onDone, oauthSettings }: { onDone: () => void; oauthSettings: OAuthProviderSetting[] }) {
  const queryClient = useQueryClient();
  const [provider, setProvider] = useState<AccountProvider>("google");
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [imap, setImap] = useState<AccountEndpointSummary>({ host: "", port: 993, tls_mode: "implicit" });
  const [smtp, setSmtp] = useState<AccountEndpointSummary>({ host: "", port: 587, tls_mode: "starttls" });
  const [color, setColor] = useState(accountColors[0]);
  const usesOAuth = provider === "google" || provider === "microsoft";
  const oauthReady = !usesOAuth || oauthSettings.find((item) => item.provider === provider)?.configured === true;
  const preset = useMemo(() => provider === "qq" ? {
    imap: { host: "imap.qq.com", port: 993, tls_mode: "implicit" as const },
    smtp: { host: "smtp.qq.com", port: 465, tls_mode: "implicit" as const },
  } : provider === "163" ? {
    imap: { host: "imap.163.com", port: 993, tls_mode: "implicit" as const },
    smtp: { host: "smtp.163.com", port: 465, tls_mode: "implicit" as const },
  } : undefined, [provider]);
  const create = useMutation({
    mutationFn: (input: AccountInput) => api.createAccount(input),
    onSuccess: () => { queryClient.invalidateQueries({ queryKey: ["accounts"] }); queryClient.invalidateQueries({ queryKey: ["mailboxes"] }); onDone(); },
  });
  const oauth = useMutation({ mutationFn: () => api.startOAuth(provider as "google" | "microsoft", { display_name: name || email.split("@")[0], email, color }), onSuccess: (value) => window.location.assign(value.authorization_url) });
  const connectionError = create.error || oauth.error;

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (usesOAuth) {
      oauth.mutate();
      return;
    }
    const input: AccountInput = {
      provider,
      auth_type: "password",
      name: name || email.split("@")[0],
      email,
      color,
      username: username.trim() || email,
      secret: password,
      imap: preset?.imap ?? imap,
      smtp: preset?.smtp ?? smtp,
    };
    create.mutate(input);
  };

  return (
    <motion.form className="add-account" onSubmit={submit} initial={{ opacity: 0, x: 10 }} animate={{ opacity: 1, x: 0 }}>
      <button type="button" className="back-link" onClick={onDone}>‹ 返回账户</button>
      <div><span className="eyebrow">连接新邮箱</span><h2>选择邮件服务</h2><p>OAuth 账户不会把主密码交给 MailManager。</p></div>
      <div className="provider-grid">
        {(Object.keys(providerNames) as AccountProvider[]).map((value) => <button className={provider === value ? "is-active" : ""} type="button" key={value} onClick={() => setProvider(value)}>{value === "imap" ? <Server size={18} /> : <Cloud size={18} />}<span>{providerNames[value]}</span><ChevronRight size={15} /></button>)}
      </div>
      <div className="form-grid">
        <Field label="账户名称"><input value={name} onChange={(e) => setName(e.target.value)} placeholder="例如：工作" /></Field>
        <Field label="邮箱地址"><input type="email" value={email} onChange={(e) => setEmail(e.target.value)} autoComplete="email" required /></Field>
      </div>
      {!usesOAuth && <>
        <div className="form-grid"><Field label="登录用户名" hint="默认使用邮箱地址"><input value={username} onChange={(event) => setUsername(event.target.value)} placeholder={email || "user@example.com"} /></Field><Field label={provider === "qq" || provider === "163" ? "授权码 / 应用密码" : "应用密码"}><input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" required /></Field></div>
        {!preset && <div className="connection-endpoints"><EndpointEditor protocol="IMAP" value={imap} onChange={setImap} /><EndpointEditor protocol="SMTP" value={smtp} onChange={setSmtp} /></div>}
      </>}
      <Field label="账户颜色"><div className="color-swatches">{accountColors.map((value) => <button type="button" aria-label={`选择颜色 ${value}`} className={color === value ? "is-active" : ""} style={{ backgroundColor: value }} key={value} onClick={() => setColor(value)} />)}</div></Field>
      {!oauthReady && <p className="form-error">请先返回账户列表，配置 {providerNames[provider]} OAuth 应用。</p>}
      {connectionError && <p className="form-error">{connectionError instanceof Error ? connectionError.message : "无法添加账户"}</p>}
      <Button size="lg" disabled={create.isPending || oauth.isPending || !oauthReady}>{usesOAuth ? <ShieldCheck size={18} /> : <Plus size={18} />}{usesOAuth ? oauth.isPending ? "正在准备授权…" : `使用 ${providerNames[provider]} 安全连接` : create.isPending ? "正在连接…" : "验证并添加账户"}</Button>
    </motion.form>
  );
}

export function SettingsDialog({ open, onOpenChange, accounts, oauthFeedback, initialTab = "accounts" }: { open: boolean; onOpenChange: (open: boolean) => void; accounts: AccountSummary[]; oauthFeedback?: { message: string; tone: "success" | "error" | "neutral" }; initialTab?: "accounts" | "preferences" | "security" | "update" }) {
  const [adding, setAdding] = useState(false);
  const [tab, setTab] = useState(initialTab);
  const [compact, setCompact] = useState(() => localStorage.getItem("mailmanager-density") === "compact");
  const [reduceMotion, setReduceMotion] = useState(() => localStorage.getItem("mailmanager-reduce-motion") === "true");
  const oauthSettings = useQuery({ queryKey: ["oauth-settings"], queryFn: api.getOAuthSettings });
  const mailboxes = useQuery({ queryKey: ["mailboxes"], queryFn: api.getMailboxes });
  const toggleCompact = (value: boolean) => { setCompact(value); localStorage.setItem("mailmanager-density", value ? "compact" : "comfortable"); document.documentElement.dataset.density = value ? "compact" : "comfortable"; };
  const toggleMotion = (value: boolean) => { setReduceMotion(value); localStorage.setItem("mailmanager-reduce-motion", String(value)); document.documentElement.dataset.reduceMotion = String(value); };
  useEffect(() => { if (open) setTab(initialTab); }, [initialTab, open]);

  return (
    <Dialog.Root open={open} onOpenChange={(value) => { onOpenChange(value); if (!value) setAdding(false); }}>
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content className="settings-dialog" aria-describedby="settings-description">
          <div className="dialog-heading"><div><Dialog.Title>设置</Dialog.Title><Dialog.Description id="settings-description">管理邮箱连接与个人使用偏好</Dialog.Description></div><Dialog.Close asChild><IconButton label="关闭设置"><X size={19} /></IconButton></Dialog.Close></div>
          {oauthFeedback && <div className={`settings-feedback settings-feedback--${oauthFeedback.tone}`} role={oauthFeedback.tone === "error" ? "alert" : "status"}><Check size={15} />{oauthFeedback.message}</div>}
          <Tabs.Root className="settings-tabs" value={tab} onValueChange={(value) => setTab(value as typeof tab)}>
            <Tabs.List aria-label="设置分类"><Tabs.Trigger value="accounts">邮箱账户</Tabs.Trigger><Tabs.Trigger value="preferences">使用偏好</Tabs.Trigger><Tabs.Trigger value="security">安全</Tabs.Trigger><Tabs.Trigger value="update">系统更新</Tabs.Trigger></Tabs.List>
            <Tabs.Content value="accounts" className="settings-pane">
              <AnimatePresence mode="wait">
                {adding ? <AddAccount key="add" onDone={() => setAdding(false)} oauthSettings={oauthSettings.data?.items ?? []} /> : <motion.div key="list" initial={{ opacity: 0 }} animate={{ opacity: 1 }}><div className="pane-heading"><div><h2>已连接账户</h2><p>分别测试收信与发信连接，或手动开始同步。</p></div><Button size="sm" onClick={() => setAdding(true)}><Plus size={15} />添加邮箱</Button></div><div className="settings-account-list">{accounts.map((account) => <AccountRow key={account.id} account={account} />)}{accounts.length === 0 && <div className="settings-empty">还没有连接邮箱。</div>}</div><FolderMappings mailboxes={mailboxes.data?.items ?? []} />{oauthSettings.isPending ? <div className="oauth-loading"><Spinner label="正在读取 OAuth 配置" /></div> : oauthSettings.isError ? <ErrorState title="无法读取 OAuth 配置" message={oauthSettings.error instanceof Error ? oauthSettings.error.message : undefined} onRetry={() => oauthSettings.refetch()} /> : <OAuthConfig settings={oauthSettings.data.items} />}</motion.div>}
              </AnimatePresence>
            </Tabs.Content>
            <Tabs.Content value="preferences" className="settings-pane"><div className="pane-heading"><div><h2>界面偏好</h2><p>设置只保存在当前浏览器。</p></div></div><div className="setting-row"><div><strong>紧凑列表</strong><span>在相同高度显示更多会话</span></div><Switch.Root checked={compact} onCheckedChange={toggleCompact} aria-label="紧凑列表"><Switch.Thumb /></Switch.Root></div><div className="setting-row"><div><strong>减少动态效果</strong><span>关闭推入、展开与重排动画</span></div><Switch.Root checked={reduceMotion} onCheckedChange={toggleMotion} aria-label="减少动态效果"><Switch.Thumb /></Switch.Root></div></Tabs.Content>
            <Tabs.Content value="security" className="settings-pane"><div className="security-note"><ShieldCheck size={24} /><div><h2>端到端由你掌控</h2><p>邮箱凭据、OAuth token 与 TOTP 密钥使用服务器主密钥加密。正文索引依赖服务器磁盘加密保护。</p></div></div><div className="setting-row"><div><strong>双重验证</strong><span>管理员账户已启用 TOTP</span></div><span className="status-badge status-badge--ok">已启用</span></div><div className="setting-row"><div><strong>活跃会话</strong><span>当前浏览器 · 最近活动</span></div><span className="status-badge">当前</span></div></Tabs.Content>
            <Tabs.Content value="update" className="settings-pane"><SystemUpdatePanel /></Tabs.Content>
          </Tabs.Root>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

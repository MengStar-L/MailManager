import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { keepPreviousData, useQuery, useQueryClient } from "@tanstack/react-query";
import { AnimatePresence, motion } from "motion/react";
import { Archive, ArrowLeft, ChevronDown, Download, Ellipsis, Forward, Mail, MailOpen, Paperclip, Reply, ReplyAll, Star, Trash2, ZoomIn, ZoomOut } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { api } from "../api/client";
import { displayName, formatBytes, formatFullDate } from "../lib/format";
import type { AccountSummary, ConversationDetail, MessageBody, MessageDetail, OperationKind } from "../types";
import type { ComposeSeed } from "./Composer";
import { AccountDot, Avatar, Button, EmptyState, ErrorState, IconButton, Spinner } from "./ui";

const bodyScaleStorageKey = "mailmanager-body-scale";
const defaultBodyScale = 85;
const minimumBodyScale = 70;
const maximumBodyScale = 120;
const bodyScaleStep = 5;

function normalizeBodyScale(value: number): number {
  if (!Number.isFinite(value)) return defaultBodyScale;
  const stepped = Math.round(value / bodyScaleStep) * bodyScaleStep;
  return Math.min(maximumBodyScale, Math.max(minimumBodyScale, stepped));
}

function storedBodyScale(): number {
  const stored = localStorage.getItem(bodyScaleStorageKey);
  return stored?.trim() ? normalizeBodyScale(Number(stored)) : defaultBodyScale;
}

function safeDocument(html: string): string {
  const csp = "default-src 'none'; img-src https: data:; style-src 'unsafe-inline' https:; font-src https: data:; media-src https:; frame-src 'none'; form-action 'none'; base-uri 'none'; script-src 'none'";
  return `<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="${csp}"><meta name="color-scheme" content="light"><style>html{font:15px/1.7 system-ui,sans-serif;color:#27313a;background:#fff}body{margin:0;padding:2px 2px 18px;overflow-wrap:anywhere}a{color:#165f83}img{max-width:100%;height:auto}blockquote{border-left:3px solid #d9dfdc;margin-left:0;padding-left:16px;color:#68716f}pre{white-space:pre-wrap}</style></head><body>${html}</body></html>`;
}

function escapeHtml(value: string): string {
  return value.replace(/[&<>"']/g, (character) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[character]!);
}

export function messageBodyContent(body: MessageBody): string {
  if (body.html.trim()) return body.html;
  return `<pre>${escapeHtml(body.plain_text)}</pre>`;
}

function MessageBodyFrame({ message, onLoad, bodyScale }: { message: MessageDetail; onLoad: () => void; bodyScale: number }) {
  const body = useQuery({ queryKey: ["message-body", message.id, true], queryFn: () => api.getMessageBody(message.id, true) });
  if (body.isPending) return <div className="message-body-loading"><Spinner label="正在加载正文" /></div>;
  if (body.isError) return <ErrorState title="正文加载失败" message={body.error instanceof Error ? body.error.message : undefined} onRetry={() => body.refetch()} />;
  const scale = bodyScale / 100;
  const frameSize = `${(100 / scale).toFixed(4)}%`;
  return <div className="message-body"><iframe className="message-body-frame" title={`${message.subject} 的邮件正文`} sandbox="" referrerPolicy="no-referrer" srcDoc={safeDocument(messageBodyContent(body.data))} style={{ width: frameSize, height: frameSize, zoom: scale }} onLoad={onLoad} /></div>;
}

function MessageCard({ message, account, defaultOpen, bodyScale, onReply, onForward }: { message: MessageDetail; account?: AccountSummary; defaultOpen: boolean; bodyScale: number; onReply: (all: boolean) => void; onForward: () => void }) {
  const [open, setOpen] = useState(defaultOpen);
  const cardRef = useRef<HTMLElement>(null);
  const reduceMotion = document.documentElement.dataset.reduceMotion === "true";
  const recipients = useMemo(() => [...message.to, ...(message.cc ?? [])], [message]);
  const keepInView = () => cardRef.current?.scrollIntoView?.({ block: "nearest" });
  useEffect(() => {
    if (!open) return;
    const frame = requestAnimationFrame(() => cardRef.current?.scrollIntoView?.({ block: "start" }));
    return () => cancelAnimationFrame(frame);
  }, [open]);
  return (
    <article ref={cardRef} className={`message-card ${open ? "is-open" : ""}`}>
      <button className="message-card__header" onClick={() => setOpen((value) => !value)} aria-expanded={open}>
        <Avatar name={displayName(message.from)} color={account?.color} size={40} />
        <div><div><strong>{displayName(message.from)}</strong><span>&lt;{message.from.email}&gt;</span></div><p>{open ? <>发送给 {recipients.map((item) => displayName(item)).join("、") || "我"}</> : message.snippet}</p></div>
        <div><time>{formatFullDate(message.sent_at)}</time><ChevronDown size={16} /></div>
      </button>
      <AnimatePresence initial={false}>{open && <motion.div className="message-card__content" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0, transition: { duration: reduceMotion ? 0 : 0.1 } }} transition={{ duration: reduceMotion ? 0 : 0.16, ease: "easeOut" }}><div className="message-card__body"><MessageBodyFrame message={message} bodyScale={bodyScale} onLoad={keepInView} />{message.attachments.length > 0 && <div className="attachment-list"><h4><Paperclip size={15} />{message.attachments.length} 个附件</h4>{message.attachments.map((attachment) => <a href={api.attachmentUrl(attachment.id)} download key={attachment.id}><span className="attachment-type">{attachment.filename.split(".").pop()?.slice(0, 4).toUpperCase()}</span><span><strong>{attachment.filename}</strong><small>{formatBytes(attachment.size)}</small></span><Download size={17} /></a>)}</div>}</div><div className="message-card__actions"><Button variant="secondary" size="sm" onClick={() => onReply(false)}><Reply size={16} />回复</Button><Button variant="ghost" size="sm" onClick={() => onReply(true)}><ReplyAll size={16} />回复全部</Button><Button variant="ghost" size="sm" onClick={onForward}><Forward size={16} />转发</Button></div></motion.div>}</AnimatePresence>
    </article>
  );
}

export function makeReplySeed(detail: ConversationDetail, message: MessageDetail, account: AccountSummary | undefined, all: boolean, body: MessageBody): ComposeSeed {
  const own = new Set([account?.email.toLocaleLowerCase()].filter(Boolean));
  const primary = message.reply_to ?? message.from;
  const allRecipients = [primary, ...message.to, ...(message.cc ?? [])].filter((address, index, values) => !own.has(address.email.toLocaleLowerCase()) && values.findIndex((item) => item.email.toLocaleLowerCase() === address.email.toLocaleLowerCase()) === index);
  return { account_id: message.account_id, reply_to_message_id: message.id, to: all ? allRecipients : [primary], subject: detail.conversation.subject.match(/^Re:/i) ? detail.conversation.subject : `Re: ${detail.conversation.subject}`, body_html: `<p></p><blockquote><p>${escapeHtml(formatFullDate(message.sent_at))}，${escapeHtml(displayName(message.from))} 写道：</p>${messageBodyContent(body)}</blockquote>` };
}

export function makeForwardSeed(detail: ConversationDetail, message: MessageDetail, body: MessageBody): ComposeSeed {
  const subject = detail.conversation.subject.match(/^Fwd:/i) ? detail.conversation.subject : `Fwd: ${detail.conversation.subject}`;
  const recipients = message.to.map((item) => `${displayName(item)} <${item.email}>`).join("、");
  return { account_id: message.account_id, forward_message_id: message.id, forward_attachments: message.attachments, subject, body_html: `<p></p><blockquote><p>---------- 转发邮件 ----------</p><p><strong>发件人：</strong>${escapeHtml(displayName(message.from))} &lt;${escapeHtml(message.from.email)}&gt;<br><strong>日期：</strong>${escapeHtml(formatFullDate(message.sent_at))}<br><strong>主题：</strong>${escapeHtml(message.subject)}<br><strong>收件人：</strong>${escapeHtml(recipients)}</p>${messageBodyContent(body)}</blockquote>` };
}

export function Reader({ conversationId, mailboxRole, accounts, onBack, onOperation, onCompose, onNotice }: { conversationId?: string; mailboxRole: string; accounts: AccountSummary[]; onBack: () => void; onOperation: (kind: OperationKind, ids: string[]) => void; onCompose: (seed: ComposeSeed) => void; onNotice: (message: string, tone?: "error" | "neutral") => void }) {
  const queryClient = useQueryClient();
  const [bodyScale, setBodyScale] = useState(storedBodyScale);
  useEffect(() => { localStorage.setItem(bodyScaleStorageKey, String(bodyScale)); }, [bodyScale]);
  const detail = useQuery({ queryKey: ["conversation", conversationId], queryFn: () => api.getConversation(conversationId!), enabled: !!conversationId, placeholderData: keepPreviousData });
  if (!conversationId) return <section className="reader reader--empty reader--empty-enter"><EmptyState title="选择一封邮件开始阅读" message="会话会在这里展开，列表位置保持不变。" /></section>;
  if (detail.isPending) return <section className="reader"><div className="reader-loader"><Spinner label="正在打开会话" /></div></section>;
  if (detail.isError) return <section className="reader"><div className="reader-loader"><ErrorState title="无法打开会话" message={detail.error instanceof Error ? detail.error.message : undefined} onRetry={() => detail.refetch()} /></div></section>;
  const { conversation, messages } = detail.data;
  const transitioning = detail.isPlaceholderData;
  const reduceMotion = document.documentElement.dataset.reduceMotion === "true";
  const account = accounts.find((item) => item.id === conversation.account_id);
  const composeReply = async (message: MessageDetail, all: boolean) => {
    try {
      const body = await queryClient.fetchQuery({ queryKey: ["message-body", message.id, false], queryFn: () => api.getMessageBody(message.id, false) });
      onCompose(makeReplySeed(detail.data, message, account, all, body));
    } catch (error) {
      onNotice(error instanceof Error ? error.message : "无法读取原邮件正文", "error");
    }
  };
  const composeForward = async (message: MessageDetail) => {
    try {
      const body = await queryClient.fetchQuery({ queryKey: ["message-body", message.id, false], queryFn: () => api.getMessageBody(message.id, false) });
      onCompose(makeForwardSeed(detail.data, message, body));
    } catch (error) {
      onNotice(error instanceof Error ? error.message : "无法读取原邮件正文", "error");
    }
  };
  return (
    <section className="reader" aria-busy={transitioning}>
      <header className="reader__toolbar">
        <div className="reader__toolbar-start"><IconButton label="返回邮件列表" className="reader-back" onClick={onBack}><ArrowLeft size={19} /></IconButton>{mailboxRole !== "trash" && <IconButton label="归档" onClick={() => onOperation("archive", [conversation.id])}><Archive size={18} /></IconButton>}<IconButton label={conversation.unread ? "标为已读" : "标为未读"} onClick={() => onOperation(conversation.unread ? "mark_read" : "mark_unread", [conversation.id])}>{conversation.unread ? <MailOpen size={18} /> : <Mail size={18} />}</IconButton><IconButton label={conversation.starred ? "取消星标" : "加星标"} active={conversation.starred} onClick={() => onOperation(conversation.starred ? "unstar" : "star", [conversation.id])}><Star size={18} fill={conversation.starred ? "currentColor" : "none"} /></IconButton></div>
        <div className="reader__toolbar-title"><AnimatePresence initial={false}><motion.div className="reader__toolbar-title-content" key={conversation.id} initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: reduceMotion ? 0 : 0.14, ease: "easeOut" }}><strong title={conversation.subject}>{conversation.subject || "（无主题）"}</strong><span><span className="reader__toolbar-account"><AccountDot color={account?.color ?? "#687675"} />{account?.name ?? "邮箱"}</span><span>· {conversation.message_count} 封邮件</span><span className="reader__toolbar-participants">· {conversation.participants.map((person) => displayName(person)).join("、")}</span></span></motion.div></AnimatePresence></div>
        <div className="reader__toolbar-end"><div className="reader__zoom"><IconButton label="缩小邮件正文" disabled={bodyScale <= minimumBodyScale} onClick={() => setBodyScale((current) => normalizeBodyScale(current - bodyScaleStep))}><ZoomOut size={16} /></IconButton><output aria-label="邮件正文缩放" aria-live="polite">{bodyScale}%</output><IconButton label="放大邮件正文" disabled={bodyScale >= maximumBodyScale} onClick={() => setBodyScale((current) => normalizeBodyScale(current + bodyScaleStep))}><ZoomIn size={16} /></IconButton></div><IconButton label={mailboxRole === "trash" ? "永久删除" : "移到回收站"} onClick={() => onOperation(mailboxRole === "trash" ? "delete" : "trash", [conversation.id])}><Trash2 size={18} /></IconButton><DropdownMenu.Root><DropdownMenu.Trigger asChild><IconButton label="更多操作"><Ellipsis size={19} /></IconButton></DropdownMenu.Trigger><DropdownMenu.Portal><DropdownMenu.Content className="menu-content" align="end"><DropdownMenu.Item onSelect={() => onOperation("mark_unread", [conversation.id])}>标为未读</DropdownMenu.Item><DropdownMenu.Item onSelect={() => navigator.clipboard.writeText(conversation.subject)}>复制邮件主题</DropdownMenu.Item><DropdownMenu.Separator /><DropdownMenu.Item className="menu-danger" onSelect={() => onOperation(mailboxRole === "trash" ? "delete" : "trash", [conversation.id])}>{mailboxRole === "trash" ? "永久删除" : "移到回收站"}</DropdownMenu.Item></DropdownMenu.Content></DropdownMenu.Portal></DropdownMenu.Root></div>
      </header>
      {transitioning && <div className="reader__progress" aria-hidden="true"><span /></div>}
      <div className="reader__stage">
        <AnimatePresence initial={false}>
          <motion.div className={`reader__scroll${messages.length === 1 ? " reader__scroll--single" : ""}`} key={conversation.id} initial={{ opacity: 0 }} animate={{ opacity: 1, transition: { duration: reduceMotion ? 0 : 0.2, ease: "easeOut" } }} exit={{ opacity: 0, transition: { duration: reduceMotion ? 0 : 0.12, ease: "easeOut" } }}>
            <div className="message-thread">{messages.map((message, index) => <MessageCard key={message.id} message={message} account={accounts.find((item) => item.id === message.account_id)} defaultOpen={index === messages.length - 1} bodyScale={bodyScale} onReply={(all) => composeReply(message, all)} onForward={() => composeForward(message)} />)}</div>
          </motion.div>
        </AnimatePresence>
      </div>
    </section>
  );
}

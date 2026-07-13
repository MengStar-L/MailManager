import { useMutation, useQueryClient } from "@tanstack/react-query";
import { EditorContent, useEditor } from "@tiptap/react";
import Placeholder from "@tiptap/extension-placeholder";
import StarterKit from "@tiptap/starter-kit";
import { AnimatePresence, motion } from "motion/react";
import { Bold, Italic, Link, List, ListOrdered, Paperclip, Send, Trash2, Underline as UnderlineIcon, X } from "lucide-react";
import { forwardRef, type ChangeEvent, useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState } from "react";
import { api, demoMode } from "../api/client";
import { addressesToString, formatBytes, parseAddresses } from "../lib/format";
import { deleteDraftRecovery, hasMeaningfulDraftContent, saveDraftRecovery, type RecoveryDraft } from "../lib/draftRecovery";
import type { AccountSummary, Attachment, Draft, DraftInput, MailAddress } from "../types";
import { Select } from "./Select";
import { AccountDot, Button, IconButton } from "./ui";

export interface ComposeSeed {
  id?: string;
  recovery_id?: string;
  recovery_revision?: number;
  account_id?: string;
  reply_to_message_id?: string;
  forward_message_id?: string;
  to?: MailAddress[];
  cc?: MailAddress[];
  bcc?: MailAddress[];
  subject?: string;
  body_html?: string;
  body_text?: string;
  attachments?: Attachment[];
  pending_files?: File[];
  forward_attachments?: Attachment[];
}

export type ComposerLeaveResult = "empty" | "server-saved" | "local-saved" | "blocked";

export interface ComposerHandle {
  saveBeforeLeave: () => Promise<ComposerLeaveResult>;
  focus: () => void;
}

type SaveStatus = "idle" | "local-saving" | "local-saved" | "server-saving" | "server-saved" | "error";

function localId() {
  return globalThis.crypto?.randomUUID?.() ?? `local-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

function textFromHTML(html = "") {
  if (!html) return "";
  const container = document.createElement("div");
  container.innerHTML = html;
  return container.textContent ?? "";
}

function ComposerToolbar({ editor }: { editor: ReturnType<typeof useEditor> }) {
  if (!editor || editor.isDestroyed) return null;
  const addLink = () => {
    const value = window.prompt("输入链接地址", "https://");
    if (!value) return;
    editor.chain().focus().setLink({ href: value }).run();
  };
  return (
    <div className="composer-toolbar" aria-label="正文格式">
      <IconButton label="粗体" active={editor.isActive("bold")} onClick={() => editor.chain().focus().toggleBold().run()}><Bold size={16} /></IconButton>
      <IconButton label="斜体" active={editor.isActive("italic")} onClick={() => editor.chain().focus().toggleItalic().run()}><Italic size={16} /></IconButton>
      <IconButton label="下划线" active={editor.isActive("underline")} onClick={() => editor.chain().focus().toggleUnderline().run()}><UnderlineIcon size={16} /></IconButton>
      <span className="toolbar-separator" />
      <IconButton label="项目符号列表" active={editor.isActive("bulletList")} onClick={() => editor.chain().focus().toggleBulletList().run()}><List size={16} /></IconButton>
      <IconButton label="编号列表" active={editor.isActive("orderedList")} onClick={() => editor.chain().focus().toggleOrderedList().run()}><ListOrdered size={16} /></IconButton>
      <IconButton label="添加链接" active={editor.isActive("link")} onClick={addLink}><Link size={16} /></IconButton>
    </div>
  );
}

export const Composer = forwardRef<ComposerHandle, {
  accounts: AccountSummary[];
  seed?: ComposeSeed;
  onRequestClose: () => void;
  onComplete: () => void;
  onRecoveryChange: () => void | Promise<void>;
  onNotice: (message: string, tone?: "success" | "error" | "neutral") => void;
}>(function Composer({ accounts, seed, onRequestClose, onComplete, onRecoveryChange, onNotice }, ref) {
  const queryClient = useQueryClient();
  const defaultAccountId = accounts[0]?.id ?? "";
  const [draftId, setDraftId] = useState(seed?.id);
  const [accountId, setAccountId] = useState(seed?.account_id || defaultAccountId);
  const [to, setTo] = useState(() => addressesToString(seed?.to ?? []));
  const [cc, setCc] = useState(() => addressesToString(seed?.cc ?? []));
  const [bcc, setBcc] = useState(() => addressesToString(seed?.bcc ?? []));
  const [subject, setSubject] = useState(seed?.subject ?? "");
  const [bodyHtml, setBodyHtml] = useState(seed?.body_html ?? "");
  const [bodyText, setBodyText] = useState(seed?.body_text ?? textFromHTML(seed?.body_html));
  const [showCarbonCopy, setShowCarbonCopy] = useState(!!seed?.cc?.length || !!seed?.bcc?.length);
  const [remoteAttachments, setRemoteAttachments] = useState<Attachment[]>(seed?.attachments ?? []);
  const [pendingFiles, setPendingFiles] = useState<File[]>(seed?.pending_files ?? []);
  const initialRevision = seed?.recovery_revision ?? 0;
  const startsDirty = !seed?.id && !seed?.recovery_id && (!!seed?.reply_to_message_id || !!seed?.forward_message_id);
  const [revision, setRevision] = useState(startsDirty ? Math.max(1, initialRevision) : initialRevision);
  const [dirty, setDirty] = useState(startsDirty);
  const [saveStatus, setSaveStatus] = useState<SaveStatus>(seed?.recovery_id ? "local-saved" : seed?.id ? "server-saved" : startsDirty ? "local-saving" : "idle");
  const fileInput = useRef<HTMLInputElement>(null);
  const recipientInput = useRef<HTMLInputElement>(null);
  const localIdRef = useRef(seed?.recovery_id ?? localId());
  const serverIdRef = useRef(seed?.id);
  const revisionRef = useRef(startsDirty ? Math.max(1, initialRevision) : initialRevision);
  const serverSavedRevisionRef = useRef(seed?.id && !seed?.recovery_id ? initialRevision : -1);
  const localSavedRevisionRef = useRef(seed?.recovery_id ? initialRevision : -1);
  const localQueueRef = useRef<Promise<void>>(Promise.resolve());
  const serverQueueRef = useRef<Promise<void>>(Promise.resolve());
  const uploadedFilesRef = useRef(new WeakSet<File>());
  const remoteAttachmentsRef = useRef(remoteAttachments);
  const latestSnapshotRef = useRef<RecoveryDraft | undefined>(undefined);

  const accountOptions = useMemo(() => accounts.map((account) => ({ value: account.id, label: `${account.name} · ${account.email}`, leading: <AccountDot color={account.color} /> })), [accounts]);
  const markDirty = useCallback(() => {
    revisionRef.current += 1;
    setRevision(revisionRef.current);
    setDirty(true);
    setSaveStatus("local-saving");
  }, []);
  const editor = useEditor({
    extensions: [StarterKit, Placeholder.configure({ placeholder: "写下清晰、简洁的内容…" })],
    content: seed?.body_html ?? "",
    onUpdate: ({ editor: current }) => {
      setBodyHtml(current.getHTML());
      setBodyText(current.getText());
      markDirty();
    },
  });

  useEffect(() => {
    if (!accountId && defaultAccountId) setAccountId(defaultAccountId);
  }, [accountId, defaultAccountId]);

  useEffect(() => {
    if (!seed?.forward_attachments?.length) return;
    const controller = new AbortController();
    const attachments = seed.forward_attachments;
    const advertisedBytes = attachments.reduce((total, attachment) => total + attachment.size, 0);
    if (advertisedBytes > 25 * 1024 * 1024) {
      onNotice("原附件总大小超过 25 MB，请手动选择需要转发的文件", "error");
      return () => controller.abort();
    }
    const load = async () => {
      const files: File[] = [];
      let total = 0;
      for (const attachment of attachments) {
        if (demoMode) {
          files.push(new File([new Uint8Array(attachment.size)], attachment.filename, { type: attachment.content_type }));
          continue;
        }
        const response = await fetch(api.attachmentUrl(attachment.id), { credentials: "same-origin", signal: controller.signal });
        if (!response.ok) throw new Error(`无法下载原附件 ${attachment.filename}`);
        const blob = await response.blob();
        total += blob.size;
        if (total > 25 * 1024 * 1024) throw new Error("原附件总大小超过 25 MB");
        files.push(new File([blob], attachment.filename, { type: attachment.content_type || blob.type }));
      }
      if (!controller.signal.aborted) {
        setPendingFiles(files);
        markDirty();
      }
    };
    load().catch((error) => {
      if (!controller.signal.aborted) onNotice(error instanceof Error ? error.message : "原附件加载失败，请手动添加", "error");
    });
    return () => controller.abort();
  }, [markDirty, onNotice, seed?.forward_attachments]);

  const snapshot = useMemo<RecoveryDraft>(() => ({
    local_id: localIdRef.current,
    server_id: draftId,
    revision,
    updated_at: new Date().toISOString(),
    account_id: accountId,
    reply_to_message_id: seed?.reply_to_message_id,
    forward_message_id: seed?.forward_message_id,
    to: parseAddresses(to),
    cc: parseAddresses(cc),
    bcc: parseAddresses(bcc),
    subject: subject.trim(),
    body_html: bodyHtml || "<p></p>",
    body_text: bodyText,
    remote_attachments: remoteAttachments,
    pending_files: pendingFiles,
  }), [accountId, bcc, bodyHtml, bodyText, cc, draftId, pendingFiles, remoteAttachments, revision, seed?.forward_message_id, seed?.reply_to_message_id, subject, to]);
  latestSnapshotRef.current = snapshot;

  const writeLocal = useCallback((value: RecoveryDraft, updateStatus = true) => {
    const run = localQueueRef.current.catch(() => undefined).then(async () => {
      await saveDraftRecovery(value);
      localSavedRevisionRef.current = Math.max(localSavedRevisionRef.current, value.revision);
      await onRecoveryChange();
      if (updateStatus && revisionRef.current === value.revision) setSaveStatus(hasMeaningfulDraftContent(value) ? "local-saved" : "idle");
    });
    localQueueRef.current = run.catch(() => undefined);
    return run;
  }, [onRecoveryChange]);

  const performServerSave = useCallback(async (value: RecoveryDraft): Promise<Draft> => {
    if (!value.account_id) throw new Error("请选择发件账户");
    if (revisionRef.current === value.revision) setSaveStatus("server-saving");
    const pending = value.pending_files.filter((file) => !uploadedFilesRef.current.has(file));
    const input: DraftInput = {
      id: serverIdRef.current,
      account_id: value.account_id,
      reply_to_message_id: value.reply_to_message_id,
      forward_message_id: value.forward_message_id,
      to: value.to,
      cc: value.cc,
      bcc: value.bcc,
      subject: value.subject,
      body_html: value.body_html,
    };
    let saved = await api.saveDraft(input);
    serverIdRef.current = saved.id;
    setDraftId(saved.id);
    let working: RecoveryDraft = { ...value, server_id: saved.id, remote_attachments: remoteAttachmentsRef.current, pending_files: pending, updated_at: new Date().toISOString() };
    if (pending.length) await writeLocal(working, false);
    for (const file of pending) {
      const attachment = await api.uploadDraftAttachment(saved.id, file);
      uploadedFilesRef.current.add(file);
      remoteAttachmentsRef.current = [...remoteAttachmentsRef.current, attachment];
      setRemoteAttachments(remoteAttachmentsRef.current);
      setPendingFiles((items) => items.filter((item) => item !== file));
      working = { ...working, remote_attachments: remoteAttachmentsRef.current, pending_files: working.pending_files.filter((item) => item !== file), updated_at: new Date().toISOString() };
      await writeLocal(working, false);
      saved = { ...saved, attachments: remoteAttachmentsRef.current };
    }
    serverSavedRevisionRef.current = Math.max(serverSavedRevisionRef.current, value.revision);
    queryClient.invalidateQueries({ queryKey: ["drafts"] });
    if (revisionRef.current === value.revision) {
      await localQueueRef.current;
      await deleteDraftRecovery(value.local_id);
      await onRecoveryChange();
      setDirty(false);
      setSaveStatus("server-saved");
    }
    return saved;
  }, [onRecoveryChange, queryClient, writeLocal]);

  const queueServerSave = useCallback((value: RecoveryDraft) => {
    let result!: Promise<Draft>;
    const queued = serverQueueRef.current.catch(() => undefined).then(() => performServerSave(value));
    result = queued;
    serverQueueRef.current = queued.then(() => undefined, () => undefined);
    return result;
  }, [performServerSave]);

  useEffect(() => {
    if (!dirty) return;
    const timer = window.setTimeout(() => {
      void writeLocal(snapshot).catch(() => {
        if (revisionRef.current === snapshot.revision) setSaveStatus("error");
      });
    }, 250);
    return () => window.clearTimeout(timer);
  }, [dirty, snapshot, writeLocal]);

  useEffect(() => {
    if (!dirty || !accountId || !hasMeaningfulDraftContent(snapshot)) return;
    const timer = window.setTimeout(() => {
      void queueServerSave(snapshot).catch(() => {
        if (revisionRef.current === snapshot.revision) setSaveStatus(localSavedRevisionRef.current >= snapshot.revision ? "local-saved" : "error");
      });
    }, 1800);
    return () => window.clearTimeout(timer);
  }, [accountId, dirty, queueServerSave, snapshot]);

  useEffect(() => {
    const flush = () => { if (dirty && latestSnapshotRef.current) void writeLocal(latestSnapshotRef.current, false); };
    const visibility = () => { if (document.visibilityState === "hidden") flush(); };
    window.addEventListener("pagehide", flush);
    document.addEventListener("visibilitychange", visibility);
    return () => {
      window.removeEventListener("pagehide", flush);
      document.removeEventListener("visibilitychange", visibility);
    };
  }, [dirty, writeLocal]);

  useImperativeHandle(ref, () => ({
    focus: () => { if (editor && !editor.isDestroyed) editor.commands.focus(); else recipientInput.current?.focus(); },
    saveBeforeLeave: async () => {
      const value = latestSnapshotRef.current;
      if (!value || (!hasMeaningfulDraftContent(value) && !seed?.recovery_id)) {
        await deleteDraftRecovery(localIdRef.current);
        await onRecoveryChange();
        return "empty";
      }
      if (!dirty && serverSavedRevisionRef.current >= value.revision) return "server-saved";
      try {
        await writeLocal(value);
        return "local-saved";
      } catch {
        try { await queueServerSave(value); return "server-saved"; }
        catch (error) {
          setSaveStatus("error");
          onNotice(error instanceof Error ? error.message : "草稿保存失败，请重试", "error");
          return "blocked";
        }
      }
    },
  }), [dirty, editor, onNotice, onRecoveryChange, queueServerSave, seed?.recovery_id, writeLocal]);

  const save = useMutation({
    mutationFn: async () => {
      const value = latestSnapshotRef.current!;
      await writeLocal(value);
      return queueServerSave(value);
    },
    onSuccess: () => onNotice("草稿已保存", "neutral"),
    onError: (error) => {
      const value = latestSnapshotRef.current;
      setSaveStatus(value && localSavedRevisionRef.current >= value.revision ? "local-saved" : "error");
      onNotice(error instanceof Error ? error.message : "草稿保存失败", "error");
    },
  });
  const send = useMutation({
    mutationFn: async () => {
      const value = latestSnapshotRef.current!;
      await writeLocal(value);
      const saved = await queueServerSave(value);
      return api.sendDraft(saved.id);
    },
    onSuccess: async (value) => {
      await deleteDraftRecovery(localIdRef.current);
      await onRecoveryChange();
      queryClient.invalidateQueries({ queryKey: ["drafts"] });
      queryClient.invalidateQueries({ queryKey: ["conversations"] });
      onComplete();
      onNotice(value.status === "unknown" ? "连接中断，邮件状态待确认，请勿重复发送" : value.status === "queued" ? "邮件已加入发件队列" : "邮件已发送", value.status === "unknown" ? "error" : "success");
    },
    onError: (error) => {
      const value = latestSnapshotRef.current;
      setSaveStatus(value && localSavedRevisionRef.current >= value.revision ? "local-saved" : "error");
      onNotice(error instanceof Error ? error.message : "发送失败，草稿已保留", "error");
    },
  });

  const addFiles = (event: ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(event.target.files ?? []);
    const total = [...pendingFiles, ...files].reduce((size, file) => size + file.size, 0) + remoteAttachments.reduce((size, file) => size + file.size, 0);
    if (total > 25 * 1024 * 1024) { onNotice("附件总大小不能超过 25 MB", "error"); return; }
    setPendingFiles((items) => [...items, ...files]);
    markDirty();
    event.target.value = "";
  };
  const removeRemoteAttachment = async (attachment: Attachment) => {
    if (!serverIdRef.current) return;
    await api.removeDraftAttachment(serverIdRef.current, attachment.id);
    remoteAttachmentsRef.current = remoteAttachmentsRef.current.filter((item) => item.id !== attachment.id);
    setRemoteAttachments(remoteAttachmentsRef.current);
    markDirty();
  };
  const discard = async () => {
    try {
      await serverQueueRef.current;
      await localQueueRef.current;
      if (serverIdRef.current) await api.deleteDraft(serverIdRef.current);
      await deleteDraftRecovery(localIdRef.current);
      await onRecoveryChange();
      queryClient.invalidateQueries({ queryKey: ["drafts"] });
      onComplete();
    } catch (error) { onNotice(error instanceof Error ? error.message : "无法删除草稿", "error"); }
  };

  const recipientValid = parseAddresses(to).every((address) => /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(address.email));
  const canSend = !!accountId && parseAddresses(to).length > 0 && recipientValid && !send.isPending;
  const statusText = saveStatus === "local-saving" ? "本地保存中…" : saveStatus === "local-saved" ? "本地已保存，等待同步" : saveStatus === "server-saving" ? "正在保存到服务器…" : saveStatus === "server-saved" ? "已保存到服务器" : saveStatus === "error" ? "保存失败" : "尚未开始编辑";

  return (
    <section className="composer" aria-label={seed?.id || seed?.recovery_id ? "编辑草稿" : seed?.forward_message_id ? "转发邮件" : seed?.reply_to_message_id ? "回复邮件" : "新邮件"} aria-busy={save.isPending || send.isPending}>
      <header className="composer__header"><div><h2>{seed?.id || seed?.recovery_id ? "编辑草稿" : seed?.forward_message_id ? "转发邮件" : seed?.reply_to_message_id ? "回复邮件" : "新邮件"}</h2><p>{statusText}</p></div><IconButton label="关闭写信" onClick={onRequestClose}><X size={19} /></IconButton></header>
      <div className="composer__content">
        <div className="composer__fields">
          <div className="composer-field"><label htmlFor="compose-from">发件人</label><div className="composer-account"><Select id="compose-from" ariaLabel="发件人" size="compact" value={accountId} options={accountOptions} onValueChange={(value) => { setAccountId(value); markDirty(); }} /></div></div>
          <div className="composer-field"><label htmlFor="compose-to">收件人</label><input ref={recipientInput} id="compose-to" value={to} onChange={(event) => { setTo(event.target.value); markDirty(); }} placeholder="姓名 <email@example.com>" autoComplete="off" /><button onClick={() => setShowCarbonCopy((value) => !value)}>抄送 / 密送</button></div>
          <AnimatePresence initial={false}>{showCarbonCopy && <motion.div className="composer-carbon" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }}><div className="composer-field"><label htmlFor="compose-cc">抄送</label><input id="compose-cc" value={cc} onChange={(event) => { setCc(event.target.value); markDirty(); }} /></div><div className="composer-field"><label htmlFor="compose-bcc">密送</label><input id="compose-bcc" value={bcc} onChange={(event) => { setBcc(event.target.value); markDirty(); }} /></div></motion.div>}</AnimatePresence>
          <div className="composer-field"><label htmlFor="compose-subject">主题</label><input id="compose-subject" className="composer-subject" value={subject} onChange={(event) => { setSubject(event.target.value); markDirty(); }} placeholder="简洁的邮件主题" /></div>
        </div>
        <ComposerToolbar editor={editor} />
        <div className="composer__editor"><EditorContent editor={editor} /></div>
        {(remoteAttachments.length > 0 || pendingFiles.length > 0) && <div className="composer-attachments">{remoteAttachments.map((attachment) => <span key={attachment.id}><Paperclip size={14} /><span>{attachment.filename}</span><small>{formatBytes(attachment.size)}</small><button aria-label={`移除 ${attachment.filename}`} onClick={() => removeRemoteAttachment(attachment)}><X size={13} /></button></span>)}{pendingFiles.map((file, index) => <span key={`${file.name}-${file.lastModified}-${index}`}><Paperclip size={14} /><span>{file.name}</span><small>{formatBytes(file.size)}</small><button aria-label={`移除 ${file.name}`} onClick={() => { setPendingFiles((items) => items.filter((item) => item !== file)); markDirty(); }}><X size={13} /></button></span>)}</div>}
      </div>
      <footer className="composer__footer"><div><Button onClick={() => send.mutate()} disabled={!canSend}><Send size={17} />{send.isPending ? "正在发送…" : "发送"}</Button><IconButton label="添加附件" onClick={() => fileInput.current?.click()}><Paperclip size={18} /></IconButton><input ref={fileInput} type="file" multiple hidden onChange={addFiles} /></div><div><Button variant="ghost" size="sm" onClick={() => save.mutate()} disabled={save.isPending || !accountId}>保存草稿</Button>{(draftId || seed?.recovery_id || hasMeaningfulDraftContent(snapshot)) && <IconButton label="删除草稿" onClick={discard}><Trash2 size={17} /></IconButton>}</div></footer>
    </section>
  );
});

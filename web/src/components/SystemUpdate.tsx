import * as Dialog from "@radix-ui/react-dialog";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ExternalLink, RefreshCw, Server, TriangleAlert } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { api, demoMode } from "../api/client";
import type { SystemUpdateState, SystemUpdateStatus } from "../types";
import { Button, ErrorState, Spinner } from "./ui";

export const systemUpdateQueryKey = ["system-update"] as const;
export const activeUpdateStates: SystemUpdateState[] = ["queued", "downloading", "installing", "restarting"];
export const updateRecoveryTimeout = 120_000;

export function isSystemUpdateActive(status?: SystemUpdateStatus): boolean {
  return !!status && activeUpdateStates.includes(status.state);
}

export function systemUpdatePollInterval(status?: SystemUpdateStatus): number {
  if (isSystemUpdateActive(status)) return 1_000;
  if (!status?.checked_at && !status?.latest) return 5_000;
  return 30 * 60_000;
}

export function shouldTimeUpdateRecovery(status: SystemUpdateStatus | undefined, queryFailed: boolean): boolean {
  return queryFailed || status?.state === "restarting";
}

const stateLabels: Record<SystemUpdateState, string> = {
  idle: "等待操作",
  queued: "等待更新器",
  downloading: "正在下载",
  installing: "正在安装",
  restarting: "正在重启",
  succeeded: "更新完成",
  failed: "更新失败",
};

function displayTime(value?: string): string {
  if (!value) return "未提供";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString("zh-CN", { hour12: false });
}

function displayVersion(value?: string): string {
  if (!value) return "dev";
  return value.startsWith("v") || value === "dev" ? value : `v${value}`;
}

export function SystemUpdatePanel() {
  const queryClient = useQueryClient();
  const updateQuery = useQuery({
    queryKey: systemUpdateQueryKey,
    queryFn: api.getSystemUpdate,
    refetchOnWindowFocus: true,
    refetchInterval: (query) => systemUpdatePollInterval(query.state.data),
  });
  const check = useMutation({
    mutationFn: api.checkSystemUpdate,
    onSuccess: (status) => {
      install.reset();
      queryClient.setQueryData(systemUpdateQueryKey, status);
    },
  });
  const install = useMutation({
    mutationFn: (version: string) => api.installSystemUpdate(version),
    onMutate: async (version) => {
      check.reset();
      await queryClient.cancelQueries({ queryKey: systemUpdateQueryKey });
      const previous = queryClient.getQueryData<SystemUpdateStatus>(systemUpdateQueryKey);
      if (previous) queryClient.setQueryData<SystemUpdateStatus>(systemUpdateQueryKey, { ...previous, state: "queued", target_version: version });
      return { previous };
    },
    onError: (_error, _version, context) => {
      if (context?.previous) queryClient.setQueryData(systemUpdateQueryKey, context.previous);
      void queryClient.invalidateQueries({ queryKey: systemUpdateQueryKey });
    },
    onSuccess: (status, version) => {
      check.reset();
      queryClient.setQueryData(systemUpdateQueryKey, status);
      void queryClient.invalidateQueries({ queryKey: systemUpdateQueryKey });
    },
  });
  const status = updateQuery.data;
  const active = isSystemUpdateActive(status);
  const error = check.error ?? install.error ?? updateQuery.error;

  if (updateQuery.isPending) return <div className="system-update-loading"><Spinner label="正在读取更新信息" /></div>;
  if (!status) return <ErrorState title="无法读取更新信息" message={error instanceof Error ? error.message : undefined} onRetry={() => { void updateQuery.refetch(); }} />;

  const checked = !!status.checked_at || !!status.latest;
  const checkDisabled = active || check.isPending || install.isPending;
  const installDisabled = active || check.isPending || install.isPending || !status.enabled || !status.install_supported || !status.available || !status.latest;
  const statusTone = status.state === "failed" ? "error" : status.state === "succeeded" || (checked && !status.available && status.state === "idle") ? "ok" : status.available || active ? "update" : "neutral";

  return (
    <div className="system-update-pane">
      <div className="pane-heading">
        <div><h2>系统更新</h2><p>从官方 GitHub Release 检查并安装 MailManager 稳定版本。</p></div>
        <Button variant="secondary" size="sm" onClick={() => { install.reset(); check.mutate(); }} disabled={checkDisabled}>
          <RefreshCw className={check.isPending ? "spin" : ""} size={15} />{check.isPending ? "检查中…" : "检查更新"}
        </Button>
      </div>

      <div className={`update-summary update-summary--${statusTone}`}>
        <div className="update-summary__icon">{statusTone === "error" ? <TriangleAlert size={22} /> : statusTone === "ok" ? <Check size={22} /> : <Server size={22} />}</div>
        <div className="update-summary__copy">
          <span>{stateLabels[status.state]}</span>
          <strong>{active ? `${stateLabels[status.state]}${status.target_version ? ` ${displayVersion(status.target_version)}` : ""}` : status.available && status.latest ? `发现新版本 ${displayVersion(status.latest.version)}` : status.state === "failed" ? "本次更新未完成" : !checked ? "尚未检查更新" : "当前已是最新版本"}</strong>
          <small>{status.message || (status.rollback_performed ? "旧版本已自动恢复，服务可以继续使用。" : status.checked_at ? `上次检查：${displayTime(status.checked_at)}` : "尚未检查更新")}</small>
        </div>
        {active && <span className="update-summary__activity" aria-label={stateLabels[status.state]} />}
      </div>

      <dl className="update-version-grid">
        <div><dt>当前版本</dt><dd>{displayVersion(status.current_version)}</dd></div>
        <div><dt>最新版本</dt><dd>{status.latest ? displayVersion(status.latest.version) : checked ? "未发现" : "尚未检查"}</dd></div>
        <div><dt>提交</dt><dd className="update-mono">{status.current_commit || "未提供"}</dd></div>
        <div><dt>构建时间</dt><dd>{displayTime(status.current_build_time)}</dd></div>
      </dl>

      {!status.enabled && <div className="update-notice"><TriangleAlert size={16} /><span>服务器未启用网页自动更新。请由管理员设置 <code>MAILMANAGER_AUTO_UPDATE_ENABLED=true</code>。</span></div>}
      {status.enabled && !status.install_supported && <div className="update-notice"><TriangleAlert size={16} /><span>当前系统或部署方式不支持网页安装，请使用服务器安装脚本更新。</span></div>}
      {error && <p className="form-error" role="alert">{error instanceof Error ? error.message : "更新操作失败"}</p>}

      {status.latest && <section className="update-release" aria-label="版本说明">
        <div className="update-release__heading"><div><strong>{status.latest.name || status.latest.tag_name}</strong><span>{displayTime(status.latest.published_at)}</span></div>{status.latest.html_url && <a href={status.latest.html_url} target="_blank" rel="noreferrer">GitHub <ExternalLink size={13} /></a>}</div>
        <p>{status.latest.release_notes || "此版本没有提供更新说明。"}</p>
      </section>}

      <div className="update-actions">
        <Button size="lg" disabled={installDisabled} onClick={() => { check.reset(); if (status.latest) install.mutate(status.latest.version); }}>
          {active ? `${stateLabels[status.state]}…` : install.isPending ? "正在提交…" : status.available && status.latest ? `更新并重启到 ${displayVersion(status.latest.version)}` : "无需更新"}
        </Button>
        <span>安装期间页面会自动等待服务恢复，不需要手动刷新。</span>
      </div>
    </div>
  );
}

export function UpdateRestartOverlay({ status, queryFailed, refetch }: { status?: SystemUpdateStatus; queryFailed: boolean; refetch: () => Promise<unknown> }) {
  const [visible, setVisible] = useState(false);
  const [backgrounded, setBackgrounded] = useState(false);
  const [timedOut, setTimedOut] = useState(false);
  const [probing, setProbing] = useState(false);
  const [startedAt, setStartedAt] = useState(0);
  const targetRef = useRef<string | undefined>(undefined);
  const probingRef = useRef(false);
  const active = isSystemUpdateActive(status);

  useEffect(() => {
    if (!active) {
      setBackgrounded(false);
      if (status?.state === "idle") {
        setVisible(false);
        setStartedAt(0);
        setTimedOut(false);
      }
      return;
    }
    targetRef.current = status?.target_version || status?.latest?.version || targetRef.current;
    if (!backgrounded) setVisible(true);
    setTimedOut(false);
  }, [active, backgrounded, status?.latest?.version, status?.state, status?.target_version]);

  useEffect(() => {
    if ((status?.state === "restarting" || queryFailed) && (active || !!targetRef.current)) {
      setBackgrounded(false);
      setVisible(true);
    }
  }, [active, queryFailed, status?.state]);

  const recovering = shouldTimeUpdateRecovery(status, queryFailed);
  useEffect(() => {
    if (!visible || !recovering) {
      setStartedAt(0);
      setTimedOut(false);
      return;
    }
    setStartedAt((current) => current || Date.now());
  }, [recovering, visible]);

  useEffect(() => {
    if (!visible || !startedAt) return;
    const remaining = Math.max(0, updateRecoveryTimeout - (Date.now() - startedAt));
    const timer = window.setTimeout(() => setTimedOut(true), remaining);
    return () => window.clearTimeout(timer);
  }, [startedAt, visible]);

  useEffect(() => {
    if (!visible || !status) return;
    const target = targetRef.current;
    const recovered = status.state === "succeeded" || (!active && !!target && status.current_version === target);
    if (!recovered) return;
    if (demoMode) {
      const timer = window.setTimeout(() => { setVisible(false); setStartedAt(0); }, 1_000);
      return () => window.clearTimeout(timer);
    }
    window.location.reload();
  }, [active, status, visible]);

  useEffect(() => {
    if (!visible || status?.state !== "failed") return;
    const timer = window.setTimeout(() => { setVisible(false); setStartedAt(0); }, 1_500);
    return () => window.clearTimeout(timer);
  }, [status?.state, visible]);

  const probe = useCallback(async () => {
    if (probingRef.current) return;
    probingRef.current = true;
    setProbing(true);
    try {
      if (await api.checkReadiness()) await refetch();
    } finally {
      probingRef.current = false;
      setProbing(false);
    }
  }, [refetch]);

  useEffect(() => {
    if (!visible || (!queryFailed && status?.state !== "restarting")) return;
    void probe();
    const timer = window.setInterval(() => { void probe(); }, 2_000);
    return () => window.clearInterval(timer);
  }, [probe, queryFailed, status?.state, visible]);

  if (!visible) return null;
  const state = queryFailed ? "restarting" : status?.state ?? "restarting";
  const stateIndex = Math.max(0, activeUpdateStates.indexOf(state as SystemUpdateState));
  const canBackground = !queryFailed && (state === "queued" || state === "downloading" || state === "installing");
  return (
    <Dialog.Root open modal>
      <Dialog.Portal>
        <Dialog.Overlay className="update-restart-overlay" />
        <Dialog.Content className="update-restart-panel" aria-modal="true" onEscapeKeyDown={(event) => event.preventDefault()} onPointerDownOutside={(event) => event.preventDefault()} onInteractOutside={(event) => event.preventDefault()}>
          <div className="update-restart-live" role="status" aria-live="polite">
        <div className="update-restart-panel__mark"><RefreshCw className={timedOut || state === "failed" ? "" : "spin"} size={25} /></div>
        <div><span className="eyebrow">系统更新</span><Dialog.Title>{timedOut ? "服务恢复时间超出预期" : state === "restarting" ? "正在重启 MailManager" : stateLabels[state as SystemUpdateState]}</Dialog.Title><Dialog.Description>{timedOut ? "更新记录仍然保留，可以重试连接或在服务器查看服务日志。" : state === "failed" ? status?.message || "更新未能完成，请在系统更新页查看详细状态。" : "请保持此页面打开，服务恢复后将自动刷新。"}</Dialog.Description></div>
        {!timedOut && state !== "failed" && <ol className="update-stage-list" aria-label="更新进度">{activeUpdateStates.map((item, index) => <li key={item} className={index < stateIndex ? "is-done" : index === stateIndex ? "is-active" : ""}><span />{stateLabels[item]}</li>)}</ol>}
        {canBackground && <div className="update-restart-actions"><Button variant="secondary" onClick={() => { setBackgrounded(true); setVisible(false); }}>在后台进行</Button></div>}
        {timedOut && <div className="update-restart-diagnostic"><code>journalctl -u mailmanager.service -n 100 --no-pager</code><Button variant="secondary" onClick={() => { setTimedOut(false); setStartedAt(Date.now()); void probe(); }} disabled={probing}>{probing ? "正在连接…" : "重试连接"}</Button></div>}
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

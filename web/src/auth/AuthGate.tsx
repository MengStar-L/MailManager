import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AnimatePresence, motion } from "motion/react";
import QRCode from "qrcode";
import { Check, Copy, KeyRound, LockKeyhole, ShieldCheck } from "lucide-react";
import { type FormEvent, type ReactNode, useEffect, useState } from "react";
import { api, ApiError } from "../api/client";
import type { SetupEnrollment } from "../types";
import { Button, ErrorState, Field, FullPageLoader } from "../components/ui";

function friendlyError(error: unknown): string {
  return error instanceof ApiError ? error.message : error instanceof Error ? error.message : "请求失败，请稍后重试。";
}

function AuthFrame({ eyebrow, title, copy, children }: { eyebrow: string; title: string; copy: string; children: ReactNode }) {
  return (
    <main className="auth-page">
      <div className="auth-page__masthead"><div className="brand-mark">M</div><span>MailManager</span></div>
      <motion.section className="auth-card" initial={{ opacity: 0, y: 14 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.24 }}>
        <div className="auth-card__intro"><span className="eyebrow">{eyebrow}</span><h1>{title}</h1><p>{copy}</p></div>
        {children}
      </motion.section>
      <p className="auth-page__footnote"><LockKeyhole size={13} /> 凭据仅加密保存在你的服务器上</p>
    </main>
  );
}

function SetupScreen({ onComplete }: { onComplete: () => void }) {
  const [step, setStep] = useState<"credentials" | "totp" | "recovery">("credentials");
  const [token, setToken] = useState("");
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [enrollment, setEnrollment] = useState<SetupEnrollment>();
  const [totp, setTotp] = useState("");
  const [qrCode, setQrCode] = useState("");
  const [recoveryCodes, setRecoveryCodes] = useState<string[]>([]);
  const [copied, setCopied] = useState(false);

  const enroll = useMutation({ mutationFn: api.beginSetup, onSuccess: (value) => { setEnrollment(value); setStep("totp"); } });
  const complete = useMutation({ mutationFn: api.completeSetup, onSuccess: (value) => { setRecoveryCodes(value.recovery_codes); setStep("recovery"); } });

  useEffect(() => {
    if (!enrollment) return;
    QRCode.toDataURL(enrollment.provisioning_uri, { width: 220, margin: 1, color: { dark: "#18232e", light: "#ffffff" } }).then(setQrCode);
  }, [enrollment]);

  const submitCredentials = (event: FormEvent) => {
    event.preventDefault();
    if (password.length < 12 || password !== confirmPassword) return;
    enroll.mutate({ token: token.trim(), username: username.trim() || "admin" });
  };

  const submitTotp = (event: FormEvent) => {
    event.preventDefault();
    complete.mutate({ token: token.trim(), username: username.trim() || "admin", password, totp_code: totp.replace(/\s/g, "") });
  };

  return (
    <AuthFrame eyebrow={`首次设置 · ${step === "credentials" ? "1 / 3" : step === "totp" ? "2 / 3" : "3 / 3"}`} title={step === "credentials" ? "建立你的私人邮箱入口" : step === "totp" ? "启用双重验证" : "保存恢复代码"} copy={step === "credentials" ? "使用服务器启动日志中的一次性令牌完成初始化。" : step === "totp" ? "用身份验证器扫描二维码，然后输入当前的 6 位验证码。" : "这些代码只显示一次，请放在安全且独立的位置。"}>
      <AnimatePresence mode="wait">
        {step === "credentials" && (
          <motion.form key="credentials" className="auth-form" onSubmit={submitCredentials} initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }}>
            <Field label="一次性设置令牌"><input autoFocus value={token} onChange={(e) => setToken(e.target.value)} autoComplete="off" placeholder="粘贴 30 分钟内有效的令牌" required /></Field>
            <Field label="管理员名称"><input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" required /></Field>
            <Field label="主密码" hint="至少 12 个字符"><input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" minLength={12} required /></Field>
            <Field label="再次输入密码"><input type="password" value={confirmPassword} onChange={(e) => setConfirmPassword(e.target.value)} autoComplete="new-password" required aria-invalid={!!confirmPassword && password !== confirmPassword} /></Field>
            {confirmPassword && password !== confirmPassword && <p className="form-error">两次输入的密码不一致。</p>}
            {enroll.error && <p className="form-error">{friendlyError(enroll.error)}</p>}
            <Button size="lg" disabled={enroll.isPending || password.length < 12 || password !== confirmPassword}>{enroll.isPending ? "正在验证…" : "继续"}</Button>
          </motion.form>
        )}
        {step === "totp" && enrollment && (
          <motion.form key="totp" className="auth-form" onSubmit={submitTotp} initial={{ opacity: 0, x: 12 }} animate={{ opacity: 1, x: 0 }} exit={{ opacity: 0 }}>
            <div className="qr-panel">{qrCode ? <img src={qrCode} alt="TOTP 配置二维码" /> : <div className="qr-placeholder" />}<code>{enrollment.secret}</code></div>
            <Field label="6 位验证码"><input className="totp-input" inputMode="numeric" pattern="[0-9]{6}" maxLength={6} value={totp} onChange={(e) => setTotp(e.target.value.replace(/\D/g, ""))} autoComplete="one-time-code" placeholder="000000" autoFocus required /></Field>
            {complete.error && <p className="form-error">{friendlyError(complete.error)}</p>}
            <Button size="lg" disabled={complete.isPending || totp.length !== 6}>{complete.isPending ? "正在启用…" : "启用并继续"}</Button>
          </motion.form>
        )}
        {step === "recovery" && (
          <motion.div key="recovery" className="auth-form" initial={{ opacity: 0, x: 12 }} animate={{ opacity: 1, x: 0 }}>
            <div className="recovery-codes">{recoveryCodes.map((code) => <code key={code}>{code}</code>)}</div>
            <Button variant="secondary" type="button" onClick={async () => { await navigator.clipboard.writeText(recoveryCodes.join("\n")); setCopied(true); }}><Copy size={16} />{copied ? "已复制" : "复制全部代码"}</Button>
            <Button size="lg" onClick={onComplete}><ShieldCheck size={18} />进入 MailManager</Button>
          </motion.div>
        )}
      </AnimatePresence>
    </AuthFrame>
  );
}

function LoginScreen({ onComplete }: { onComplete: () => void }) {
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [challenge, setChallenge] = useState("");
  const [code, setCode] = useState("");
  const login = useMutation({ mutationFn: api.login, onSuccess: (value) => setChallenge(value.challenge_token) });
  const verify = useMutation({ mutationFn: api.verifyTotp, onSuccess: onComplete });

  return (
    <AuthFrame eyebrow="私人工作台" title={challenge ? "验证你的身份" : "欢迎回来"} copy={challenge ? "输入身份验证器中当前显示的 6 位验证码。" : "登录后，你的全部邮箱会回到一个安静的工作空间。"}>
      {!challenge ? (
        <form className="auth-form" onSubmit={(event) => { event.preventDefault(); login.mutate({ username, password }); }}>
          <Field label="管理员名称"><input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" autoFocus required /></Field>
          <Field label="密码"><input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" required /></Field>
          {login.error && <p className="form-error">{friendlyError(login.error)}</p>}
          <Button size="lg" disabled={login.isPending}><KeyRound size={18} />{login.isPending ? "正在验证…" : "继续"}</Button>
        </form>
      ) : (
        <form className="auth-form" onSubmit={(event) => { event.preventDefault(); verify.mutate({ challenge_token: challenge, code }); }}>
          <Field label="6 位验证码"><input className="totp-input" inputMode="numeric" pattern="[0-9]{6}" maxLength={6} value={code} onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))} autoComplete="one-time-code" placeholder="000000" autoFocus required /></Field>
          {verify.error && <p className="form-error">{friendlyError(verify.error)}</p>}
          <Button size="lg" disabled={verify.isPending || code.length !== 6}><Check size={18} />{verify.isPending ? "正在登录…" : "打开邮箱"}</Button>
          <Button type="button" variant="ghost" onClick={() => { setChallenge(""); setCode(""); }}>返回密码登录</Button>
        </form>
      )}
    </AuthFrame>
  );
}

export function AuthGate({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  const setup = useQuery({ queryKey: ["setup"], queryFn: api.getSetupStatus, retry: 1 });
  const session = useQuery({ queryKey: ["session"], queryFn: api.getSession, enabled: setup.data?.setup_required === false, retry: false });
  const refresh = async () => {
    await queryClient.invalidateQueries({ queryKey: ["setup"] });
    await queryClient.invalidateQueries({ queryKey: ["session"] });
  };

  if (setup.isPending || (setup.data?.setup_required === false && session.isPending)) return <FullPageLoader />;
  if (setup.isError) return <main className="center-page"><ErrorState title="无法连接 MailManager" message={friendlyError(setup.error)} onRetry={() => setup.refetch()} /></main>;
  if (setup.data?.setup_required) return <SetupScreen onComplete={refresh} />;
  if (!session.data?.authenticated) return <LoginScreen onComplete={refresh} />;
  return children;
}

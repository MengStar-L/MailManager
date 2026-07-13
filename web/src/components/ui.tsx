import * as AvatarPrimitive from "@radix-ui/react-avatar";
import * as CheckboxPrimitive from "@radix-ui/react-checkbox";
import * as TooltipPrimitive from "@radix-ui/react-tooltip";
import { Check, Inbox, LoaderCircle, TriangleAlert } from "lucide-react";
import type { ButtonHTMLAttributes, CSSProperties, ReactNode } from "react";
import { initials } from "../lib/format";

export function Button({ className = "", variant = "primary", size = "md", ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: "primary" | "secondary" | "ghost" | "danger"; size?: "sm" | "md" | "lg" }) {
  return <button className={`button button--${variant} button--${size} ${className}`} {...props} />;
}

export function IconButton({ label, className = "", active = false, children, ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { label: string; active?: boolean; children: ReactNode }) {
  return (
    <TooltipPrimitive.Root>
      <TooltipPrimitive.Trigger asChild>
        <button aria-label={label} className={`icon-button ${active ? "is-active" : ""} ${className}`} {...props}>{children}</button>
      </TooltipPrimitive.Trigger>
      <TooltipPrimitive.Portal>
        <TooltipPrimitive.Content className="tooltip" sideOffset={7}>{label}<TooltipPrimitive.Arrow className="tooltip__arrow" /></TooltipPrimitive.Content>
      </TooltipPrimitive.Portal>
    </TooltipPrimitive.Root>
  );
}

export function Checkbox({ checked, onCheckedChange, label, className = "" }: { checked: boolean | "indeterminate"; onCheckedChange: (value: boolean) => void; label: string; className?: string }) {
  return (
    <CheckboxPrimitive.Root className={`checkbox ${className}`} checked={checked} onCheckedChange={(value) => onCheckedChange(value === true)} aria-label={label}>
      <CheckboxPrimitive.Indicator className="checkbox__indicator"><Check size={13} strokeWidth={3} /></CheckboxPrimitive.Indicator>
    </CheckboxPrimitive.Root>
  );
}

export function Avatar({ name, color, size = 36 }: { name: string; color?: string; size?: number }) {
  return (
    <AvatarPrimitive.Root className="avatar" style={{ "--avatar-color": color ?? "#55706c", width: size, height: size } as CSSProperties}>
      <AvatarPrimitive.Fallback delayMs={0}>{initials(name)}</AvatarPrimitive.Fallback>
    </AvatarPrimitive.Root>
  );
}

export function Spinner({ label = "正在加载" }: { label?: string }) {
  return <div className="spinner" role="status"><LoaderCircle size={18} /><span>{label}</span></div>;
}

export function FullPageLoader() {
  return <main className="center-page"><div className="brand-mark brand-mark--large">M</div><Spinner label="正在打开邮箱" /></main>;
}

export function ErrorState({ title = "暂时无法加载", message, onRetry }: { title?: string; message?: string; onRetry?: () => void }) {
  return (
    <div className="state-panel state-panel--error" role="alert">
      <div className="state-panel__icon"><TriangleAlert size={20} /></div>
      <div><strong>{title}</strong><p>{message ?? "请稍后重试。"}</p></div>
      {onRetry && <Button variant="secondary" size="sm" onClick={onRetry}>重试</Button>}
    </div>
  );
}

export function EmptyState({ title, message, action }: { title: string; message: string; action?: ReactNode }) {
  return (
    <div className="empty-state">
      <div className="empty-state__icon"><Inbox size={24} /></div>
      <h3>{title}</h3><p>{message}</p>{action}
    </div>
  );
}

export function Field({ label, hint, children, className = "" }: { label: string; hint?: string; children: ReactNode; className?: string }) {
  return <label className={`field ${className}`}><span className="field__label">{label}</span>{children}{hint && <span className="field__hint">{hint}</span>}</label>;
}

export function AccountDot({ color, status }: { color: string; status?: string }) {
  return <span className={`account-dot ${status === "error" || status === "reauth_required" ? "has-error" : status === "syncing" ? "is-syncing" : ""}`} style={{ "--dot-color": color } as CSSProperties} />;
}

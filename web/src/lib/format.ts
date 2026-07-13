import type { MailAddress } from "../types";

export function formatMailboxDate(value: string): string {
  const date = new Date(value);
  const now = new Date();
  if (date.toDateString() === now.toDateString()) {
    return new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false }).format(date);
  }
  if (date.getFullYear() === now.getFullYear()) {
    return new Intl.DateTimeFormat("zh-CN", { month: "short", day: "numeric" }).format(date);
  }
  return new Intl.DateTimeFormat("zh-CN", { year: "numeric", month: "short", day: "numeric" }).format(date);
}

export function formatFullDate(value: string): string {
  return new Intl.DateTimeFormat("zh-CN", { year: "numeric", month: "long", day: "numeric", weekday: "short", hour: "2-digit", minute: "2-digit", hour12: false }).format(new Date(value));
}

export function formatBytes(size: number): string {
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${Math.round(size / 1024)} KB`;
  return `${(size / 1024 / 1024).toFixed(1)} MB`;
}

export function displayName(address?: MailAddress): string {
  if (!address) return "未知发件人";
  return address.name?.trim() || address.email.split("@")[0];
}

export function initials(value: string): string {
  const compact = value.trim();
  if (!compact) return "?";
  const words = compact.split(/\s+/);
  return words.length > 1 ? `${words[0][0]}${words[1][0]}`.toUpperCase() : [...compact].slice(0, 2).join("").toUpperCase();
}

export function relativeSync(value?: string, fallback = "尚未同步"): string {
  if (!value) return fallback;
  const minutes = Math.max(0, Math.round((Date.now() - new Date(value).getTime()) / 60_000));
  if (minutes < 1) return "刚刚同步";
  if (minutes < 60) return `${minutes} 分钟前`;
  if (minutes < 1440) return `${Math.round(minutes / 60)} 小时前`;
  return `${Math.round(minutes / 1440)} 天前`;
}

export function parseAddresses(value: string): MailAddress[] {
  return value.split(/[,;]/).map((part) => part.trim()).filter(Boolean).map((part) => {
    const match = part.match(/^(.*?)\s*<([^>]+)>$/);
    return match ? { name: match[1].trim(), email: match[2].trim() } : { email: part };
  });
}

export function addressesToString(addresses: MailAddress[]): string {
  return addresses.map((address) => address.name ? `${address.name} <${address.email}>` : address.email).join(", ");
}

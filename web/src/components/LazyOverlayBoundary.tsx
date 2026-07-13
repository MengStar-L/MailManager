import { RefreshCw, X } from "lucide-react";
import { Component, type ErrorInfo, type ReactNode } from "react";
import { Button } from "./ui";

export class LazyOverlayBoundary extends Component<{
  children: ReactNode;
  label: string;
  resetKey: string;
  onDismiss: () => void;
}, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("Lazy overlay failed", error, info);
  }

  componentDidUpdate(previous: Readonly<{ resetKey: string }>) {
    if (this.state.failed && previous.resetKey !== this.props.resetKey) this.setState({ failed: false });
  }

  render() {
    if (!this.state.failed) return this.props.children;
    return (
      <div className="toast toast--error lazy-overlay-error" role="alert">
        <span>{this.props.label}加载失败，工作台仍可使用。</span>
        <Button variant="ghost" size="sm" onClick={() => window.location.reload()}><RefreshCw size={15} />重新加载</Button>
        <button aria-label={`关闭${this.props.label}错误提示`} onClick={this.props.onDismiss}><X size={15} /></button>
      </div>
    );
  }
}

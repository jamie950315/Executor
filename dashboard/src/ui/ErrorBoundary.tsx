import { Component, type ErrorInfo, type ReactNode } from "react";

export class ErrorBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  override state = { failed: false };
  static getDerivedStateFromError(): { failed: boolean } { return { failed: true }; }
  override componentDidCatch(_error: Error, _info: ErrorInfo): void { /* Payload-free boundary by design. */ }
  override render(): ReactNode {
    if (this.state.failed) return <main className="fatal-state"><p className="eyebrow alarm-text">Interface isolated</p><h1>Dashboard view failed safely</h1><p>No command, file, terminal, screenshot, or credential payload was included in this error.</p><button onClick={() => location.reload()}>Reload Dashboard</button></main>;
    return this.props.children;
  }
}

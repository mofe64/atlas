import { Component, type ErrorInfo, type ReactNode } from "react";

type AppErrorBoundaryProps = {
  children: ReactNode;
};

type AppErrorBoundaryState = {
  failed: boolean;
};

export class AppErrorBoundary extends Component<AppErrorBoundaryProps, AppErrorBoundaryState> {
  state: AppErrorBoundaryState = { failed: false };

  static getDerivedStateFromError(): AppErrorBoundaryState {
    return { failed: true };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("Atlas operator interface failed to render", error, info.componentStack);
  }

  render() {
    if (this.state.failed) {
      return (
        <main className="app-failure" role="alert">
          <p className="eyebrow">Interface recovery</p>
          <h1>Atlas could not display this view</h1>
          <p>
            The local services are still running, but part of the operator interface encountered
            unexpected data. Reload Atlas before continuing operations.
          </p>
          <button type="button" onClick={() => window.location.reload()}>
            Reload Atlas
          </button>
        </main>
      );
    }

    return this.props.children;
  }
}

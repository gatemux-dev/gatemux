import { Component, type ErrorInfo, type ReactNode } from 'react'

interface Props {
  children: ReactNode
}

interface State {
  error: Error | null
}

// Last line of defense: a render-time throw anywhere below used to blank
// the entire console to a white screen. Keep this a class component —
// error boundaries have no hook equivalent.
export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('Unhandled render error:', error, info.componentStack)
  }

  render() {
    if (!this.state.error) return this.props.children
    return (
      <div role="alert" style={{ maxWidth: 480, margin: '15vh auto', padding: '0 24px', textAlign: 'center' }}>
        <h1 style={{ fontSize: 18, marginBottom: 8 }}>Something went wrong</h1>
        <p className="muted" style={{ fontSize: 13, marginBottom: 16 }}>
          The console hit an unexpected error. Reloading usually fixes it; if it keeps
          happening, check the browser console for details.
        </p>
        <button className="btn btn-primary" onClick={() => window.location.reload()}>
          Reload
        </button>
        <button className="btn btn-ghost" style={{ marginLeft: 8 }} onClick={() => { window.location.href = '/' }}>
          Go home
        </button>
      </div>
    )
  }
}

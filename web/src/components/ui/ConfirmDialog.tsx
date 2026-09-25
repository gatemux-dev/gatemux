import { createContext, ReactNode, useCallback, useContext, useMemo, useState } from 'react'
import { Modal } from './Modal'
import { Button } from './Button'

interface ConfirmInput {
  title: string
  description?: ReactNode
  confirmLabel?: string
  cancelLabel?: string
  destructive?: boolean
}

interface ConfirmContextValue {
  confirm: (input: ConfirmInput) => Promise<boolean>
}

const ConfirmContext = createContext<ConfirmContextValue | null>(null)

interface ResolveTuple {
  input: ConfirmInput
  resolve: (v: boolean) => void
}

// ConfirmProvider exposes a global async confirm() replacement that
// uses the in-app Modal instead of the browser prompt. Awaiting the
// promise resolves to true (confirmed) or false (cancelled or ESC).
export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState<ResolveTuple | null>(null)
  const [busy, setBusy] = useState(false)

  const confirm = useCallback((input: ConfirmInput): Promise<boolean> => {
    return new Promise((resolve) => {
      setOpen({ input, resolve })
    })
  }, [])

  // Stable identity so `confirm` is safe in effect dependency arrays.
  const confirmValue = useMemo(() => ({ confirm }), [confirm])

  const settle = (result: boolean) => {
    if (!open) return
    open.resolve(result)
    setOpen(null)
    setBusy(false)
  }

  return (
    <ConfirmContext.Provider value={confirmValue}>
      {children}
      {open && (
        <Modal
          title={open.input.title}
          description={open.input.description}
          onClose={() => settle(false)}
          size="sm"
        >
          <div className="flex justify-end gap-2 pt-2">
            <Button variant="secondary" onClick={() => settle(false)}>
              {open.input.cancelLabel ?? 'Cancel'}
            </Button>
            <Button
              variant={open.input.destructive ? 'danger' : 'primary'}
              loading={busy}
              onClick={() => {
                setBusy(true)
                settle(true)
              }}
            >
              {open.input.confirmLabel ?? 'Confirm'}
            </Button>
          </div>
        </Modal>
      )}
    </ConfirmContext.Provider>
  )
}

export function useConfirm(): ConfirmContextValue['confirm'] {
  const ctx = useContext(ConfirmContext)
  if (!ctx) throw new Error('useConfirm called outside ConfirmProvider')
  return ctx.confirm
}

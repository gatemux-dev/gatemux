// The GateMux mark: a single gateway arch — an opening in a solid block.
// Drawn in currentColor so it follows ink in both themes.
export default function BrandMark({ size = 24 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden="true" className="brand-mark-svg">
      <path
        fill="currentColor"
        fillRule="evenodd"
        d="M5 2h14a3 3 0 0 1 3 3v17h-7v-7a3 3 0 0 0-6 0v7H2V5a3 3 0 0 1 3-3Z"
      />
    </svg>
  )
}

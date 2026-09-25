import tseslint from 'typescript-eslint'
import reactHooks from 'eslint-plugin-react-hooks'

// Lean config: the rules that catch real defects (hooks misuse, unused
// state, type-unsafe patterns). Formatting stays out of lint.
export default tseslint.config(
  { ignores: ['dist', 'node_modules', 'vite.config.ts', 'tests'] },
  {
    files: ['src/**/*.{ts,tsx}'],
    extends: [...tseslint.configs.recommended],
    plugins: { 'react-hooks': reactHooks },
    rules: {
      ...reactHooks.configs.recommended.rules,
      // The codebase intentionally uses a few `{}`-ish empty patterns and
      // non-null assertions where the router guarantees presence.
      '@typescript-eslint/no-non-null-assertion': 'off',
      '@typescript-eslint/no-explicit-any': 'warn',
      'react-hooks/exhaustive-deps': 'warn',
    },
  },
)

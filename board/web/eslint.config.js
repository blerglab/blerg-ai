// ESLint flat config — TypeScript-aware lint gate for the web UI.
import js from "@eslint/js";
import reactHooks from "eslint-plugin-react-hooks";
import tseslint from "typescript-eslint";

export default tseslint.config(
  { ignores: ["dist/**", "node_modules/**"] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  reactHooks.configs.flat.recommended,
  {
    linterOptions: { reportUnusedDisableDirectives: "error" },
    rules: {
      "@typescript-eslint/no-unused-vars": ["error", { argsIgnorePattern: "^_" }],
      "@typescript-eslint/no-explicit-any": "off", // payloads are genuinely dynamic
      "no-empty": ["error", { allowEmptyCatch: true }],
      "no-useless-assignment": "off", // k++ key counters trip it
    },
  },
);

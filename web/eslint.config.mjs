import js from "@eslint/js";
import globals from "globals";

// Run from the repository root to include the runnable host example.
export default [
  {
    name: "agenstra/javascript",
    files: ["web/**/*.{js,mjs}", "examples/web-integration/**/*.js"],
    languageOptions: { ecmaVersion: 2022, sourceType: "module" },
    linterOptions: { reportUnusedDisableDirectives: "error" },
    rules: {
      ...js.configs.recommended.rules,
      "eqeqeq": ["error", "always", { null: "ignore" }],
      "no-eval": "error",
      "no-implied-eval": "error",
      "no-new-func": "error",
      "no-var": "error",
      "no-throw-literal": "error",
      "no-unused-vars": ["error", { argsIgnorePattern: "^_", caughtErrorsIgnorePattern: "^_" }],
      "prefer-const": "error",
      "semi": ["error", "always"]
    }
  },
  {
    name: "agenstra/browser",
    files: ["web/**/*.js", "examples/web-integration/**/*.js"],
    ignores: ["web/**/*.test.js"],
    languageOptions: { globals: globals.browser },
    rules: { "no-restricted-imports": ["error", { patterns: ["node:*"] }] }
  },
  {
    name: "agenstra/headless-client",
    files: ["web/agenstra-client.js"],
    rules: {
      "no-restricted-imports": ["error", {
        patterns: ["node:*"],
        paths: [{ name: "./agenstra-chat.js", message: "The headless client must work without the optional chat UI." }]
      }]
    }
  },
  {
    name: "agenstra/node",
    files: ["web/**/*.mjs", "web/**/*.test.js"],
    languageOptions: { globals: globals.node }
  },
  {
    name: "agenstra/admin-model-test",
    files: ["web/admin_models.test.js"],
    // This test provides a window fixture while executing the browser script.
    languageOptions: { globals: { window: "readonly" } }
  }
];

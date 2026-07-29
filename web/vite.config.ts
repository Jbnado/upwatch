import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],

  build: {
    // O build vai direto para onde o go:embed lê, de modo que compilar a
    // interface e compilar o binário sejam o mesmo gesto — sem etapa de
    // cópia que alguém esquece e publica uma interface velha.
    outDir: "../internal/web/dist",
    emptyOutDir: true,
    // Sem sourcemap no pacote final: ele dobraria o tamanho do binário
    // sem servir a quem apenas roda a ferramenta.
    sourcemap: false,
  },

  server: {
    // Em desenvolvimento a interface fala com o binário Go rodando ao
    // lado, então o caminho da API é o mesmo de produção.
    proxy: {
      "/api": "http://localhost:8080",
      "/healthz": "http://localhost:8080",
    },
  },

  test: {
    environment: "jsdom",
    setupFiles: ["./src/test-setup.ts"],
    globals: true,
  },
});

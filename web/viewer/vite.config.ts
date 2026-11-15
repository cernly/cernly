import { defineConfig } from "vite";

// The viewer is built to one JS chunk, one CSS file and fonts inlined as data:
// URIs; inline.mjs then folds the result and the WebAssembly module into a
// single HTML file (web/viewer/build.sh).
export default defineConfig({
  base: "./",
  resolve: { dedupe: ["react", "react-dom"] },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    assetsInlineLimit: Number.MAX_SAFE_INTEGER,
    cssCodeSplit: false,
    modulePreload: false,
    sourcemap: false,
    reportCompressedSize: false,
  },
});

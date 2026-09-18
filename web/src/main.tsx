import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { ThemeProvider } from "@lyra-ds/react/theme-provider";
import { App } from "./App";
import { bootLang } from "./i18n";
import { token } from "./lib";
import "@lyra-ds/styles";
import "./batuta-theme.css";
import "./app.css";

bootLang(token).finally(() => {
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <ThemeProvider defaultTheme="light" storageKey="bt-theme">
        <App />
      </ThemeProvider>
    </StrictMode>,
  );
});

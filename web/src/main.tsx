import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { ThemeProvider } from "@lyra-ds/react/theme-provider";
import { App } from "./App";
import "@lyra-ds/styles";
import "./batuta-theme.css";
import "./app.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ThemeProvider defaultTheme="light" storageKey="bt-theme">
      <App />
    </ThemeProvider>
  </StrictMode>,
);

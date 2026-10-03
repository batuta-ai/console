import { render, screen, within, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ThemeProvider } from "@lyra-ds/react/theme-provider";
import { expect, test, vi } from "vitest";
import { App } from "./App";
import type { Workspace } from "./lib";

test("creates a workspace from a chosen folder and selects the returned workspace", async () => {
  const workspaces: Workspace[] = [];
  const requests: { path: string; init?: RequestInit }[] = [];
  vi.stubGlobal("fetch", async (path: string, init?: RequestInit) => {
    requests.push({ path, init });
    if (path === "/v0/workspaces/pick-root") return Response.json({ path: "/tmp/synthetic-checkout" });
    if (path === "/v0/workspaces" && init?.method === "POST") {
      const workspace = { id: "ws-created", ...JSON.parse(String(init.body)), profile: true };
      workspaces.push(workspace);
      return Response.json(workspace);
    }
    if (path === "/v0/workspaces") return Response.json(workspaces);
    if (path.startsWith("/v0/watch")) return Response.json({ available: false });
    if (path === "/v0/skills") return Response.json({ skills: [] });
    return Response.json([]);
  });
  localStorage.setItem("bt-token", "synthetic-token");
  const user = userEvent.setup();
  render(<ThemeProvider defaultTheme="light"><App /></ThemeProvider>);
  await user.click(await screen.findByRole("button", { name: "New workspace" }));
  const dialog = screen.getByRole("dialog", { name: "New workspace" });
  const create = within(dialog).getByRole("button", { name: "Create" });
  expect(create).toBeDisabled();
  await user.click(within(dialog).getByRole("button", { name: "Choose folder" }));
  expect(await within(dialog).findByRole("textbox", { name: "Name" })).toHaveValue("synthetic-checkout");
  await user.clear(within(dialog).getByRole("textbox", { name: "Name" }));
  await user.type(within(dialog).getByRole("textbox", { name: "Name" }), "Journey workspace");
  expect(create).toBeEnabled();
  await user.click(create);
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  expect(await screen.findByRole("button", { name: /Journey workspace/ })).toBeVisible();
  expect(localStorage.getItem("bt-ws")).toBe("ws-created");
  const request = requests.find(({ path, init }) => path === "/v0/workspaces" && init?.method === "POST");
  expect(JSON.parse(String(request?.init?.body))).toEqual({ name: "Journey workspace", root: "/tmp/synthetic-checkout" });
  expect(request?.init?.headers).toEqual({ Authorization: "Bearer synthetic-token", "Content-Type": "application/json" });
});

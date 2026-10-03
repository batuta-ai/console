import { expect, test, vi } from "vitest";
import { api } from "./lib";

test("propagates the actual failed API response body", async () => {
  vi.stubGlobal("fetch", async () => new Response("workspace root does not exist", { status: 400 }));
  await expect(api("/v0/workspaces", { method: "POST", body: JSON.stringify({ name: "missing", root: "/missing" }) }))
    .rejects.toThrow("workspace root does not exist");
});

test("rejects unauthorized responses and clears the stale credential", async () => {
  localStorage.setItem("bt-token", "expired-synthetic-token");
  vi.stubGlobal("fetch", async () => new Response("unauthorized", { status: 401 }));
  await expect(api("/v0/accounts")).rejects.toThrow("invalid token. Run console --web to open with the token for this home");
  expect(localStorage.getItem("bt-token")).toBeNull();
});

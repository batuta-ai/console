import { expect, test, vi } from "vitest";
import { api, journeyMutation } from "./lib";

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

// The companion redirect.test.ts exercises actual fetch redirects over HTTP.
test.each(["journey", "POST", "PUT"])("%s requires transport rejection of redirects", async method => {
  let request: Request | undefined;
  vi.stubGlobal("fetch", async (path: string, init?: RequestInit) => {
    request = new Request(`http://localhost${path}`, init);
    return Response.json({ effect: "recorded" });
  });
  const body = { mission_id: "m1", version: "exact-version" };
  if (method === "journey") await journeyMutation("/mutate", body);
  else await api("/mutate", { method, body: JSON.stringify(body), redirect: "follow" });
  expect(request?.redirect).toBe("error");
  expect(await request?.json()).toEqual(body);
});

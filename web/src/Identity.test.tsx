import { fireEvent, render, screen, within, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ThemeProvider } from "@lyra-ds/react/theme-provider";
import { expect, test, vi } from "vitest";
import { App } from "./App";
import type { Account } from "./lib";

function fixture() {
  const accounts: Account[] = [];
  const requests: { path: string; body: unknown }[] = [];
  const control: { createError?: boolean; malformedCreate?: boolean; connectError?: boolean; lostCreate?: boolean; pending?: Promise<Response> } = {};
  vi.stubGlobal("fetch", async (path: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      const body = JSON.parse(String(init.body || "{}"));
      requests.push({ path, body });
      if (path === "/v0/accounts") {
        if (control.createError) return Response.json({ error: "invalid account label" }, { status: 400 });
        if (control.malformedCreate) return Response.json({ id: "accepted-incomplete" });
        const account = { ...body, id: `actual-${accounts.length + 1}`, provider: "OpenAI", isolation: "supported", connected: false };
        accounts.push(account);
        if (control.pending) return control.pending;
        if (control.lostCreate) throw new Error("lost create reply");
        return Response.json(account);
      }
      if (path.endsWith("/connect")) {
        if (control.connectError) return Response.json({ error: "connect failed" }, { status: 500 });
        const account = accounts.find(a => path === `/v0/accounts/${a.id}/connect`)!;
        account.connected = true;
        return Response.json(account);
      }
    }
    if (path === "/v0/accounts") return Response.json(accounts);
    if (path === "/v0/skills") return Response.json({ skills: [] });
    return Response.json([]);
  });
  const user = userEvent.setup();
  render(<ThemeProvider defaultTheme="light"><App /></ThemeProvider>);
  return { user, accounts, requests, control };
}
async function openAccount(user: ReturnType<typeof userEvent.setup>, label: string, provider = "codex") {
  await user.click(screen.getByRole("button", { name: "Add account" }));
  const dialog = screen.getByRole("dialog");
  if (provider === "claude") await user.click(within(dialog).getByRole("radio", { name: /Claude/ }));
  await user.type(within(dialog).getByRole("textbox"), label);
  await user.click(within(dialog).getByRole("button", { name: "Continue" }));
  return dialog;
}

test("consecutive account creation starts fresh and connects each actual returned identity", async () => {
  const { user, requests } = fixture();
  await user.click(screen.getByRole("button", { name: "Accounts and limits" }));
  for (const label of ["First", "Second"]) {
    const dialog = await openAccount(user, label, label === "First" ? "claude" : "codex");
    await user.click(within(dialog).getByRole("button", { name: /Connect/ }));
    await user.click(await within(dialog).findByRole("button", { name: "Save account" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  }
  expect(requests).toEqual([
    { path: "/v0/accounts", body: { label: "First", adapter: "claude" } },
    { path: "/v0/accounts/actual-1/connect", body: {} },
    { path: "/v0/accounts", body: { label: "Second", adapter: "codex" } },
    { path: "/v0/accounts/actual-2/connect", body: {} },
  ]);
  expect(document.querySelector("button button")).toBeNull();
});

test("partial account creation requires inspection and retries only the accepted account", async () => {
  const { user, requests, control } = fixture();
  control.connectError = true;
  await user.click(screen.getByRole("button", { name: "Accounts and limits" }));
  const dialog = await openAccount(user, "Retained");
  await user.click(within(dialog).getByRole("button", { name: /Connect/ }));
  expect(await within(dialog).findByText(/connect failed/)).toBeVisible();
  expect(within(dialog).getByRole("button", { name: /Connect/ })).toBeDisabled();
  control.connectError = false;
  await user.click(within(dialog).getByRole("button", { name: "Inspect creation" }));
  await user.click(within(dialog).getByRole("button", { name: /Connect/ }));
  await within(dialog).findByRole("button", { name: "Save account" });
  expect(requests.map(r => r.path)).toEqual(["/v0/accounts", "/v0/accounts/actual-1/connect", "/v0/accounts/actual-1/connect"]);
});

test("lost account reply retains context across navigation and requires explicit identity selection", async () => {
  const { user, requests, control } = fixture();
  control.lostCreate = true;
  await user.click(screen.getByRole("button", { name: "Accounts and limits" }));
  const dialog = await openAccount(user, "Uncertain");
  await user.click(within(dialog).getByRole("button", { name: /Connect/ }));
  await within(dialog).findByText(/lost create reply/);
  await user.keyboard("{Escape}");
  await user.click(screen.getByRole("button", { name: "Home" }));
  await user.click(screen.getByRole("button", { name: "Accounts and limits" }));
  await user.click(screen.getByRole("button", { name: "Add account" }));
  await user.click(screen.getByRole("button", { name: "Inspect creation" }));
  await user.click(await screen.findByRole("button", { name: /Use account actual-1/ }));
  await user.click(screen.getByRole("button", { name: /Connect/ }));
  await screen.findByRole("button", { name: "Save account" });
  expect(requests.map(r => r.path)).toEqual(["/v0/accounts", "/v0/accounts/actual-1/connect"]);
});

test("pending account submission cannot be doubled", async () => {
  const { user, requests, control } = fixture();
  let resolve!: (value: Response) => void;
  control.pending = new Promise(r => { resolve = r; });
  await user.click(screen.getByRole("button", { name: "Accounts and limits" }));
  const dialog = await openAccount(user, "Pending");
  const connect = within(dialog).getByRole("button", { name: /Connect/ });
  fireEvent.click(connect); fireEvent.click(connect);
  expect(requests).toHaveLength(1);
  resolve(Response.json({ id: "actual-1", label: "Pending", adapter: "codex", isolation: "supported", connected: false }));
  await screen.findByRole("button", { name: "Save account" });
});

test("rejected account can be edited locally after empty inspection without replaying the request", async () => {
  const { user, requests, control } = fixture();
  control.createError = true;
  await user.click(screen.getByRole("button", { name: "Accounts and limits" }));
  const dialog = await openAccount(user, "Rejected label", "claude");
  await user.click(within(dialog).getByRole("button", { name: /Connect/ }));
  await within(dialog).findByText(/invalid account label/);
  expect(within(dialog).queryByRole("button", { name: "Edit draft" })).toBeNull();
  await user.click(within(dialog).getByRole("button", { name: "Inspect creation" }));
  await user.click(await within(dialog).findByRole("button", { name: "Edit draft" }));
  expect(within(dialog).getByRole("textbox")).toHaveValue("Rejected label");
  expect(within(dialog).getByRole("radio", { name: /Claude/ })).toBeChecked();
  expect(within(dialog).getByText(/previous attempt/)).toBeVisible();
  expect(within(dialog).getByText(/invalid account label/)).toBeVisible();
  expect(requests).toHaveLength(1);
  await user.clear(within(dialog).getByRole("textbox"));
  await user.type(within(dialog).getByRole("textbox"), "Corrected label");
  await user.click(within(dialog).getByRole("button", { name: "Continue" }));
  control.createError = false;
  await user.click(within(dialog).getByRole("button", { name: /Connect/ }));
  await within(dialog).findByRole("button", { name: "Save account" });
  expect(requests).toEqual([
    { path: "/v0/accounts", body: { label: "Rejected label", adapter: "claude" } },
    { path: "/v0/accounts", body: { label: "Corrected label", adapter: "claude" } },
    { path: "/v0/accounts/actual-1/connect", body: {} },
  ]);
});

test("incomplete account creation retains the returned identity and forbids local edit or replay", async () => {
  const { user, requests, control } = fixture();
  control.malformedCreate = true;
  await user.click(screen.getByRole("button", { name: "Accounts and limits" }));
  const dialog = await openAccount(user, "Accepted incomplete");
  await user.click(within(dialog).getByRole("button", { name: /Connect/ }));
  await within(dialog).findByRole("alert");
  expect(within(dialog).getByText(/accepted-incomplete/)).toBeVisible();
  await user.click(within(dialog).getByRole("button", { name: "Inspect creation" }));
  expect(within(dialog).queryByRole("button", { name: "Edit draft" })).toBeNull();
  expect(within(dialog).getByRole("button", { name: /Connect/ })).toBeDisabled();
  expect(within(dialog).getByRole("button", { name: "Inspect creation" })).toBeEnabled();
  expect(requests).toHaveLength(1);
});

function squadFixture() {
  const squads: { id: string; name: string; members: unknown[] }[] = [];
  const missions: { id: string; squad_id: string; workspace_id: string; title: string }[] = [];
  const requests: { path: string; body: Record<string, unknown> }[] = [];
  const control: { saveRejected?: boolean; malformedSave?: boolean; missionError?: boolean; lostSave?: boolean; lostAcceptedMission?: boolean; pendingSave?: Promise<Response> } = {};
  vi.stubGlobal("fetch", async (path: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      const body = JSON.parse(String(init.body || "{}")); requests.push({ path, body });
      if (path.endsWith("/validate")) return Response.json({ validation: { title: "Draft checked", text: "No save occurred" } });
      if (path === "/v0/squads") {
        if (control.malformedSave) return Response.json({ id: "accepted-incomplete" });
        if (control.saveRejected) return Response.json({ error: "invalid draft" }, { status: 400 });
        const q = { ...body, id: body.id || `saved-${squads.length+1}`, version: 1, members: body.members.map((m: object, i: number) => ({ ...m, id: `member-${i}` })) };
        squads.push(q);
        if (control.pendingSave) return control.pendingSave;
        if (control.lostSave) throw new Error("lost squad reply");
        return Response.json(q);
      }
      if (path === "/v0/missions") {
        if (control.missionError) throw new Error("lost mission reply");
        const m = { ...body, id: "mission-actual", status: "open" }; missions.push(m); if (control.lostAcceptedMission) throw new Error("accepted mission reply lost"); return Response.json(m);
      }
    }
    if (path === "/v0/workspaces") return Response.json([{ id: "workspace-exact", name: "Project", root: "/tmp/project", profile: true }]);
    if (path === "/v0/squads") return Response.json(squads);
    if (path === "/v0/missions") return Response.json(missions);
    if (path === "/v0/skills") return Response.json({ skills: [] });
    return Response.json([]);
  });
  render(<ThemeProvider defaultTheme="light"><App /></ThemeProvider>);
  return { user: userEvent.setup(), requests, squads, missions, control };
}
async function newSquad(user: ReturnType<typeof userEvent.setup>, name: string) {
  await user.click(screen.getByRole("button", { name: "Squads" }));
  await user.click(screen.getByRole("button", { name: "Create squad" }));
  const input = screen.getByRole("textbox", { name: "Name" });
  await user.clear(input); await user.type(input, name);
}

test("consecutive squads save only once per flow with fresh draft identities", async () => {
  const { user, requests } = squadFixture();
  for (const name of ["First squad", "Second squad"]) {
    await newSquad(user, name);
    await user.dblClick(screen.getByRole("button", { name: "Save version" }));
    await screen.findByRole("button", { name: "Create squad" });
  }
  expect(requests).toHaveLength(2);
  expect(requests.map(r => r.body)).toEqual(["First squad", "Second squad"].map(name => ({ id: "", name, purpose: "", origin: "custom", members: [], version: 0, draft: true })));
});

test("partial squad save retains exact saved identity and retries mission only after inspection", async () => {
  const { user, requests, control } = squadFixture(); control.missionError = true;
  await newSquad(user, "Retained squad");
  await user.click(screen.getByRole("button", { name: "Create mission with this squad" }));
  await screen.findByText(/lost mission reply/);
  expect(screen.queryByRole("button", { name: "Edit draft" })).toBeNull();
  expect(screen.getByRole("button", { name: "Create mission with this squad" })).toBeDisabled();
  await user.click(screen.getByRole("button", { name: "Inspect creation" }));
  control.missionError = false;
  expect(screen.queryByRole("button", { name: "Edit draft" })).toBeNull();
  await user.click(screen.getByRole("button", { name: "Create mission with this squad" }));
  await screen.findByRole("button", { name: "Create squad" });
  expect(requests.map(r => r.path)).toEqual(["/v0/squads", "/v0/missions", "/v0/missions"]);
  expect(requests[2].body).toEqual({ workspace_id: "workspace-exact", title: "Retained squad", squad_id: "saved-1" });
});

test("lost squad save survives navigation and selects a catalog identity without saving again", async () => {
  const { user, requests, control } = squadFixture(); control.lostSave = true;
  await newSquad(user, "Uncertain squad");
  await user.click(screen.getByRole("button", { name: "Save version" }));
  await screen.findByText(/lost squad reply/);
  await user.click(screen.getByRole("button", { name: "Home" }));
  await user.click(screen.getByRole("button", { name: "Squads" }));
  expect(screen.getByRole("button", { name: "Create squad" })).toBeDisabled();
  await user.click(screen.getByRole("button", { name: "Resume creation" }));
  await user.click(screen.getByRole("button", { name: "Inspect creation" }));
  expect(screen.queryByRole("button", { name: "Edit draft" })).toBeNull();
  await user.click(await screen.findByRole("button", { name: /Use squad saved-1/ }));
  await user.click(screen.getByRole("button", { name: "Save version" }));
  await screen.findByRole("button", { name: "Create squad" });
  expect(requests).toHaveLength(1);
});


test("a lost accepted mission is recovered by its exact ID without resaving the squad", async () => {
  const { user, requests, control } = squadFixture(); control.lostAcceptedMission = true;
  await newSquad(user, "Accepted mission");
  await user.click(screen.getByRole("button", { name: "Add member" }));
  await user.click(screen.getByRole("button", { name: "Create mission with this squad" }));
  await screen.findByText(/accepted mission reply lost/);
  await user.click(screen.getByRole("button", { name: "Inspect creation" }));
  await user.click(await screen.findByRole("button", { name: "Use mission mission-actual" }));
  await screen.findByRole("button", { name: "Create squad" });
  expect(requests.map(r => r.path)).toEqual(["/v0/squads", "/v0/missions"]);
  expect(requests[1].body).toEqual({ workspace_id: "workspace-exact", title: "Accepted mission", squad_id: "saved-1" });
  expect(document.querySelector("button button")).toBeNull();
});

test("pending squad save freezes inputs, blocks duplicate actions and retains the saved members", async () => {
  const { user, requests, control, squads } = squadFixture();
  let resolve!: (response: Response) => void;
  control.pendingSave = new Promise(r => { resolve = r; });
  await newSquad(user, "Pending squad");
  await user.click(screen.getByRole("button", { name: "Add member" }));
  const save = screen.getByRole("button", { name: "Save version" });
  fireEvent.click(save); fireEvent.click(save);
  expect(requests).toHaveLength(1);
  expect(screen.getByRole("textbox", { name: "Name" })).toBeDisabled();
  expect(screen.queryByRole("button", { name: "Edit draft" })).toBeNull();
  expect(screen.getByRole("button", { name: "Create mission with this squad" })).toBeDisabled();
  resolve(Response.json(squads[0]));
  await screen.findByRole("button", { name: "Create squad" });
  expect(squads[0].members).toEqual([expect.objectContaining({ id: "member-0" })]);
});

test("canceling an unsubmitted account starts a fresh provider and draft with keyboard access", async () => {
  const { user, requests } = fixture();
  await user.click(screen.getByRole("button", { name: "Accounts and limits" }));
  await user.click(screen.getByRole("button", { name: "Add account" }));
  await user.click(screen.getByRole("radio", { name: /Claude/ }));
  await user.type(screen.getByRole("textbox"), "Canceled");
  await user.keyboard("{Escape}");
  const button = screen.getByRole("button", { name: "Add account" }); button.focus();
  await user.keyboard("{Enter}");
  expect(screen.getByRole("textbox")).toHaveValue("");
  expect(screen.getByRole("radio", { name: /Codex/ })).toBeChecked();
  expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled();
  expect(requests).toHaveLength(0);
});


test("a new squad can validate its submitted draft without creating an identity", async () => {
  const { user, requests } = squadFixture();
  await newSquad(user, "Validate draft");
  await user.click(screen.getByRole("button", { name: "Validate" }));
  await screen.findByText("Draft checked. No save occurred");
  expect(requests).toEqual([{ path: "/v0/squads/draft/validate", body: { id: "", name: "Validate draft", purpose: "", origin: "custom", members: [], version: 0, draft: true } }]);
});

test("conductor recovery account message keeps valid paragraph DOM", async () => {
  const { user, control, requests } = fixture(); control.connectError = true;
  await user.click(screen.getByRole("button", { name: "Accounts and limits" }));
  const dialog = await openAccount(user, "Retained");
  await user.click(within(dialog).getByRole("button", { name: /Connect/ }));
  expect(await within(dialog).findByText(/connect failed/)).toBeVisible();
  expect(within(dialog).getByText(/actual-1/)).toBeVisible();
  expect(requests.map(r => r.path)).toEqual(["/v0/accounts", "/v0/accounts/actual-1/connect"]);
  expect(dialog.querySelector("p p")).toBeNull();
});

test("conductor completed account close starts fresh without deleting accepted identity", async () => {
  const { user, accounts, requests } = fixture();
  await user.click(screen.getByRole("button", { name: "Accounts and limits" }));
  const dialog = await openAccount(user, "Complete before close");
  await user.click(within(dialog).getByRole("button", { name: /Connect/ }));
  await within(dialog).findByRole("button", { name: "Save account" });
  dialog.focus();
  await user.keyboard("{Escape}");
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  await user.click(screen.getByRole("button", { name: "Add account" }));
  expect(screen.getByRole("textbox")).toHaveValue("");
  expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled();
  expect(accounts.map(a => a.id)).toEqual(["actual-1"]);
  expect(requests.map(r => r.path)).toEqual(["/v0/accounts", "/v0/accounts/actual-1/connect"]);
});

test("conductor rejected unsaved squad can explicitly edit after empty inspection without replay", async () => {
  const { user, control, requests, squads } = squadFixture(); control.saveRejected = true;
  await newSquad(user, "Broken draft");
  await user.click(screen.getByRole("button", { name: "Save version" }));
  await screen.findByText(/invalid draft/);
  await user.click(screen.getByRole("button", { name: "Inspect creation" }));
  await screen.findByText(/No matching target found/);
  await user.click(screen.getByRole("button", { name: "Edit draft" }));
  const input = screen.getByRole("textbox", { name: "Name" });
  expect(input).toBeEnabled();
  await user.clear(input); await user.type(input, "Fixed draft");
  expect(requests).toHaveLength(1); expect(squads).toHaveLength(0);
  control.saveRejected = false;
  await user.click(screen.getByRole("button", { name: "Save version" }));
  await screen.findByRole("button", { name: "Create squad" });
  expect(requests.map(r => r.body.name)).toEqual(["Broken draft", "Fixed draft"]);
  expect(squads).toHaveLength(1);
});


test("explicit successful account close refreshes the persisted account and resets the provider", async () => {
  const { user, requests } = fixture();
  await user.click(screen.getByRole("button", { name: "Accounts and limits" }));
  const dialog = await openAccount(user, "Accepted Claude", "claude");
  await user.click(within(dialog).getByRole("button", { name: /Connect/ }));
  await within(dialog).findByRole("button", { name: "Save account" });
  dialog.focus();
  await user.keyboard("{Escape}");
  expect(await screen.findByRole("button", { name: /Accepted Claude/ })).toBeVisible();
  await user.click(screen.getByRole("button", { name: "Add account" }));
  expect(screen.getByRole("radio", { name: /Codex/ })).toBeChecked();
  expect(screen.getByRole("textbox")).toHaveValue("");
  expect(requests).toEqual([
    { path: "/v0/accounts", body: { label: "Accepted Claude", adapter: "claude" } },
    { path: "/v0/accounts/actual-1/connect", body: {} },
  ]);
});

test("editing an inspected rejected draft preserves the uncertain original context across navigation", async () => {
  const { user, control, requests } = squadFixture(); control.saveRejected = true;
  await newSquad(user, "Original uncertain draft");
  await user.click(screen.getByRole("button", { name: "Save version" }));
  await screen.findByText(/invalid draft/);
  expect(screen.queryByRole("button", { name: "Edit draft" })).toBeNull();
  await user.click(screen.getByRole("button", { name: "Inspect creation" }));
  await screen.findByText(/No matching target found/);
  await user.click(screen.getByRole("button", { name: "Edit draft" }));
  await user.clear(screen.getByRole("textbox", { name: "Name" }));
  await user.type(screen.getByRole("textbox", { name: "Name" }), "Repaired local draft");
  expect(screen.getByText(/Original uncertain draft.*workspace-exact/)).toBeVisible();
  expect(screen.getByText(/outcome remains unconfirmed/i)).toBeVisible();
  expect(screen.getByText(/invalid draft/)).toBeVisible();
  await user.click(screen.getByRole("button", { name: "Back" }));
  await user.click(screen.getByRole("button", { name: "Resume creation" }));
  expect(screen.getByRole("textbox", { name: "Name" })).toHaveValue("Repaired local draft");
  expect(screen.getByText(/Original uncertain draft.*workspace-exact/)).toBeVisible();
  expect(requests).toHaveLength(1);
});


test("an acknowledged squad ID in an incomplete save reply never unlocks draft editing", async () => {
  const { user, control, requests } = squadFixture(); control.malformedSave = true;
  await newSquad(user, "Acknowledged draft");
  await user.click(screen.getByRole("button", { name: "Save version" }));
  await screen.findByRole("button", { name: "Inspect creation" });
  expect(screen.getByText(/accepted-incomplete/)).toBeVisible();
  await user.click(screen.getByRole("button", { name: "Inspect creation" }));
  await screen.findByText(/No matching target found/);
  expect(screen.queryByRole("button", { name: "Edit draft" })).toBeNull();
  expect(screen.getByRole("textbox", { name: "Name" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "Save version" })).toBeDisabled();
  expect(screen.getByText(/accepted-incomplete/)).toBeVisible();
  expect(requests).toHaveLength(1);
});

test("conductor rejected edit of existing squad preserves ID and can repair local fields", async () => {
  const { user, control, requests, squads } = squadFixture();
  await newSquad(user, "Existing saved squad");
  await user.click(screen.getByRole("button", { name: "Save version" }));
  await screen.findByRole("button", { name: "Create squad" });
  expect(squads.map(q => q.id)).toEqual(["saved-1"]);
  await user.click(screen.getByRole("button", { name: "Existing saved squad" }));
  control.saveRejected = true;
  await user.click(screen.getByRole("button", { name: "Save version" }));
  await screen.findByText(/invalid draft/);
  await user.click(screen.getByRole("button", { name: "Inspect creation" }));
  await screen.findByText(/No matching target found/);
  expect(requests).toHaveLength(2);
  expect(requests[1].body.id).toBe("saved-1");
  await user.click(screen.getByRole("button", { name: "Edit draft" }));
  expect(screen.getByRole("textbox", { name: "Name" })).toBeEnabled();
  expect(screen.getByText(/saved-1/)).toBeVisible();
  expect(requests).toHaveLength(2);
  expect(squads.map(q => q.id)).toEqual(["saved-1"]);
});

for (const scenario of ["changed", "missing", "duplicate"] as const) {
  test(`existing squad local repair stays blocked for ${scenario} inspected identity`, async () => {
    const { user, control, requests, squads } = squadFixture();
    await newSquad(user, "Existing saved squad");
    await user.click(screen.getByRole("button", { name: "Save version" }));
    await screen.findByRole("button", { name: "Create squad" });
    await user.click(screen.getByRole("button", { name: "Existing saved squad" }));
    control.saveRejected = true;
    await user.click(screen.getByRole("button", { name: "Save version" }));
    await screen.findByText(/invalid draft/);
    if (scenario === "changed") { squads[0].name = "Foreign update"; Object.assign(squads[0], { version: 2 }); }
    if (scenario === "missing") squads.splice(0);
    if (scenario === "duplicate") squads.push({ ...squads[0] });
    await user.click(screen.getByRole("button", { name: "Inspect creation" }));
    await screen.findByText(scenario === "changed" ? /The saved squad changed/ : /No matching target found/);
    expect(screen.queryByRole("button", { name: "Edit draft" })).toBeNull();
    expect(screen.getByRole("textbox", { name: "Name" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Save version" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Inspect creation" })).toBeEnabled();
    expect(requests).toHaveLength(2);
    expect(requests[1].body.id).toBe("saved-1");
  });
}

import { useState } from "react";
import { render, screen, within, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { Button, Dialog, Field, Input, Select, Tabs } from "./ui";

const scrollByDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollBy");
beforeEach(() => {
  // WORKAROUND: jsdom has no scrolling implementation; model offsets, not layout.
  Object.defineProperty(HTMLElement.prototype, "scrollBy", {
    configurable: true,
    value(this: HTMLElement, options: ScrollToOptions) {
      this.scrollLeft += options.left ?? 0;
      this.scrollTop += options.top ?? 0;
    },
  });
});
afterEach(() => {
  if (scrollByDescriptor) Object.defineProperty(HTMLElement.prototype, "scrollBy", scrollByDescriptor);
  else Reflect.deleteProperty(HTMLElement.prototype, "scrollBy");
});

test("dialog preserves controlled fields, keyboard dismissal and trigger focus", async () => {
  function Form() {
    const [open, setOpen] = useState(false);
    const [name, setName] = useState("");
    const [provider, setProvider] = useState("codex");
    return <>
      <Button onClick={() => setOpen(true)}>Edit account</Button>
      <Dialog open={open} title="Account details" onClose={() => setOpen(false)}>
        <Field label="Name"><Input value={name} onChange={event => setName(event.target.value)} /></Field>
        <Field label="Provider"><Select value={provider} onChange={event => setProvider(event.target.value)}>
          <option value="codex">Codex</option><option value="claude">Claude</option>
        </Select></Field>
      </Dialog>
    </>;
  }
  const user = userEvent.setup();
  render(<Form />);
  const trigger = screen.getByRole("button", { name: "Edit account" });
  // WORKAROUND: jsdom has no layout; give only the return-focus target a visible rectangle.
  const rect = new DOMRect(0, 0, 120, 32);
  vi.spyOn(trigger, "getClientRects").mockReturnValue({
    0: rect, length: 1, item: index => index === 0 ? rect : null,
    [Symbol.iterator]: () => [rect][Symbol.iterator](),
  });
  await user.click(trigger);
  const dialog = within(screen.getByRole("dialog", { name: "Account details" }));
  await user.type(dialog.getByRole("textbox", { name: "Name" }), "Local account");
  await user.selectOptions(dialog.getByRole("combobox", { name: "Provider" }), "claude");
  expect(dialog.getByRole("textbox", { name: "Name" })).toHaveValue("Local account");
  expect(dialog.getByRole("combobox", { name: "Provider" })).toHaveValue("claude");
  await user.keyboard("{Escape}");
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  await waitFor(() => expect(trigger).toHaveFocus());
  await user.click(trigger);
  expect(screen.getByRole("textbox", { name: "Name" })).toHaveValue("Local account");
  expect(screen.getByRole("combobox", { name: "Provider" })).toHaveValue("claude");
});

test("tabs propagate selection to the controlled content", async () => {
  function Content() {
    const [active, setActive] = useState("terminal");
    return <>
      <Tabs active={active} onChange={setActive} items={[{ id: "terminal", label: "Terminal" }, { id: "history", label: "History" }]} />
      <p>{active === "terminal" ? "Live output" : "Session history"}</p>
    </>;
  }
  const user = userEvent.setup();
  render(<Content />);
  await user.click(screen.getByRole("tab", { name: "History" }));
  expect(screen.getByRole("tab", { name: "History" })).toHaveAttribute("aria-selected", "true");
  expect(screen.getByText("Session history")).toBeVisible();
});

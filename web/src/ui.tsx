import { type CSSProperties, type ReactNode } from "react";
import { T } from "./i18n";
import { Alert as LyraAlert } from "@lyra-ds/react/alert";
import { AppSidebar } from "@lyra-ds/react/app-sidebar";
import { Avatar as LyraAvatar } from "@lyra-ds/react/avatar";
import { Badge } from "@lyra-ds/react/badge";
import { BottomNav } from "@lyra-ds/react/bottom-nav";
import { Brand } from "@lyra-ds/react/brand";
import { Breadcrumb } from "@lyra-ds/react/breadcrumb";
import { Button as LyraButton, type ButtonProps } from "@lyra-ds/react/button";
import { Card } from "@lyra-ds/react/card";
import { Dialog as LyraDialog, type DialogProps } from "@lyra-ds/react/dialog";
import { Drawer as LyraDrawer } from "@lyra-ds/react/drawer";
import { EmptyState } from "@lyra-ds/react/empty-state";
import { IconButton as LyraIconButton, type IconButtonProps } from "@lyra-ds/react/icon-button";
import { Input as LyraInput, type InputProps } from "@lyra-ds/react/input";
import { PageHeader } from "@lyra-ds/react/page-header";
import { Progress as LyraProgress } from "@lyra-ds/react/progress";
import { Select as LyraSelect, type SelectProps } from "@lyra-ds/react/select";
import { Shell } from "@lyra-ds/react/shell";
import { Stat } from "@lyra-ds/react/stat";
import { Stepper as LyraStepper } from "@lyra-ds/react/stepper";
import { Tabs as LyraTabs, TabsList, TabsTrigger } from "@lyra-ds/react/tabs";
import { WorkspaceSwitcher as LyraWorkspaceSwitcher } from "@lyra-ds/react/workspace-switcher";

export { AppSidebar, Badge, BottomNav, Brand, Breadcrumb, Card, EmptyState, PageHeader, Shell, Stat };

export function Button({ size = "sm", ...props }: ButtonProps) {
  return <LyraButton size={size} {...props} />;
}

export function IconButton({ variant = "ghost", size = "sm", ...props }: IconButtonProps) {
  return <LyraIconButton variant={variant} size={size} {...props} />;
}

export function Avatar({ name, size = "sm" }: { name: string; size?: "xs" | "sm" | "md" }) {
  const lyraSize = size === "xs" ? "sm" : size;
  return <LyraAvatar name={name} size={lyraSize} className={size === "xs" ? "lyra-avatar--xs" : undefined} />;
}

export function Progress({ value, unknown, tone }: { value?: number | null; unknown?: boolean; tone?: "success" | "danger" }) {
  if (unknown || value == null) return <div className="lyra-progress lyra-progress--unknown" title={T("unknown")} />;
  return <LyraProgress value={value} tone={tone} />;
}

export function Alert({ tone, title, children }: { tone: "info" | "success" | "warning" | "danger"; title?: string; children?: ReactNode }) {
  return (
    <LyraAlert tone={tone} title={title}>
      {children ?? null}
    </LyraAlert>
  );
}

export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label className="lyra-field">
      <span className="lyra-label">{label}</span>
      {children}
      {hint ? <span className="lyra-hint">{hint}</span> : null}
    </label>
  );
}

export function Input({ size = "sm", ...props }: InputProps) {
  return <LyraInput size={size} {...props} />;
}

export function Select({ size = "sm", ...props }: SelectProps) {
  return <LyraSelect size={size} {...props} />;
}

export function Tabs({
  variant = "pills",
  active,
  onChange,
  items,
}: {
  variant?: "line" | "pills";
  active: string;
  onChange?: (value: string) => void;
  items: { id: string; label: string; count?: number }[];
}) {
  return (
    <LyraTabs variant={variant} active={active} onChange={onChange}>
      <TabsList>
        {items.map((item) => (
          <TabsTrigger key={item.id} value={item.id} count={item.count}>
            {item.label}
          </TabsTrigger>
        ))}
      </TabsList>
    </LyraTabs>
  );
}

export function Stepper({ steps, current }: { steps: string[]; current: number }) {
  return <LyraStepper steps={steps} active={Math.max(0, current - 1)} />;
}

export function Dialog({ closeLabel, ...props }: DialogProps) {
  return <LyraDialog closeLabel={closeLabel ?? T("close")} {...props} />;
}

export function Drawer({
  open,
  title,
  onClose,
  children,
  footer,
  width,
}: {
  open: boolean;
  title: string;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  width?: number;
}) {
  return (
    <LyraDrawer
      open={open}
      title={title}
      onClose={onClose}
      footer={footer}
      closeLabel={T("close")}
      className="lyra-drawer--accounts"
      style={width ? ({ maxWidth: width } as CSSProperties) : undefined}
    >
      {children}
    </LyraDrawer>
  );
}

export function WorkspaceSwitcher({
  workspaces,
  current,
  collapsed,
  onChange,
  onCreate,
}: {
  workspaces: { id: string; name: string; root: string }[];
  current?: { id: string; name: string; root: string };
  collapsed?: boolean;
  onChange: (id: string) => void;
  onCreate: () => void;
}) {
  return (
    <LyraWorkspaceSwitcher
      className={collapsed ? "lyra-wssw--collapsed" : undefined}
      workspaces={workspaces.map((w) => ({ id: w.id, name: w.name, plan: w.root }))}
      current={current?.id}
      onChange={onChange}
      onCreate={onCreate}
      createLabel={T("new_workspace")}
    />
  );
}

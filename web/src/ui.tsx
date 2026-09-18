import { type CSSProperties, type ReactNode } from "react";
import { Alert as LyraAlert } from "@lyra-ds/react/alert";
import { Avatar as LyraAvatar } from "@lyra-ds/react/avatar";
import { Badge } from "@lyra-ds/react/badge";
import { Button as LyraButton, type ButtonProps } from "@lyra-ds/react/button";
import { Dialog as LyraDialog, type DialogProps } from "@lyra-ds/react/dialog";
import { Drawer as LyraDrawer } from "@lyra-ds/react/drawer";
import { IconButton as LyraIconButton, type IconButtonProps } from "@lyra-ds/react/icon-button";
import { Input as LyraInput, type InputProps } from "@lyra-ds/react/input";
import { Progress as LyraProgress } from "@lyra-ds/react/progress";
import { Select as LyraSelect, type SelectProps } from "@lyra-ds/react/select";
import { Stat } from "@lyra-ds/react/stat";
import { Stepper as LyraStepper } from "@lyra-ds/react/stepper";
import { Tabs as LyraTabs, type TabsProps } from "@lyra-ds/react/tabs";
import { WorkspaceSwitcher as LyraWorkspaceSwitcher } from "@lyra-ds/react/workspace-switcher";

export { Badge, Stat };

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
  if (unknown || value == null) return <div className="lyra-progress lyra-progress--unknown" title="desconhecido" />;
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

export function Tabs({ variant = "pills", ...props }: TabsProps) {
  return <LyraTabs variant={variant} {...props} />;
}

export function Stepper({ steps, current }: { steps: string[]; current: number }) {
  return <LyraStepper steps={steps} active={Math.max(0, current - 1)} />;
}

export function Dialog({ closeLabel = "Fechar", ...props }: DialogProps) {
  return <LyraDialog closeLabel={closeLabel} {...props} />;
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
      closeLabel="Fechar"
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
      createLabel="Novo workspace"
    />
  );
}

import { type ButtonHTMLAttributes, type CSSProperties, type InputHTMLAttributes, type ReactNode, type SelectHTMLAttributes } from "react";

function cx(...parts: Array<string | false | null | undefined>) {
  return parts.filter(Boolean).join(" ");
}

export function Button({
  variant = "primary",
  size = "sm",
  full,
  loading,
  className,
  children,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "primary" | "secondary" | "soft" | "ghost" | "danger";
  size?: "sm" | "md" | "lg";
  full?: boolean;
  loading?: boolean;
}) {
  return (
    <button
      className={cx("lyra-btn", `lyra-btn--${variant}`, `lyra-btn--${size}`, full && "lyra-btn--full", loading && "lyra-btn--loading", className)}
      disabled={props.disabled || loading}
      {...props}
    >
      {loading ? <span className="lyra-btn__spinner" /> : null}
      {children}
    </button>
  );
}

export function IconButton({
  label,
  className,
  children,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { label: string }) {
  return (
    <button className={cx("lyra-btn", "lyra-btn--ghost", "lyra-btn--sm", "lyra-btn--icon", className)} aria-label={label} title={label} {...props}>
      {children}
    </button>
  );
}

export function Badge({
  tone = "neutral",
  dot,
  children,
}: {
  tone?: "neutral" | "accent" | "success" | "warning" | "danger" | "info";
  dot?: boolean;
  children: ReactNode;
}) {
  return (
    <span className={cx("lyra-badge", `lyra-badge--${tone}`)}>
      {dot ? <span className="lyra-badge__dot" /> : null}
      {children}
    </span>
  );
}

export function Avatar({ name, size = "sm" }: { name: string; size?: "xs" | "sm" | "md" }) {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  const letters = parts.length === 0 ? "BT" : parts.length === 1 ? parts[0].slice(0, 2).toUpperCase() : (parts[0][0] + parts[1][0]).toUpperCase();
  return (
    <span className={cx("lyra-avatar", `lyra-avatar--${size}`)} title={name}>
      {letters}
    </span>
  );
}

export function Progress({ value, unknown, tone }: { value?: number | null; unknown?: boolean; tone?: "success" | "danger" }) {
  if (unknown || value == null) return <div className="lyra-progress lyra-progress--unknown" title="desconhecido" />;
  return (
    <div className={cx("lyra-progress", tone && `lyra-progress--${tone}`)}>
      <div className="lyra-progress__fill" style={{ width: `${Math.max(0, Math.min(100, value))}%` }} />
    </div>
  );
}

export function Alert({ tone, title, children }: { tone: "info" | "success" | "warning" | "danger"; title?: string; children?: ReactNode }) {
  return (
    <div className={cx("lyra-alert", `lyra-alert--${tone}`)}>
      <div>
        {title ? <p className="lyra-alert__title">{title}</p> : null}
        {children ? <p className="lyra-alert__body">{children}</p> : null}
      </div>
    </div>
  );
}

export function Stat({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="lyra-stat">
      <span className="lyra-stat__label">{label}</span>
      <span className="lyra-stat__value" style={{ fontSize: 22 }}>
        {value}
      </span>
    </div>
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

export function Input(props: InputHTMLAttributes<HTMLInputElement> & { sizeVariant?: "sm" | "md" }) {
  const { sizeVariant = "sm", className, ...rest } = props;
  return <input className={cx("lyra-input", sizeVariant === "sm" && "lyra-input--sm", className)} {...rest} />;
}

export function Select(props: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <span className="lyra-select-wrap">
      <select className={cx("lyra-input", "lyra-input--sm")} {...props} />
    </span>
  );
}

export function Tabs({
  items,
  active,
  onChange,
}: {
  items: { id: string; label: string; count?: number }[];
  active: string;
  onChange: (id: string) => void;
}) {
  return (
    <div className="lyra-tabs lyra-tabs--pills" role="tablist">
      {items.map((item) => (
        <button
          key={item.id}
          type="button"
          role="tab"
          className={cx("lyra-tab", active === item.id && "lyra-tab--active")}
          onClick={() => onChange(item.id)}
        >
          {item.label}
          {item.count != null ? <span className="lyra-tab__count">{item.count}</span> : null}
        </button>
      ))}
    </div>
  );
}

export function Stepper({ steps, current }: { steps: string[]; current: number }) {
  return (
    <div className="lyra-stepper">
      {steps.map((label, i) => {
        const n = i + 1;
        const state = n < current ? "done" : n === current ? "active" : "";
        return (
          <div key={label} className="lyra-step-wrap" style={{ display: "flex", alignItems: "center", gap: 8 }}>
            {i > 0 ? <span className={cx("lyra-step__line", n <= current && "lyra-step__line--done")} /> : null}
            <div className={cx("lyra-step", state && `lyra-step--${state}`)}>
              <span className="lyra-step__dot">{n < current ? "✓" : n}</span>
              <span className="lyra-step__label">{label}</span>
            </div>
          </div>
        );
      })}
    </div>
  );
}

export function Dialog({
  open,
  title,
  onClose,
  children,
  footer,
}: {
  open: boolean;
  title: string;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
}) {
  if (!open) return null;
  return (
    <div className="lyra-dialog-overlay" onClick={onClose}>
      <div className="lyra-dialog" role="dialog" aria-labelledby="dlg-title" onClick={(e) => e.stopPropagation()}>
        <div className="lyra-dialog__header">
          <h2 className="lyra-dialog__title" id="dlg-title">
            {title}
          </h2>
          <IconButton label="Fechar" onClick={onClose}>
            ×
          </IconButton>
        </div>
        <div className="lyra-dialog__body" style={{ padding: "16px 24px 24px" }}>
          {children}
        </div>
        {footer ? <div className="lyra-dialog__footer">{footer}</div> : null}
      </div>
    </div>
  );
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
  if (!open) return null;
  return (
    <div className="lyra-drawer-overlay" onClick={onClose}>
      <div
        className="lyra-drawer lyra-drawer--accounts"
        role="dialog"
        aria-labelledby="drawer-title"
        style={{ maxWidth: width || 480 } as CSSProperties}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="lyra-drawer__header">
          <h2 className="lyra-drawer__title" id="drawer-title">
            {title}
          </h2>
          <IconButton label="Fechar" onClick={onClose}>
            ×
          </IconButton>
        </div>
        <div className="lyra-drawer__body">{children}</div>
        {footer ? <div className="lyra-drawer__footer">{footer}</div> : null}
      </div>
    </div>
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
    <details className="lyra-wssw">
      <summary
        className="lyra-wssw__trigger"
        style={collapsed ? { width: 36, height: 36, padding: 0, justifyContent: "center", minWidth: 36 } : undefined}
      >
        {collapsed ? (
          <strong>{(current?.name || "ws").slice(0, 2).toUpperCase()}</strong>
        ) : (
          <>
            <span className="lyra-wssw__id">
              <span className="lyra-wssw__name">{current?.name || "Nenhum workspace"}</span>
              <span className="lyra-wssw__meta">{current?.root || "escolha ou crie um"}</span>
            </span>
            <span aria-hidden>▾</span>
          </>
        )}
      </summary>
      <div className="lyra-wssw__pop">
        <div className="lyra-wssw__pop-label">Workspaces</div>
        {workspaces.map((w) => (
          <button key={w.id} type="button" className="lyra-wssw__item" onClick={() => onChange(w.id)}>
            <span className="lyra-wssw__id">
              <span className="lyra-wssw__name">{w.name}</span>
              <span className="lyra-wssw__meta">{w.root}</span>
            </span>
          </button>
        ))}
        <hr className="lyra-wssw__sep" />
        <button type="button" className="lyra-wssw__item" onClick={onCreate}>
          <span className="lyra-wssw__plus">+</span>
          <span className="lyra-wssw__create-label">Novo workspace</span>
        </button>
      </div>
    </details>
  );
}

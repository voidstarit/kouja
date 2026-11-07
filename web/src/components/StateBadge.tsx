const labels: Record<string, string> = {
  creating: "creating",
  available: "available",
  exporting: "exporting",
  exported: "exported",
  unexporting: "unexporting",
  unexported: "unexported",
  resizing: "resizing",
  deleting: "deleting",
  failed: "failed",
  drift: "drift",
  active: "active",
  unavailable: "unavailable",
  unknown: "unknown",
  not_registered: "not registered",
  none: "none",
  running: "running",
  needs_attention: "needs attention",
  succeeded: "succeeded",
};

export function StateBadge({ state }: { state: string }) {
  const normalized = state || "unknown";
  return <span class={`badge badge-${normalized}`}>{labels[normalized] ?? normalized}</span>;
}

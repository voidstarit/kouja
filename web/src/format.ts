const byteUnits = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];

export function formatBytes(value: number): string {
  if (!Number.isFinite(value)) {
    return "unknown";
  }
  const sign = value < 0 ? "-" : "";
  let amount = Math.abs(value);
  let unit = 0;
  while (amount >= 1024 && unit < byteUnits.length - 1) {
    amount /= 1024;
    unit++;
  }
  if (unit === 0) {
    return `${sign}${Math.round(amount)} B`;
  }
  return `${sign}${amount.toFixed(2)} ${byteUnits[unit]}`;
}

export function formatTime(value: string): string {
  if (!value) {
    return "unknown";
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "medium",
  }).format(date);
}

export function formatUptime(seconds: number): string {
  if (seconds < 0 || !Number.isFinite(seconds)) {
    return "unknown";
  }
  if (seconds < 60) {
    return `${Math.floor(seconds)}s`;
  }
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) {
    return `${minutes}m ${Math.floor(seconds % 60)}s`;
  }
  const hours = Math.floor(minutes / 60);
  if (hours < 24) {
    return `${hours}h ${minutes % 60}m`;
  }
  return `${Math.floor(hours / 24)}d ${hours % 24}h`;
}

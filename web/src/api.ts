import {
  ApiError,
  type CertificatesResponse,
  type CreateCertificateResponse,
  type ClassListResponse,
  type ConfigResponse,
  type RegisterVGResponse,
  type SaveConfigResponse,
  type SetClassResponse,
  type StatusResponse,
  type VGListResponse,
} from "./types";

async function parseResponse<T>(response: Response): Promise<T> {
  if (response.ok) {
    if (response.status === 204) {
      return undefined as T;
    }
    return (await response.json()) as T;
  }
  let code = "http_error";
  let message = `${response.status} ${response.statusText}`;
  try {
    const body = (await response.json()) as { error?: string; code?: string };
    code = body.code ?? code;
    message = body.error ?? message;
  } catch {
    // Keep the HTTP status text when the body is not JSON.
  }
  throw new ApiError(response.status, code, message);
}

export async function getStatus(signal?: AbortSignal): Promise<StatusResponse> {
  const response = await fetch("/api/status", { signal, headers: { Accept: "application/json" } });
  return parseResponse<StatusResponse>(response);
}

export async function getConfig(signal?: AbortSignal): Promise<ConfigResponse> {
  const response = await fetch("/api/config", { signal, headers: { Accept: "application/json" } });
  return parseResponse<ConfigResponse>(response);
}

export async function saveConfig(yaml: string, etag: string): Promise<SaveConfigResponse> {
  const headers: Record<string, string> = { "Content-Type": "application/yaml" };
  if (etag === "*") {
    headers["If-None-Match"] = "*";
  } else {
    headers["If-Match"] = etag;
  }
  const response = await fetch("/api/config", { method: "PUT", headers, body: yaml });
  return parseResponse<SaveConfigResponse>(response);
}

export async function getVGs(): Promise<VGListResponse> {
  const response = await fetch("/api/vgs", { headers: { Accept: "application/json" } });
  return parseResponse<VGListResponse>(response);
}

export async function registerVG(name: string): Promise<RegisterVGResponse> {
  const response = await fetch("/api/vgs", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name }),
  });
  return parseResponse<RegisterVGResponse>(response);
}

export async function deregisterVG(name: string): Promise<void> {
  const response = await fetch(`/api/vgs/${encodeURIComponent(name)}`, {
    method: "DELETE",
    headers: { "Content-Type": "application/json" },
  });
  await parseResponse<void>(response);
}

export async function getClasses(): Promise<ClassListResponse> {
  const response = await fetch("/api/classes", { headers: { Accept: "application/json" } });
  return parseResponse<ClassListResponse>(response);
}

export async function setClass(name: string, vg: string): Promise<SetClassResponse> {
  const response = await fetch(`/api/classes/${encodeURIComponent(name)}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ vg }),
  });
  return parseResponse<SetClassResponse>(response);
}

export async function removeClass(name: string): Promise<void> {
  const response = await fetch(`/api/classes/${encodeURIComponent(name)}`, {
    method: "DELETE",
    headers: { "Content-Type": "application/json" },
  });
  await parseResponse<void>(response);
}

export async function disableISCSITarget(owner: string, volumeID: string): Promise<void> {
  const response = await fetch("/api/targets/disable", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ owner, volume_id: volumeID }),
  });
  await parseResponse<void>(response);
}

export async function enableISCSITarget(owner: string, volumeID: string): Promise<void> {
  const response = await fetch("/api/targets/enable", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ owner, volume_id: volumeID }),
  });
  await parseResponse<void>(response);
}

export async function getCertificates(): Promise<CertificatesResponse> {
  const response = await fetch("/api/certificates", { headers: { Accept: "application/json" } });
  return parseResponse<CertificatesResponse>(response);
}

export async function createCertificate(role: "admin" | "caller", name: string): Promise<CreateCertificateResponse> {
  const response = await fetch("/api/certificates", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ role, name }),
  });
  return parseResponse<CreateCertificateResponse>(response);
}

export function certificateDownloadURL(name: string): string {
  return `/api/certificates/${encodeURIComponent(name)}`;
}

export async function revokeCertificate(serial: string): Promise<void> {
  const response = await fetch(`/api/certificates/${encodeURIComponent(serial)}`, {
    method: "DELETE",
    headers: { "Content-Type": "application/json" },
  });
  await parseResponse<void>(response);
}

export async function revokeExternalCertificate(certificatePEM: string, reason: string): Promise<void> {
  const response = await fetch("/api/certificates:revoke-external", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ certificate_pem: certificatePEM, reason }),
  });
  await parseResponse<void>(response);
}

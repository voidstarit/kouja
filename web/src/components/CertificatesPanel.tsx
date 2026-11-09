import { useEffect, useState } from "preact/hooks";
import {
  certificateDownloadURL,
  createCertificate,
  getCertificates,
  revokeCertificate,
  revokeExternalCertificate,
} from "../api";
import { formatTime } from "../format";
import type { CertificatesResponse } from "../types";

export function CertificatesPanel() {
  const [data, setData] = useState<CertificatesResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState("");
  const [createError, setCreateError] = useState("");
  const [notice, setNotice] = useState("");
  const [role, setRole] = useState<"admin" | "caller">("caller");
  const [name, setName] = useState("");
  const [externalPEM, setExternalPEM] = useState("");
  const [externalReason, setExternalReason] = useState("external");

  async function refresh() {
    setLoading(true);
    setError("");
    try {
      setData(await getCertificates());
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void refresh();
  }, []);

  async function issue(event: Event) {
    event.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) {
      return;
    }
    setCreating(true);
    setCreateError("");
    setNotice("");
    try {
      const created = await createCertificate(role, trimmed);
      setName("");
      setNotice(`Created ${created.certificate.name || created.serial_number}. Download the certificate and private key below.`);
      await refresh();
    } catch (caught) {
      setCreateError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setCreating(false);
    }
  }

  async function revoke(serial: string) {
    if (!serial || !window.confirm(`Revoke certificate ${serial}? This cannot be undone.`)) {
      return;
    }
    setError("");
    try {
      await revokeCertificate(serial);
      setNotice(`Revoked ${serial}.`);
      await refresh();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    }
  }

  async function revokeExternal(event: Event) {
    event.preventDefault();
    if (!externalPEM.trim()) {
      return;
    }
    setError("");
    try {
      await revokeExternalCertificate(externalPEM, externalReason);
      setExternalPEM("");
      setNotice("External certificate revoked.");
      await refresh();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    }
  }

  const certificates = data?.certificates ?? data?.files ?? [];
  const canIssue = data?.issuance_enabled !== false;

  return (
    <section class="card">
      <div class="section-heading">
        <h2>Client and admin certificates</h2>
        <button onClick={() => void refresh()} disabled={loading || creating}>
          {loading ? "Refreshing…" : "Refresh"}
        </button>
      </div>
      <p class="muted">
        Registry rows are shown below. Certificate and private-key downloads are served only for
        managed files; the CA private key and server private key are never exposed here.
      </p>
      {!canIssue && (
        <p class="notice">Certificate issuance is disabled. Existing certificates can still be listed and revoked.</p>
      )}
      <form class="form-grid" onSubmit={issue}>
        <label class="field">
          <span>Certificate type</span>
          <select value={role} disabled={creating || !canIssue} onChange={(event) => setRole((event.target as HTMLSelectElement).value as "admin" | "caller")}>
            <option value="caller">Client (caller)</option>
            <option value="admin">Admin</option>
          </select>
        </label>
        <label class="field">
          <span>Name</span>
          <input
            value={name}
            placeholder="e.g. rookery-prod"
            maxLength={64}
            pattern="[A-Za-z0-9][A-Za-z0-9._-]*"
            disabled={creating || !canIssue}
            onInput={(event) => setName((event.target as HTMLInputElement).value)}
          />
        </label>
        <div class="field field-wide">
          <button type="submit" disabled={creating || !canIssue || !name.trim()}>
            {creating ? "Creating…" : "Create certificate"}
          </button>
          <span class="field-hint">
            The agent writes the keypair and records it in the registry. Download both files and
            keep the private key protected.
          </span>
        </div>
      </form>
      {createError && <p class="error">{createError}</p>}
      {notice && <p class="success">{notice}</p>}
      {data?.ca ? (
        <div class="form-row">
          <a class="button-link" href={data.ca.download_url} download={data.ca.name}>
            Download client CA
          </a>
          <span class="mono muted">{data.ca.name}</span>
        </div>
      ) : (
        <p class="muted">The client CA certificate is not configured or not readable.</p>
      )}
      {error && <p class="error">{error}</p>}
      {data?.error && <p class="error">{data.error}</p>}
      <div class="table-scroll">
        <table>
          <thead>
            <tr>
              <th>Serial</th>
              <th>Role</th>
              <th>Status</th>
              <th>Expires</th>
              <th>Downloads</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {certificates.length === 0 && (
              <tr>
                <td colSpan={6} class="empty">No issued certificates found.</td>
              </tr>
            )}
            {certificates.map((certificate) => (
              <tr key={certificate.serial_number || certificate.name}>
                <td class="mono">{certificate.serial_number || certificate.name}</td>
                <td>{certificate.role || "unknown"}</td>
                <td>{certificate.revoked ? "revoked" : "active"}</td>
                <td>{certificate.not_after ? formatTime(certificate.not_after) : "unknown"}</td>
                <td>
                  {certificate.name || certificate.download_url ? (
                    <a class="button-link" href={certificate.download_url || certificateDownloadURL(certificate.name)} download={certificate.name}>
                      Certificate
                    </a>
                  ) : null}
                  {certificate.key_download_url ? (
                    <a class="button-link" href={certificate.key_download_url}>Private key</a>
                  ) : null}
                </td>
                <td>
                  <button
                    disabled={certificate.revoked || !certificate.serial_number}
                    onClick={() => void revoke(certificate.serial_number || "")}
                  >
                    Revoke
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <form class="form-grid" onSubmit={revokeExternal}>
        <label class="field field-wide">
          <span>Revoke external certificate</span>
          <textarea
            value={externalPEM}
            placeholder="Paste the PEM certificate"
            disabled={!canIssue}
            onInput={(event) => setExternalPEM((event.target as HTMLTextAreaElement).value)}
          />
        </label>
        <label class="field">
          <span>Reason</span>
          <input value={externalReason} disabled={!canIssue} onInput={(event) => setExternalReason((event.target as HTMLInputElement).value)} />
        </label>
        <div class="field field-wide">
          <button type="submit" disabled={!canIssue || !externalPEM.trim()}>Revoke external</button>
        </div>
      </form>
    </section>
  );
}

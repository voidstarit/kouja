import { useEffect, useState } from "preact/hooks";
import { certificateDownloadURL, createCertificate, getCertificates } from "../api";
import { formatBytes, formatTime } from "../format";
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
      setNotice(`Created ${created.certificate.name}. Download the certificate and private key below.`);
      await refresh();
    } catch (caught) {
      setCreateError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setCreating(false);
    }
  }

  const files = data?.files ?? [];
  const hasPrivateKey = files.some((file) => file.kind === "private_key");

  return (
    <section class="card">
      <div class="section-heading">
        <h2>Client and admin certificates</h2>
        <button onClick={() => void refresh()} disabled={loading || creating}>
          {loading ? "Refreshing…" : "Refresh"}
        </button>
      </div>
      <p class="muted">
        Files are read from <code>{data?.directory || "the configured client certificate directory"}</code>.
        Only regular <code>.crt</code>, <code>.key</code>, and <code>.pem</code> files in that
        directory are served. The CA private key and server private key are never exposed here.
      </p>
      <form class="form-grid" onSubmit={issue}>
        <label class="field">
          <span>Certificate type</span>
          <select value={role} disabled={creating} onChange={(event) => setRole((event.target as HTMLSelectElement).value as "admin" | "caller")}>
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
            disabled={creating}
            onInput={(event) => setName((event.target as HTMLInputElement).value)}
          />
        </label>
        <div class="field field-wide">
          <button type="submit" disabled={creating || !name.trim()}>
            {creating ? "Creating…" : "Create certificate"}
          </button>
          <span class="field-hint">
            A new CA-signed client keypair is written to the certificate directory. Download both
            files and keep the private key protected.
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
      {hasPrivateKey && (
        <p class="notice">
          This list includes private keys. Downloading them gives the holder the same client
          identity as the certificate. Keep the files protected.
        </p>
      )}
      {error && <p class="error">{error}</p>}
      {data?.error && <p class="error">{data.error}</p>}
      <div class="table-scroll">
        <table>
          <thead>
            <tr>
              <th>File</th>
              <th>Kind</th>
              <th>Role</th>
              <th>Size</th>
              <th>Modified</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {files.length === 0 && (
              <tr>
                <td colSpan={6} class="empty">
                  No client certificate files found.
                </td>
              </tr>
            )}
            {files.map((file) => (
              <tr key={file.name}>
                <td class="mono">{file.name}</td>
                <td>{file.kind === "private_key" ? "private key" : "certificate"}</td>
                <td>{file.role || "unknown"}</td>
                <td>{formatBytes(file.size_bytes)}</td>
                <td>{formatTime(file.modified_at)}</td>
                <td>
                  <a class="button-link" href={certificateDownloadURL(file.name)} download={file.name}>
                    Download
                  </a>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

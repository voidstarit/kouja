import { useEffect, useState } from "preact/hooks";
import { certificateDownloadURL, getCertificates } from "../api";
import { formatBytes, formatTime } from "../format";
import type { CertificatesResponse } from "../types";

export function CertificatesPanel() {
  const [data, setData] = useState<CertificatesResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

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

  const files = data?.files ?? [];
  const hasPrivateKey = files.some((file) => file.kind === "private_key");

  return (
    <section class="card">
      <div class="section-heading">
        <h2>Client certificates</h2>
        <button onClick={() => void refresh()} disabled={loading}>
          {loading ? "Refreshing…" : "Refresh"}
        </button>
      </div>
      <p class="muted">
        Files are read from <code>{data?.directory || "the configured client certificate directory"}</code>.
        Only regular <code>.crt</code>, <code>.key</code>, and <code>.pem</code> files in that
        directory are served. The CA private key and server private key are never exposed here.
      </p>
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

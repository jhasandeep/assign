"use client";

import { useCallback, useEffect, useState } from "react";

type Voucher = {
  id: number;
  type: string;
  source_id: number;
  amount_paise: number;
  status: "pending" | "pushed" | "failed";
  attempts: number;
  last_error: string | null;
  client_ref: string;
  external_ref: string | null;
  updated_at: string;
};

const STATUSES: Voucher["status"][] = ["pending", "failed", "pushed"];
const POLL_MS = 2000;

// Money is integer paise end to end; only format it for display here.
const rupees = (paise: number) =>
  (paise / 100).toLocaleString("en-IN", { style: "currency", currency: "INR" });

export default function SyncPage() {
  const [vouchers, setVouchers] = useState<Voucher[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [retrying, setRetrying] = useState<Set<number>>(new Set());

  const load = useCallback(async () => {
    try {
      const res = await fetch("/backend/vouchers", { cache: "no-store" });
      if (!res.ok) throw new Error(`API returned ${res.status}`);
      setVouchers(await res.json());
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not load vouchers");
    }
  }, []);

  useEffect(() => {
    load();
    const t = setInterval(load, POLL_MS);
    return () => clearInterval(t);
  }, [load]);

  async function retry(id: number) {
    setRetrying((s) => new Set(s).add(id));
    try {
      const res = await fetch(`/backend/vouchers/${id}/retry`, { method: "POST" });
      if (!res.ok) {
        const body = await res.json().catch(() => ({}));
        setError(body.error ?? `retry failed (${res.status})`);
      }
    } finally {
      setRetrying((s) => {
        const next = new Set(s);
        next.delete(id);
        return next;
      });
      load();
    }
  }

  const cell: React.CSSProperties = { textAlign: "left", padding: "6px 10px", borderBottom: "1px solid #ddd", verticalAlign: "top" };

  return (
    <main>
      <h1 style={{ marginTop: 0 }}>Voucher sync</h1>
      {error && <p style={{ color: "#b00020" }}>Error: {error}</p>}

      {STATUSES.map((status) => {
        const rows = vouchers.filter((v) => v.status === status);
        return (
          <section key={status} style={{ marginBottom: 28 }}>
            <h2 style={{ textTransform: "capitalize" }}>
              {status} ({rows.length})
            </h2>
            {rows.length === 0 ? (
              <p style={{ color: "#666" }}>None.</p>
            ) : (
              <table style={{ borderCollapse: "collapse", width: "100%", background: "#fff" }}>
                <thead>
                  <tr>
                    {["Voucher", "Receipt", "Amount", "Attempts", "External ref", "Failure reason", ""].map((h) => (
                      <th key={h} style={cell}>{h}</th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {rows.map((v) => (
                    <tr key={v.id}>
                      <td style={cell}>#{v.id}</td>
                      <td style={cell}>#{v.source_id}</td>
                      <td style={cell}>{rupees(v.amount_paise)}</td>
                      <td style={cell}>{v.attempts}</td>
                      <td style={cell}>{v.external_ref ?? "-"}</td>
                      <td style={{ ...cell, color: "#b00020" }}>{v.last_error ?? "-"}</td>
                      <td style={cell}>
                        {v.status === "failed" && (
                          <button onClick={() => retry(v.id)} disabled={retrying.has(v.id)}>
                            {retrying.has(v.id) ? "Retrying..." : "Retry"}
                          </button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </section>
        );
      })}
    </main>
  );
}

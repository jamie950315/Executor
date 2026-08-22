import { useEffect, useRef, useState } from "react";
import type { PanelProps } from "./types";

const uploadChunkBytes = 4 * 1024 * 1024;
const maximumFileBytes = 64 * 1024 * 1024;
interface FileEntry { name: string; path: string; isDir: boolean }

export function FilesPanel({ call }: PanelProps) {
  const [path, setPath] = useState("/");
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [filePath, setFilePath] = useState<string | null>(null);
  const [content, setContent] = useState("");
  const [status, setStatus] = useState("Directory not loaded");
  const [upload, setUpload] = useState<File | null>(null);
  const [newDirectory, setNewDirectory] = useState("");
  const [moveDestination, setMoveDestination] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);

  const list = async (nextPath = path) => {
    setStatus("Reading host directory…");
    try {
      const response = await call("filesystem_read", { action: "read_directory", path: nextPath, privilege: "owner" }, new AbortController().signal);
      setEntries(parseEntries(response.result)); setPath(nextPath); setFilePath(null); setContent(""); setStatus("Directory loaded");
    } catch { setStatus("Directory unavailable"); }
  };
  useEffect(() => { void list("/"); }, []); // one initial unrestricted root probe

  const open = async (entry: FileEntry) => {
    if (entry.isDir) { await list(entry.path); return; }
    setStatus("Reading UTF-8 file…");
    try {
      const response = await call("filesystem_read", { action: "read_file", path: entry.path, privilege: "owner", encoding: "utf8" }, new AbortController().signal);
      const record = asRecord(response.result); if (typeof record?.content !== "string") throw new Error();
      setFilePath(entry.path); setContent(record.content); setStatus(`${record.size ?? record.content.length} bytes · UTF-8`);
    } catch { setStatus("Text preview unavailable. Use Download for binary data."); setFilePath(entry.path); setContent(""); }
  };
  const save = async () => {
    if (!filePath) return;
    try { const response = await call("filesystem_write", { action: "write_file", path: filePath, privilege: "owner", encoding: "utf8", content }, new AbortController().signal); const record = asRecord(response.result); setStatus(`${record?.size ?? content.length} bytes saved`); }
    catch { setStatus("Save failed"); }
  };
  const uploadFile = async () => {
    if (!upload) return;
    if (upload.size > maximumFileBytes) { setStatus("Upload exceeds the 64 MiB relay limit"); return; }
    const bytes = new Uint8Array(await upload.arrayBuffer());
    try {
      const destination = joinPath(path, upload.name);
      for (let offset = 0; offset < bytes.length || (bytes.length === 0 && offset === 0); offset += uploadChunkBytes) {
        const part = bytes.slice(offset, Math.min(offset + uploadChunkBytes, bytes.length));
        const encoded = standardBase64(part);
        part.fill(0);
        await call("filesystem_write", { action: offset === 0 ? "write_file" : "append_file", path: destination, privilege: "owner", encoding: "base64", content: encoded }, new AbortController().signal);
        if (bytes.length === 0) break;
      }
      setStatus(`${upload.size} bytes uploaded`); setUpload(null); if (inputRef.current) inputRef.current.value = ""; await list(path);
    } catch { setStatus("Upload failed"); }
    finally { bytes.fill(0); }
  };
  const download = async () => {
    if (!filePath) return;
    try {
      const response = await call("filesystem_read", { action: "read_file", path: filePath, privilege: "owner", encoding: "base64" }, new AbortController().signal);
      const record = asRecord(response.result); if (typeof record?.content !== "string") throw new Error();
      const bytes = decodeStandardBase64(record.content); const blob = new Blob([Uint8Array.from(bytes).buffer]); bytes.fill(0);
      const url = URL.createObjectURL(blob); const link = document.createElement("a"); link.href = url; link.download = basename(filePath); link.click(); URL.revokeObjectURL(url); setStatus(`${record.size ?? blob.size} bytes downloaded`);
    } catch { setStatus("Download failed"); }
  };
  const mkdir = async () => { if (!newDirectory) return; try { await call("filesystem_write", { action: "mkdir", path: joinPath(path, newDirectory), privilege: "owner" }, new AbortController().signal); setNewDirectory(""); await list(path); } catch { setStatus("Directory creation failed"); } };
  const move = async () => { if (!filePath || !moveDestination) return; try { await call("filesystem_write", { action: "move", path: filePath, destination: moveDestination, privilege: "owner" }, new AbortController().signal); setMoveDestination(""); await list(path); } catch { setStatus("Move failed"); } };
  const remove = async () => { if (!filePath || !window.confirm(`Delete ${filePath}?`)) return; try { await call("filesystem_write", { action: "delete", path: filePath, privilege: "owner" }, new AbortController().signal); await list(path); } catch { setStatus("Delete failed"); } };

  return (
    <section className="panel-shell files-panel" aria-labelledby="files-title"><header className="panel-heading"><div><p className="eyebrow">Unrestricted host filesystem</p><h2 id="files-title">Files</h2></div><span className="limit-badge">64 MiB relay ceiling</span></header>
      <form className="path-bar" onSubmit={(event) => { event.preventDefault(); void list(path); }}><label>Host path<input value={path} onChange={(event) => setPath(event.target.value)} /></label><button type="submit">Open</button></form>
      <nav className="breadcrumbs" aria-label="Path breadcrumbs">{breadcrumbs(path).map((crumb) => <button key={crumb.path} onClick={() => void list(crumb.path)}>{crumb.label}</button>)}</nav>
      <p className="status-line" aria-live="polite">{status}</p>
      <div className="files-layout"><div className="file-list" role="list" aria-label="Directory entries">{entries.map((entry) => <button role="listitem" key={entry.path} onClick={() => void open(entry)}><span aria-hidden="true">{entry.isDir ? "DIR" : "FILE"}</span><strong>{entry.name}</strong></button>)}</div>
        <div className="file-editor"><label>Operation path<input value={filePath ?? ""} onChange={(event) => setFilePath(event.target.value || null)} placeholder="Select or enter any host path" /></label><textarea aria-label="File contents" value={content} disabled={!filePath} onChange={(event) => setContent(event.target.value)} spellCheck={false} /><div className="button-row"><button disabled={!filePath} onClick={() => { if (filePath) void open({ name: basename(filePath), path: filePath, isDir: false }); }}>Read UTF-8</button><button disabled={!filePath} onClick={() => void save()}>Save UTF-8</button><button disabled={!filePath} onClick={() => void download()}>Download binary</button><button className="danger-ghost" disabled={!filePath} onClick={() => void remove()}>Delete</button></div>
          <label>Move destination<input value={moveDestination} onChange={(event) => setMoveDestination(event.target.value)} /></label><button disabled={!filePath || !moveDestination} onClick={() => void move()}>Move</button></div></div>
      <div className="file-operations"><div><label>New directory<input value={newDirectory} onChange={(event) => setNewDirectory(event.target.value)} /></label><button onClick={() => void mkdir()}>Create directory</button></div><div><label>Choose file to upload<input ref={inputRef} type="file" onChange={(event) => setUpload(event.target.files?.[0] ?? null)} /></label><button disabled={!upload} onClick={() => void uploadFile()}>Upload</button></div></div>
    </section>
  );
}

function parseEntries(value: unknown): FileEntry[] { if (!Array.isArray(value)) return []; return value.flatMap((item) => { const record = asRecord(item); const name = record?.Name ?? record?.name; const path = record?.Path ?? record?.path; const isDir = record?.IsDir ?? record?.isDir ?? record?.is_dir; return typeof name === "string" && typeof path === "string" && typeof isDir === "boolean" ? [{ name, path, isDir }] : []; }); }
function asRecord(value: unknown): Record<string, unknown> | null { return typeof value === "object" && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : null; }
function joinPath(parent: string, child: string): string { const separator = parent.includes("\\") && !parent.includes("/") ? "\\" : "/"; return `${parent.replace(/[\\/]$/u, "")}${separator}${child}` || `${separator}${child}`; }
function basename(path: string): string { return path.split(/[\\/]/u).filter(Boolean).at(-1) ?? "download.bin"; }
function breadcrumbs(path: string): Array<{ label: string; path: string }> { if (/^[A-Za-z]:\\/u.test(path)) { const parts = path.split("\\").filter(Boolean); return parts.map((label, index) => ({ label, path: parts.slice(0, index + 1).join("\\") + (index === 0 ? "\\" : "") })); } const parts = path.split("/").filter(Boolean); return [{ label: "/", path: "/" }, ...parts.map((label, index) => ({ label, path: `/${parts.slice(0, index + 1).join("/")}` }))]; }
function standardBase64(bytes: Uint8Array): string { let binary = ""; const block = 32_768; for (let offset = 0; offset < bytes.length; offset += block) binary += String.fromCharCode(...bytes.subarray(offset, offset + block)); return btoa(binary); }
function decodeStandardBase64(value: string): Uint8Array { if (value.length % 4 !== 0 || !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/u.test(value)) throw new Error(); const binary = atob(value); if (btoa(binary) !== value) throw new Error(); return Uint8Array.from(binary, (character) => character.charCodeAt(0)); }

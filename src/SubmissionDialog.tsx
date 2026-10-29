import { useCallback, useEffect, useRef, useState } from "react";
import type { Submission } from "../shared/contracts";

const storageKey = "repository-submission";
const terminal = (result: Submission) =>
  ["completed", "partial", "failed"].includes(result.status);
const resultLabels: Record<string, string> = {
  pending: "等待处理",
  tracked: "已收录",
  ineligible: "不符合收录资格",
  unconfirmed: "资格待确认，可重新投稿重试",
  failed: "收录失败",
  skipped: "未处理",
};
const statusLabels: Record<Submission["status"], string> = {
  queued: "已接收，等待处理",
  processing: "正在处理",
  retrying: "等待重试",
  completed: "处理完成",
  partial: "部分完成",
  failed: "处理失败",
};

function savedSubmission() {
  try {
    const id = localStorage.getItem(storageKey) || "";
    return /^[a-f0-9]{32}$/.test(id) ? id : "";
  } catch {
    return "";
  }
}

export function SubmissionDialog({
  apiBase,
  onClose,
  onChanged,
}: {
  apiBase: string;
  onClose: () => void;
  onChanged: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [url, setURL] = useState("");
  const [id, setID] = useState(savedSubmission);
  const [result, setResult] = useState<Submission | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const postController = useRef<AbortController | null>(null);
  const notified = useRef("");

  useEffect(() => {
    dialog.current?.showModal();
    dialog.current?.querySelector("input")?.focus();
    return () => postController.current?.abort();
  }, []);

  const accept = useCallback((next: Submission) => {
    setResult(next);
    setError("");
  }, []);

  useEffect(() => {
    if (!result) return;
    const signature = `${result.id}:${result.github}:${result.linuxdo}`;
    if (
      signature !== notified.current &&
      (result.github === "tracked" || result.linuxdo === "tracked")
    ) {
      notified.current = signature;
      onChanged();
    }
  }, [result, onChanged]);

  useEffect(() => {
    if (!id) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const poll = async () => {
      let delay = 30_000;
      try {
        const response = await fetch(`${apiBase}/api/submissions/${id}`, {
          signal: controller.signal,
          cache: "no-store",
        });
        const value = await response.json();
        if (!response.ok) {
          if (response.status === 404) {
            setID("");
            setResult(null);
            try { localStorage.removeItem(storageKey); } catch { /* Storage is optional. */ }
          }
          throw new Error(value.error || "暂时无法查询投稿状态");
        }
        if (controller.signal.aborted) return;
        const next = value as Submission;
        accept(next);
        if (terminal(next)) return;
        delay = next.status === "retrying" ? 30_000 : 5_000;
      } catch (problem) {
        if (controller.signal.aborted) return;
        setError(problem instanceof Error ? problem.message : "暂时无法查询投稿状态");
      }
      if (!controller.signal.aborted) timer = setTimeout(() => void poll(), delay);
    };
    void poll();
    return () => { controller.abort(); clearTimeout(timer); };
  }, [id, apiBase, accept]);

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy) return;
    postController.current?.abort();
    const controller = new AbortController();
    postController.current = controller;
    setBusy(true);
    setError("");
    try {
      const response = await fetch(`${apiBase}/api/submissions`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ url }),
        signal: controller.signal,
        cache: "no-store",
      });
      const value = await response.json();
      if (!response.ok) throw new Error(value.error || "投稿失败，请稍后重试");
      if (controller.signal.aborted) return;
      const next = value as Submission;
      accept(next);
      setID(next.id);
      try { localStorage.setItem(storageKey, next.id); } catch { /* Storage is optional. */ }
    } catch (problem) {
      if (!controller.signal.aborted)
        setError(problem instanceof Error ? problem.message : "投稿失败，请稍后重试");
    } finally {
      if (!controller.signal.aborted) setBusy(false);
    }
  }

  return (
    <dialog className="submission-dialog" ref={dialog} aria-labelledby="submission-title" onCancel={onClose}>
      <div className="submission-heading">
        <h2 id="submission-title">投稿开源项目</h2>
        <button type="button" className="icon-button" onClick={onClose} aria-label="关闭投稿">×</button>
      </div>
      <p>提交公开的 GitHub 仓库地址，收录后持续观察 Star 变化。</p>
      <form onSubmit={(event) => void submit(event)}>
        <label htmlFor="submission-url">GitHub 仓库地址</label>
        <input id="submission-url" type="url" required maxLength={2048} autoFocus placeholder="https://github.com/owner/repo" value={url} onChange={(event) => setURL(event.target.value)} />
        <button className="submission-button" type="submit" disabled={busy}>{busy ? "正在提交…" : "提交仓库"}</button>
      </form>
      {error && <p className="submission-error" role="alert">{error}</p>}
      {result && (
        <section className="submission-result" aria-live="polite">
          <h3>{statusLabels[result.status]}</h3>
          <a href={result.url} target="_blank" rel="noreferrer">{result.name}</a>
          <dl>
            <div><dt>GitHub 趋势</dt><dd>{resultLabels[result.github]}</dd></div>
            <div><dt>Linux.do 开源项目</dt><dd>{resultLabels[result.linuxdo]}</dd></div>
          </dl>
          {result.message && <p>{result.message}</p>}
          {result.nextAttemptAt && <p>下次尝试：{new Date(result.nextAttemptAt).toLocaleString("zh-CN", { hour12: false })}</p>}
        </section>
      )}
    </dialog>
  );
}

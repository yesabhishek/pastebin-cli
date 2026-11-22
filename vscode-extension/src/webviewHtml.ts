import { randomBytes } from "node:crypto";

export interface HtmlParts {
  cspSource: string;
  styles: string[];
  scripts: string[];
  bodyClass: string;
  body: string;
  title: string;
}

export function makeNonce(): string {
  return randomBytes(18).toString("base64").replace(/[^A-Za-z0-9]/g, "");
}

/**
 * Builds a webview document with a strict Content-Security-Policy: no remote
 * content, no inline script, scripts only with a per-render nonce. All dynamic
 * data is delivered later via postMessage and rendered with textContent.
 */
export function buildHtml(parts: HtmlParts): string {
  const nonce = makeNonce();
  const csp = [
    "default-src 'none'",
    `style-src ${parts.cspSource}`,
    `img-src ${parts.cspSource} data:`,
    `font-src ${parts.cspSource}`,
    `script-src 'nonce-${nonce}'`,
    "connect-src 'none'",
    "form-action 'none'",
    "base-uri 'none'",
    "frame-src 'none'"
  ].join("; ");
  const styles = parts.styles.map((href) => `<link rel="stylesheet" href="${escapeAttr(href)}">`).join("\n");
  const scripts = parts.scripts.map((src) => `<script nonce="${nonce}" src="${escapeAttr(src)}"></script>`).join("\n");
  return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy" content="${csp}">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>${escapeAttr(parts.title)}</title>
${styles}
</head>
<body class="${escapeAttr(parts.bodyClass)}">
${parts.body}
${scripts}
</body>
</html>`;
}

export function escapeAttr(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c] as string));
}

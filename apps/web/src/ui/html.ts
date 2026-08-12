export function escapeHtml(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

/**
 * Escapes an untrusted value for a quoted HTML attribute. Kept distinct from
 * text escaping so dynamic attribute sinks are explicit and auditable.
 */
export function escapeHtmlAttribute(value: string): string {
  return escapeHtml(value)
    .replaceAll("\r", "&#13;")
    .replaceAll("\n", "&#10;")
    .replaceAll("\t", "&#9;");
}

/** Escapes untrusted text embedded inside a Markdown link/image label. */
export function escapeMarkdownLabel(value: string): string {
  return value
    .replaceAll("\\", "\\\\")
    .replaceAll("[", "\\[")
    .replaceAll("]", "\\]")
    .replace(/[\r\n]+/g, " ");
}

/** Prevents a validated URL from terminating a Markdown link destination. */
export function escapeMarkdownDestination(value: string): string {
  return value
    .replaceAll("(", "%28")
    .replaceAll(")", "%29")
    .replaceAll("<", "%3C")
    .replaceAll(">", "%3E")
    .replaceAll(" ", "%20")
    .replaceAll("\r", "%0D")
    .replaceAll("\n", "%0A");
}

export function icon(name: "arrow" | "check" | "copy" | "sync" | "trash"): string {
  const icons = {
    arrow: "→",
    check: "✓",
    copy: "⧉",
    sync: "↻",
    trash: "×",
  };
  return `<span aria-hidden="true">${icons[name]}</span>`;
}

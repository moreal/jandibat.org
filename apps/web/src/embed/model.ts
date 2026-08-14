import {
  escapeHtml,
  escapeMarkdownDestination,
  escapeMarkdownLabel,
} from "../ui/html.ts";

export type EmbedFormat = "markdown" | "html" | "url";

export type EmbedOptions = {
  subject: string;
  title: string;
  theme: string;
  weekStart: string;
  cellSize: number;
  showLegend: boolean;
  apiBaseUrl: string;
  appBaseUrl: string;
};

export type EmbedValues = {
  url: string;
  markdown: string;
  html: string;
};

function withoutTrailingSlash(value: string): string {
  return value.replace(/\/$/, "");
}

export function buildEmbedValues(options: EmbedOptions): EmbedValues {
  const subject = options.subject.trim();
  const title = options.title.trim() || `${subject}'s activity`;
  const cellSize = Math.max(6, Math.min(32, options.cellSize));
  const params = new URLSearchParams({
    theme: options.theme,
    weekStart: options.weekStart,
    cellSize: String(cellSize),
    showLegend: String(options.showLegend),
  });
  const url = `${withoutTrailingSlash(options.apiBaseUrl)}/v1/render/${encodeURIComponent(subject)}.svg?${params.toString()}`;
  const profileUrl = `${withoutTrailingSlash(options.appBaseUrl)}/explore/${encodeURIComponent(subject)}`;

  return {
    url,
    markdown: `[![${escapeMarkdownLabel(title)}](${escapeMarkdownDestination(url)})](${escapeMarkdownDestination(profileUrl)})`,
    html: `<a href="${escapeHtml(profileUrl)}"><img src="${escapeHtml(url)}" alt="${escapeHtml(title)}"></a>`,
  };
}

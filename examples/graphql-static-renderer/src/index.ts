import { access, readFile, writeFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";

export interface Options {
  endpoint: string;
  subject: string;
  from: string;
  to: string;
  timezone: string;
  environmentIDs: string[];
  output: string;
}

interface Snapshot {
  subject: { handle: string; displayName: string | null };
  range: { from: string; to: string };
  days: Array<{ date: string; count: string; level: number }>;
  total: string;
  longestStreak: number;
  generatedAt: string;
  dataUpdatedAt: string | null;
  revision: string;
}

interface Metadata {
  generatedAt: string;
  dataUpdatedAt: string | null;
  revision: string;
  renderedSubjectLabel: string;
}

export const query = `query StaticActivitySnapshot($subject: String!, $range: DateRangeInput!, $timezone: TimeZone!, $environmentIDs: [String!]) {
  subject(handleOrID: $subject) {
    activitySnapshot(range: $range, timezone: $timezone, environmentIDs: $environmentIDs) {
      subject { handle displayName }
      range { from to }
      days { date count level }
      total
      longestStreak
      generatedAt dataUpdatedAt revision
    }
  }
}`;

function escapeHtml(value: string): string {
  return value.replace(/[&<>"']/g, (character) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  })[character]!);
}

function parseSnapshot(payload: unknown): Snapshot {
  if (typeof payload !== "object" || payload === null) throw new Error("GraphQL response has no snapshot");
  const response = payload as { errors?: unknown; data?: { subject?: { activitySnapshot?: Snapshot } | null } };
  if (Array.isArray(response.errors) && response.errors.length > 0) throw new Error("GraphQL request failed");
  const snapshot = response.data?.subject?.activitySnapshot;
  if (!snapshot || typeof snapshot.generatedAt !== "string" ||
      (snapshot.dataUpdatedAt !== null && typeof snapshot.dataUpdatedAt !== "string") ||
      typeof snapshot.revision !== "string" || !Array.isArray(snapshot.days) ||
      typeof snapshot.subject?.handle !== "string" ||
      (snapshot.subject.displayName !== null && typeof snapshot.subject.displayName !== "string") ||
      typeof snapshot.range?.from !== "string" || typeof snapshot.range?.to !== "string" ||
      typeof snapshot.total !== "string" || !Number.isSafeInteger(snapshot.longestStreak) ||
      !snapshot.days.every((day) => day && typeof day.date === "string" &&
        typeof day.count === "string" && Number.isSafeInteger(day.level))) {
    throw new Error("GraphQL response has no valid snapshot");
  }
  return snapshot;
}

function subjectLabel(snapshot: Snapshot): string {
  return snapshot.subject.displayName || snapshot.subject.handle;
}

function htmlFor(snapshot: Snapshot): string {
  const title = escapeHtml(subjectLabel(snapshot));
  const rows = snapshot.days.map((day) =>
    `      <tr><th scope="row">${escapeHtml(day.date)}</th><td>${escapeHtml(day.count)}</td><td>${day.level}</td></tr>`,
  ).join("\n");
  const updated = snapshot.dataUpdatedAt === null ? "" :
    `    <p>Data updated at: <time>${escapeHtml(snapshot.dataUpdatedAt)}</time></p>\n`;
  return `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>${title} activity</title></head>
<body>
  <main>
    <h1>${title} activity</h1>
    <p>Range: ${escapeHtml(snapshot.range.from)} – ${escapeHtml(snapshot.range.to)}</p>
    <p>Total: ${escapeHtml(snapshot.total)} · Longest streak: ${snapshot.longestStreak}</p>
    <table><thead><tr><th>Date</th><th>Count</th><th>Level</th></tr></thead><tbody>
${rows}
    </tbody></table>
    <p>Generated at: <time>${escapeHtml(snapshot.generatedAt)}</time></p>
${updated}  </main>
</body>
</html>
`;
}

async function readPriorMetadata(path: string): Promise<Partial<Metadata> | undefined> {
  try {
    const metadata = JSON.parse(await readFile(path, "utf8")) as Partial<Metadata>;
    return metadata;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return undefined;
    throw error;
  }
}

async function fileExists(path: string): Promise<boolean> {
  try {
    await access(path);
    return true;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return false;
    throw error;
  }
}

export async function renderToFiles(options: Options, fetcher: typeof fetch = fetch): Promise<Metadata & { status: "written" | "unchanged revision" }> {
  const response = await fetcher(options.endpoint, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      query,
      operationName: "StaticActivitySnapshot",
      variables: {
        subject: options.subject,
        range: { from: options.from, to: options.to },
        timezone: options.timezone,
        environmentIDs: options.environmentIDs,
      },
    }),
  });
  if (!response.ok) throw new Error(`GraphQL HTTP request failed (${response.status})`);
  const snapshot = parseSnapshot(await response.json());
  const metadata: Metadata = {
    generatedAt: snapshot.generatedAt,
    dataUpdatedAt: snapshot.dataUpdatedAt,
    revision: snapshot.revision,
    renderedSubjectLabel: subjectLabel(snapshot),
  };
  const prior = await readPriorMetadata(`${options.output}.json`);
  if (prior?.revision === metadata.revision &&
      prior.renderedSubjectLabel === metadata.renderedSubjectLabel &&
      await fileExists(options.output)) {
    return { ...metadata, status: "unchanged revision" };
  }
  await writeFile(options.output, htmlFor(snapshot), "utf8");
  await writeFile(`${options.output}.json`, `${JSON.stringify(metadata, null, 2)}\n`, "utf8");
  return { ...metadata, status: "written" };
}

function parseArguments(args: string[]): Options {
  const values = new Map<string, string>();
  const environmentIDs: string[] = [];
  const allowed = new Set(["--endpoint", "--subject", "--from", "--to", "--timezone", "--environment-id", "--output"]);
  for (let index = 0; index < args.length; index += 2) {
    const flag = args[index];
    const value = args[index + 1];
    if (!flag || !allowed.has(flag) || !value || value.startsWith("--")) {
      throw new Error("Usage: --endpoint URL --subject HANDLE --from YYYY-MM-DD --to YYYY-MM-DD --timezone ZONE --output FILE [--environment-id ID]...");
    }
    if (flag === "--environment-id") environmentIDs.push(value);
    else values.set(flag, value);
  }
  for (const flag of ["--endpoint", "--subject", "--from", "--to", "--timezone", "--output"]) {
    if (!values.has(flag)) throw new Error(`Missing required ${flag}`);
  }
  return {
    endpoint: values.get("--endpoint")!,
    subject: values.get("--subject")!,
    from: values.get("--from")!,
    to: values.get("--to")!,
    timezone: values.get("--timezone")!,
    environmentIDs,
    output: values.get("--output")!,
  };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  renderToFiles(parseArguments(process.argv.slice(2)))
    .then((result) => {
      console.log(`${result.status}: revision=${result.revision} generatedAt=${result.generatedAt} dataUpdatedAt=${result.dataUpdatedAt ?? "null"}`);
    })
    .catch(() => {
      console.error("Static GraphQL render failed.");
      process.exitCode = 1;
    });
}

#!/usr/bin/env node

import { dirname, join } from "node:path";
import { pathToFileURL } from "node:url";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const packageRoot = dirname(require.resolve("openapi-typescript/package.json"));

await import(pathToFileURL(join(packageRoot, "bin/cli.js")).href);

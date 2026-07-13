import { writeFile } from "node:fs/promises";

await writeFile(new URL("../../internal/webui/dist/.keep", import.meta.url), "");

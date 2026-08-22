// Every client module has to at least *link*.
//
// This exists because of a bug that shipped and took the whole dashboard with
// it: `cluster.js` imports `set as remember` from the store, and a new
// top-level `function remember()` beside it is not a shadow — it is
// "Identifier 'remember' has already been declared", a module that never
// evaluates, and every page this file drives silently doing nothing.
//
// `node --check` cannot see it: it parses one file and resolves no imports. The
// browser only says so in a console nobody has open. So each module is imported
// here, where linking is what we are testing — the evaluation that follows will
// fail on `document`, and that failure is expected and ignored. Anything that
// is a SyntaxError is not.
import { readdir } from "node:fs/promises";
import { pathToFileURL } from "node:url";

const here = new URL(".", import.meta.url);
const files = (await readdir(here))
  .filter((name) => name.endsWith(".js"))
  .sort();

let failures = 0;
for (const name of files) {
  try {
    await import(pathToFileURL(new URL(name, here).pathname).href);
  } catch (error) {
    // A module that links and then reaches for a browser is doing exactly what
    // it is supposed to do in a browser. Only a link-time error is a bug here.
    const linkError = error instanceof SyntaxError ||
      /has already been declared|does not provide an export|Cannot find module/.test(error.message || "");
    if (linkError) {
      console.error(`FAIL ${name}: ${error.message}`);
      failures++;
    }
  }
}
if (failures) {
  console.error(`${failures} module${failures === 1 ? "" : "s"} would not link.`);
  process.exit(1);
}
console.log(`ok — ${files.length} client modules link`);

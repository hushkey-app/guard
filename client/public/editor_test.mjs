// The compose editor's two halves that are worth asserting rather than
// eyeballing: what the tokeniser decides a line is, and what the line
// transforms behind Tab and Cmd+/ do to a selection.
//
// The highlighting is a courtesy — a wrong colour costs nothing. The reason it
// is tested anyway is that this file's input is a docker compose file, which
// contains the two constructs that break every naive YAML highlighter: a value
// with a colon in it (`image: registry:5000/app`) and a shell script inside a
// block scalar. Both used to render as something the reader would have to
// distrust the rest of the file over.
const editor = await import("./editor.js");

let failures = 0;
function check(ok, label, detail) {
  if (ok) return;
  failures++;
  console.error(`FAIL ${label}`, detail === undefined ? "" : detail);
}

// The text of one line, with the token class of each piece — what the reader
// actually sees, rather than the exact HTML.
function tokens(line) {
  const html = editor.paintYAML(line).replace(/\n$/, "");
  const out = [];
  const scan = /<span class="tok-([a-z]+)">([^<]*)<\/span>|([^<]+)/g;
  for (let hit = scan.exec(html); hit; hit = scan.exec(html)) {
    if (hit[3] !== undefined) { if (hit[3].trim()) out.push(["bare", hit[3]]); continue; }
    if (hit[2].trim() || hit[1] === "punct") out.push([hit[1], hit[2]]);
  }
  return out;
}
const kindOf = (line, text) => (tokens(line).find((t) => t[1] === text) || ["missing"])[0];

// 1. A key, and a value whose colon is not one. This is the whole reason the
//    key rule is "the first colon followed by whitespace" — the registry
//    reference below is the line people care most about reading correctly.
check(kindOf("  image: syd.vultrcr.com/hushkey/pack:${TAG}", "image") === "key", "image is a key");
check(kindOf("  image: syd.vultrcr.com/hushkey/pack:${TAG}", "${TAG}") === "var", "${TAG} is a variable");
check(
  tokens("  image: syd.vultrcr.com/hushkey/pack:${TAG}").some((t) => t[0] === "plain" && t[1].includes(":")),
  "the tag's own colon stays inside the value",
  tokens("  image: syd.vultrcr.com/hushkey/pack:${TAG}"),
);

// 2. A shell heredoc inside a block scalar is text, not YAML. Without this,
//    `pack.hushkey.app:80 {` from the caddy service renders as a key and the
//    highlighting is confidently wrong about the one part of the file that is
//    not YAML at all.
const heredoc = [
  "    command:",
  "      - /bin/sh",
  "      - -c",
  "      - |",
  "        pack.hushkey.app:80 {",
  "            reverse_proxy app:8000",
  "        }",
  "  volumes:",
].join("\n");
const painted = editor.paintYAML(heredoc).split("\n");
check(/tok-string/.test(painted[4]) && !/tok-key/.test(painted[4]), "a block scalar's body is not parsed as YAML", painted[4]);
check(/tok-string/.test(painted[6]), "the block runs to its last indented line", painted[6]);
check(/tok-key/.test(painted[7]), "and ends when the indent comes back", painted[7]);

// 3. An inline comment is one; a `#` inside a token is not.
check(kindOf("  DENO_APP_URL: ${X}   # the public origin", "# the public origin") === "comment", "an inline comment");
check(kindOf('  image: registry/app:sha#2', "# 2") === "missing", "a # mid-token opens no comment");
check(kindOf('  test: ["CMD", "wget"]', '"CMD"') !== "comment", "a quoted string is not a comment");

// 4. The scalar kinds, which is all the colour is for.
check(kindOf("  port: 5432", "5432") === "number", "a number");
check(kindOf("  restart: true", "true") === "const", "a boolean");
check(kindOf('  name: "80:80"', '"80:80"') === "string", "a quoted string, colon and all");
check(kindOf("      - \"80:80\"", "-") === "punct", "a sequence marker");

// 5. Markup in a value cannot become markup in the page. The compose file is
//    typed by an admin rather than a stranger, but it is also restored from a
//    backup file, and innerHTML is innerHTML.
check(!editor.paintYAML('  command: <img src=x onerror=alert(1)>').includes("<img"), "a value is escaped");

// ------------------------------------------------------------ the transforms

const eq = (a, b) => JSON.stringify(a) === JSON.stringify(b);

// 6. Commenting goes in at the block's shallowest indent, so a service
//    commented out of a compose file keeps its shape.
check(
  eq(editor.commentBlock(["  redis:", "    image: redis:7", "      restart: always"]),
     ["  # redis:", "  #   image: redis:7", "  #     restart: always"]),
  "comment at the shallowest indent",
  editor.commentBlock(["  redis:", "    image: redis:7", "      restart: always"]),
);

// 7. And comes back out. A round trip has to be the identity or the shortcut
//    is a one-way door.
const original = ["  redis:", "    image: redis:7", "", "      restart: always"];
check(eq(editor.commentBlock(editor.commentBlock(original)), original), "comment then uncomment is the identity",
  editor.commentBlock(editor.commentBlock(original)));

// 8. A blank line in the middle stays blank: a lone `#` is noise nobody typed.
check(eq(editor.commentBlock(["a", "", "b"]), ["# a", "", "# b"]), "a blank line is left alone");

// 9. Half-commented is commented, not uncommented — otherwise selecting a
//    block with one comment in it strips that comment instead of adding any.
check(eq(editor.commentBlock(["# a", "b"]), ["# # a", "# b"]), "a partly commented block is commented");

// 10. Indent and outdent. Outdenting a line with nothing to give back is not
//     an error, it is a no-op, because a selection is usually ragged.
check(eq(editor.shiftBlock(["a", "  b", ""], false), ["  a", "    b", ""]), "indent skips blank lines");
check(eq(editor.shiftBlock(["  a", " b", "c"], true), ["a", "b", "c"]), "outdent takes what is there");

// ------------------------------------------------------------ the formatter

// 11. Format touches whitespace and nothing else. The comments in a compose
//     file are where the deployment notes live, so a serialiser round trip
//     would be a data loss dressed as a tidy-up.
const messy = "\n\nservices:\n\tapp:\n    image: x   \n\n\n\n  # a note\n\n";
const tidy = editor.formatYAML(messy);
check(tidy === "services:\n  app:\n    image: x\n\n  # a note\n", "format normalises whitespace only", JSON.stringify(tidy));
check(editor.formatYAML(tidy) === tidy, "format is idempotent");
check(tidy.includes("# a note"), "format keeps comments");
check(editor.formatYAML("") === "", "an empty file formats to empty");

if (failures) {
  console.error(`${failures} editor assertion${failures === 1 ? "" : "s"} failed.`);
  process.exit(1);
}
console.log("ok — compose editor");

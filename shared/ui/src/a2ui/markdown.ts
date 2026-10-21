// The small Markdown the A2UI Text component allows, drawn as DOM, never as an
// HTML string: headings (#), paragraphs, bold, italic, `code`, code blocks,
// bulleted and numbered lists, and links. Text from a model is untrusted, so
// nothing here parses or inserts HTML: `<script>` is plain characters, an image
// is shown as its alt text, and a link is a real link only for http and https
// (opened in a new tab with no opener and no referrer); any other address
// leaves the link text unlinked.
import { html, type TemplateResult } from "lit";

type Part = TemplateResult | string;

const MAX_DEPTH = 6;

const safeHref = (raw: string) => {
  try {
    const url = new URL(raw.trim());
    return url.protocol === "https:" || url.protocol === "http:" ? url.href : undefined;
  } catch {
    return undefined;
  }
};

const isWord = (ch: string | undefined) => ch !== undefined && /[\p{L}\p{N}]/u.test(ch);

// Inline spans of one line or paragraph.
export function inline(text: string, depth = 0): Part[] {
  const out: Part[] = [];
  let plain = "";
  const flush = () => {
    if (plain) out.push(plain);
    plain = "";
  };
  let i = 0;
  while (i < text.length) {
    const ch = text[i];
    if (ch === "\\" && i + 1 < text.length && /[\\`*_[\]()#+\-.!>~|{}]/.test(text[i + 1])) {
      plain += text[i + 1];
      i += 2;
      continue;
    }
    if (ch === "`") {
      const end = text.indexOf("`", i + 1);
      if (end > i + 1) {
        flush();
        out.push(html`<code>${text.slice(i + 1, end)}</code>`);
        i = end + 1;
        continue;
      }
    }
    if (depth < MAX_DEPTH && (text.startsWith("**", i) || text.startsWith("__", i))) {
      const mark = text.slice(i, i + 2);
      const end = text.indexOf(mark, i + 2);
      if (end > i + 2 && !(mark === "__" && (isWord(text[i - 1]) || isWord(text[end + 2])))) {
        flush();
        out.push(html`<strong>${inline(text.slice(i + 2, end), depth + 1)}</strong>`);
        i = end + 2;
        continue;
      }
    }
    if (depth < MAX_DEPTH && (ch === "*" || ch === "_") && text[i + 1] !== ch && text[i + 1] !== " ") {
      const end = text.indexOf(ch, i + 1);
      if (end > i + 1 && text[end - 1] !== " " && !(ch === "_" && (isWord(text[i - 1]) || isWord(text[end + 1])))) {
        flush();
        out.push(html`<em>${inline(text.slice(i + 1, end), depth + 1)}</em>`);
        i = end + 1;
        continue;
      }
    }
    const isImage = ch === "!" && text[i + 1] === "[";
    if ((ch === "[" || isImage) && depth < MAX_DEPTH) {
      const open = isImage ? i + 1 : i;
      const close = text.indexOf("]", open + 1);
      if (close > open && text[close + 1] === "(") {
        const end = text.indexOf(")", close + 2);
        if (end > close + 1) {
          const label = text.slice(open + 1, close);
          const href = safeHref(text.slice(close + 2, end));
          flush();
          if (isImage) out.push(label); // images are not drawn; the alt text stands in
          else if (href) out.push(html`<a href=${href} target="_blank" rel="noopener noreferrer">${inline(label, depth + 1)}</a>`);
          else out.push(...inline(label, depth + 1));
          i = end + 1;
          continue;
        }
      }
    }
    plain += ch;
    i++;
  }
  flush();
  return out;
}

// A paragraph keeps its own line breaks.
const withBreaks = (block: string[]): Part[] =>
  block.flatMap((line, index) => (index === 0 ? inline(line) : [html`<br />`, ...inline(line)]));

export function renderMarkdown(source: string): TemplateResult {
  const rows = source.replace(/\r\n?/g, "\n").split("\n");
  const blocks: TemplateResult[] = [];
  let paragraph: string[] = [];
  const endParagraph = () => {
    if (paragraph.length) blocks.push(html`<p>${withBreaks(paragraph)}</p>`);
    paragraph = [];
  };
  for (let i = 0; i < rows.length; i++) {
    const row = rows[i];
    if (/^\s*$/.test(row)) {
      endParagraph();
      continue;
    }
    const fence = /^\s*```/.exec(row);
    if (fence) {
      endParagraph();
      const code: string[] = [];
      for (i++; i < rows.length && !/^\s*```/.test(rows[i]); i++) code.push(rows[i]);
      blocks.push(html`<pre><code>${code.join("\n")}</code></pre>`);
      continue;
    }
    const heading = /^(#{1,6})\s+(.*?)\s*#*\s*$/.exec(row);
    if (heading) {
      endParagraph();
      const level = heading[1].length;
      const content = inline(heading[2]);
      blocks.push(
        [html`<h1>${content}</h1>`, html`<h2>${content}</h2>`, html`<h3>${content}</h3>`, html`<h4>${content}</h4>`, html`<h5>${content}</h5>`, html`<h6>${content}</h6>`][level - 1],
      );
      continue;
    }
    const bullet = /^\s*[-*+]\s+(.*)$/.exec(row);
    const numbered = /^\s*\d{1,9}[.)]\s+(.*)$/.exec(row);
    if (bullet || numbered) {
      endParagraph();
      const items: Part[][] = [];
      const pattern = bullet ? /^\s*[-*+]\s+(.*)$/ : /^\s*\d{1,9}[.)]\s+(.*)$/;
      for (; i < rows.length; i++) {
        const match = pattern.exec(rows[i]);
        if (!match) break;
        items.push(inline(match[1]));
      }
      i--;
      const lis = items.map((item) => html`<li>${item}</li>`);
      blocks.push(bullet ? html`<ul>${lis}</ul>` : html`<ol>${lis}</ol>`);
      continue;
    }
    paragraph.push(row.trim());
  }
  endParagraph();
  return html`${blocks}`;
}

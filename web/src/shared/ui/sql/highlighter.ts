import { tagHighlighter, tags } from "@lezer/highlight";

/**
 * Maps syntax tags to class names styled in sql-code.css with the --syntax-*
 * tokens. Class based, so the same rules serve the editor and the read-only
 * viewer, in light and dark (the tokens are light-dark() pairs).
 */
export const sqlHighlighter = tagHighlighter([
  { tag: [tags.keyword, tags.operatorKeyword, tags.modifier], class: "sx-keyword" },
  { tag: [tags.string, tags.special(tags.string)], class: "sx-string" },
  { tag: [tags.number, tags.bool, tags.null], class: "sx-number" },
  { tag: [tags.typeName, tags.standard(tags.name)], class: "sx-type" },
  { tag: [tags.function(tags.variableName), tags.special(tags.name)], class: "sx-function" },
  { tag: [tags.lineComment, tags.blockComment, tags.comment], class: "sx-comment" },
  { tag: [tags.punctuation, tags.paren, tags.squareBracket, tags.brace, tags.separator], class: "sx-punctuation" },
]);

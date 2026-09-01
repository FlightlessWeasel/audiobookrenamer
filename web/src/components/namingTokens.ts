/**
 * The template tokens internal/organize/template.go understands. The naming
 * guide documents them and the template fields insert them, so both read from
 * this one list — keep it in step with the renderer.
 */
export interface NamingToken {
  token: string;
  meaning: string;
  example: string;
  /** Only meaningful for a book split across several files. */
  trackOnly?: boolean;
}

export const NAMING_TOKENS: NamingToken[] = [
  { token: "{title}", meaning: "Book title", example: "The Final Empire" },
  { token: "{subtitle}", meaning: "Subtitle, when the provider gave one", example: "Mistborn Book One" },
  { token: "{author}", meaning: "Author as displayed", example: "Brandon Sanderson" },
  {
    token: "{author_sort}",
    meaning: "Author in sort order; falls back to {author} when unknown",
    example: "Sanderson, Brandon",
  },
  { token: "{series}", meaning: "Series name; empty for a standalone book", example: "Mistborn" },
  { token: "{series_index}", meaning: "Position in the series", example: "1" },
  { token: "{year}", meaning: "Publication year; empty when unknown", example: "2006" },
  { token: "{narrator}", meaning: "Narrator", example: "Michael Kramer" },
  { token: "{asin}", meaning: "Audible ASIN", example: "B002UZMLXM" },
  { token: "{isbn}", meaning: "ISBN, 13-digit when available", example: "9780765311788" },
  { token: "{ext}", meaning: "File extension, lowercase, with the dot", example: ".m4b" },
  { token: "{track}", meaning: "Track number, unpadded", example: "7", trackOnly: true },
  { token: "{track2}", meaning: "Track number padded to 2 digits", example: "07", trackOnly: true },
  { token: "{track3}", meaning: "Track number padded to 3 digits", example: "007", trackOnly: true },
];

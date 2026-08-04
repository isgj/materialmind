import hljs from 'highlight.js/lib/core';
import bash from 'highlight.js/lib/languages/bash';
import c from 'highlight.js/lib/languages/c';
import cpp from 'highlight.js/lib/languages/cpp';
import csharp from 'highlight.js/lib/languages/csharp';
import css from 'highlight.js/lib/languages/css';
import go from 'highlight.js/lib/languages/go';
import java from 'highlight.js/lib/languages/java';
import javascript from 'highlight.js/lib/languages/javascript';
import json from 'highlight.js/lib/languages/json';
import kotlin from 'highlight.js/lib/languages/kotlin';
import makefile from 'highlight.js/lib/languages/makefile';
import markdown from 'highlight.js/lib/languages/markdown';
import python from 'highlight.js/lib/languages/python';
import ruby from 'highlight.js/lib/languages/ruby';
import rust from 'highlight.js/lib/languages/rust';
import scss from 'highlight.js/lib/languages/scss';
import sql from 'highlight.js/lib/languages/sql';
import typescript from 'highlight.js/lib/languages/typescript';
import xml from 'highlight.js/lib/languages/xml';
import yaml from 'highlight.js/lib/languages/yaml';

const languages = {
  bash,
  c,
  cpp,
  csharp,
  css,
  go,
  java,
  javascript,
  json,
  kotlin,
  makefile,
  markdown,
  python,
  ruby,
  rust,
  scss,
  sql,
  typescript,
  xml,
  yaml,
} as const;

for (const [name, definition] of Object.entries(languages)) {
  hljs.registerLanguage(name, definition);
}

const filenameLanguages: Readonly<Record<string, keyof typeof languages>> = {
  gnumakefile: 'makefile',
  makefile: 'makefile',
};

const extensionLanguages: Readonly<Record<string, keyof typeof languages>> = {
  bash: 'bash',
  c: 'c',
  cc: 'cpp',
  cjs: 'javascript',
  cpp: 'cpp',
  cs: 'csharp',
  css: 'css',
  cts: 'typescript',
  cxx: 'cpp',
  go: 'go',
  h: 'c',
  hh: 'cpp',
  hpp: 'cpp',
  htm: 'xml',
  html: 'xml',
  hxx: 'cpp',
  java: 'java',
  js: 'javascript',
  json: 'json',
  jsonc: 'json',
  jsx: 'javascript',
  kt: 'kotlin',
  kts: 'kotlin',
  markdown: 'markdown',
  md: 'markdown',
  mjs: 'javascript',
  mk: 'makefile',
  mts: 'typescript',
  py: 'python',
  pyw: 'python',
  rb: 'ruby',
  rs: 'rust',
  scss: 'scss',
  sh: 'bash',
  sql: 'sql',
  svg: 'xml',
  ts: 'typescript',
  tsx: 'typescript',
  xml: 'xml',
  yaml: 'yaml',
  yml: 'yaml',
  zsh: 'bash',
};

export type SyntaxLanguage = keyof typeof languages;

export function syntaxLanguageForPath(filePath: string): SyntaxLanguage | null {
  const basename = filePath.replaceAll('\\', '/').split('/').at(-1)?.toLowerCase() ?? '';
  const filenameLanguage = filenameLanguages[basename];
  if (filenameLanguage) {
    return filenameLanguage;
  }
  const extension = basename.includes('.') ? (basename.split('.').at(-1) ?? '') : '';
  return extensionLanguages[extension] ?? null;
}

export function highlightCode(code: string, language: SyntaxLanguage): string | null {
  try {
    return hljs.highlight(code, { language, ignoreIllegals: true }).value;
  } catch {
    return null;
  }
}

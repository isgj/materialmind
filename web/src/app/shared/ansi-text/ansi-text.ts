import Anser from 'anser';

export interface AnsiTextSegment {
  text: string;
  foreground: string | null;
  background: string | null;
  bold: boolean;
  dim: boolean;
  italic: boolean;
  hidden: boolean;
  textDecoration: string | null;
}

const ansiColorVariables: Readonly<Record<string, string>> = {
  'ansi-black': '--ansi-black',
  'ansi-red': '--ansi-red',
  'ansi-green': '--ansi-green',
  'ansi-yellow': '--ansi-yellow',
  'ansi-blue': '--ansi-blue',
  'ansi-magenta': '--ansi-magenta',
  'ansi-cyan': '--ansi-cyan',
  'ansi-white': '--ansi-white',
  'ansi-bright-black': '--ansi-bright-black',
  'ansi-bright-red': '--ansi-bright-red',
  'ansi-bright-green': '--ansi-bright-green',
  'ansi-bright-yellow': '--ansi-bright-yellow',
  'ansi-bright-blue': '--ansi-bright-blue',
  'ansi-bright-magenta': '--ansi-bright-magenta',
  'ansi-bright-cyan': '--ansi-bright-cyan',
  'ansi-bright-white': '--ansi-bright-white',
};

const standardPalette = [
  'ansi-black',
  'ansi-red',
  'ansi-green',
  'ansi-yellow',
  'ansi-blue',
  'ansi-magenta',
  'ansi-cyan',
  'ansi-white',
  'ansi-bright-black',
  'ansi-bright-red',
  'ansi-bright-green',
  'ansi-bright-yellow',
  'ansi-bright-blue',
  'ansi-bright-magenta',
  'ansi-bright-cyan',
  'ansi-bright-white',
] as const;

const colorCubeLevels = [0, 95, 135, 175, 215, 255] as const;
const stringControlSequencePattern =
  /\u001b(?:\][\s\S]*?(?:\u0007|\u001b\\)|[P^_][\s\S]*?\u001b\\)/g;
const unfinishedStringControlSequencePattern = /\u001b(?:\]|[P^_])[\s\S]*$/;
const unfinishedCsiPattern = /\u001b\[[0-?]*[ -/]*$/;

export function parseAnsiText(value: string): readonly AnsiTextSegment[] {
  const entries = Anser.ansiToJson(stripUnsupportedControlSequences(value), {
    remove_empty: true,
    use_classes: true,
  });

  return entries.map((entry) => {
    const decorations = new Set(entry.decorations);
    const textDecorations = [
      decorations.has('underline') ? 'underline' : '',
      decorations.has('strikethrough') ? 'line-through' : '',
    ].filter(Boolean);

    return {
      text: entry.content,
      foreground: resolveAnsiColor(entry.fg || null, entry.fg_truecolor || null),
      background: resolveAnsiColor(entry.bg || null, entry.bg_truecolor || null),
      bold: decorations.has('bold'),
      dim: decorations.has('dim'),
      italic: decorations.has('italic'),
      hidden: decorations.has('hidden'),
      textDecoration: textDecorations.length > 0 ? textDecorations.join(' ') : null,
    };
  });
}

function stripUnsupportedControlSequences(value: string): string {
  return value
    .replace(stringControlSequencePattern, '')
    .replace(unfinishedStringControlSequencePattern, '')
    .replace(unfinishedCsiPattern, '');
}

function resolveAnsiColor(color: string | null, trueColor: string | null): string | null {
  if (!color) {
    return null;
  }
  if (color === 'ansi-truecolor') {
    return trueColor ? rgbColor(trueColor) : null;
  }

  const variable = ansiColorVariables[color];
  if (variable) {
    return `var(${variable})`;
  }

  const paletteMatch = /^ansi-palette-(\d{1,3})$/.exec(color);
  if (paletteMatch) {
    return paletteColor(Number(paletteMatch[1]));
  }

  return rgbColor(color);
}

function paletteColor(index: number): string | null {
  if (!Number.isInteger(index) || index < 0 || index > 255) {
    return null;
  }
  if (index < standardPalette.length) {
    return `var(${ansiColorVariables[standardPalette[index]]})`;
  }
  if (index < 232) {
    const offset = index - 16;
    const red = colorCubeLevels[Math.floor(offset / 36)];
    const green = colorCubeLevels[Math.floor((offset % 36) / 6)];
    const blue = colorCubeLevels[offset % 6];
    return `rgb(${red} ${green} ${blue})`;
  }

  const gray = 8 + (index - 232) * 10;
  return `rgb(${gray} ${gray} ${gray})`;
}

function rgbColor(value: string): string | null {
  const channels = value.split(',').map((channel) => Number(channel.trim()));
  if (
    channels.length !== 3 ||
    channels.some((channel) => !Number.isInteger(channel) || channel < 0 || channel > 255)
  ) {
    return null;
  }
  return `rgb(${channels.join(' ')})`;
}

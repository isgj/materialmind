import { Component, computed, effect, input, output, signal, untracked } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatCheckboxChange, MatCheckboxModule } from '@angular/material/checkbox';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatIconModule } from '@angular/material/icon';
import { MatInputModule } from '@angular/material/input';
import { MatProgressSpinnerModule } from '@angular/material/progress-spinner';
import { MatSelectChange, MatSelectModule } from '@angular/material/select';

import { MCPElicitationState, MCPElicitationSubmission } from './mcp-elicitation.models';

interface ElicitationField {
  name: string;
  label: string;
  description: string;
  kind: 'string' | 'number' | 'integer' | 'boolean' | 'enum' | 'multi-enum';
  required: boolean;
  options: readonly { label: string; value: unknown }[];
  minimum?: number;
  maximum?: number;
  minLength?: number;
  maxLength?: number;
  minItems?: number;
  maxItems?: number;
  pattern?: string;
  inputType: string;
  defaultValue?: unknown;
}

@Component({
  selector: 'app-mcp-elicitation',
  imports: [
    MatButtonModule,
    MatCheckboxModule,
    MatFormFieldModule,
    MatIconModule,
    MatInputModule,
    MatProgressSpinnerModule,
    MatSelectModule,
  ],
  templateUrl: './mcp-elicitation.component.html',
  styleUrl: './mcp-elicitation.component.scss',
})
export class MCPElicitationComponent {
  readonly request = input.required<MCPElicitationState>();
  readonly submitted = output<MCPElicitationSubmission>();

  protected readonly fields = computed(() => elicitationFields(this.request().requestedSchema));
  protected readonly values = signal<Record<string, unknown>>({});
  protected readonly canAccept = computed(
    () =>
      this.request().status === 'pending' &&
      this.fields().every((field) => validFieldValue(field, this.values()[field.name])),
  );

  private activeRequestId = '';

  constructor() {
    effect(() => {
      const request = this.request();
      const fields = this.fields();
      if (request.id === this.activeRequestId) {
        return;
      }
      this.activeRequestId = request.id;
      untracked(() => {
        this.values.set(
          Object.fromEntries(
            fields.flatMap((field) => {
              if (field.defaultValue !== undefined) {
                return [[field.name, field.defaultValue]];
              }
              return field.kind === 'boolean' ? [[field.name, false]] : [];
            }),
          ),
        );
      });
    });
  }

  protected textValue(field: ElicitationField): string {
    const value = this.values()[field.name];
    return value === undefined || value === null ? '' : String(value);
  }

  protected booleanValue(field: ElicitationField): boolean {
    return this.values()[field.name] === true;
  }

  protected enumValue(field: ElicitationField): unknown {
    return this.values()[field.name];
  }

  protected updateText(field: ElicitationField, event: Event): void {
    const rawValue = (event.target as HTMLInputElement).value;
    if (rawValue === '') {
      this.setValue(field.name, undefined);
      return;
    }
    const value = field.kind === 'number' || field.kind === 'integer' ? Number(rawValue) : rawValue;
    this.setValue(field.name, value);
  }

  protected updateBoolean(field: ElicitationField, event: MatCheckboxChange): void {
    this.setValue(field.name, event.checked);
  }

  protected updateEnum(field: ElicitationField, event: MatSelectChange): void {
    this.setValue(field.name, event.value);
  }

  protected submit(action: 'accept' | 'decline' | 'cancel'): void {
    if (this.request().status !== 'pending' || (action === 'accept' && !this.canAccept())) {
      return;
    }
    const content =
      action === 'accept'
        ? Object.fromEntries(
            Object.entries(this.values()).filter(([, value]) => value !== undefined),
          )
        : undefined;
    this.submitted.emit({ id: this.request().id, action, content });
  }

  protected resolutionLabel(): string {
    switch (this.request().resolution?.action) {
      case 'accept':
        return this.request().mode === 'url'
          ? this.request().externalCompleted
            ? 'External flow completed'
            : 'Link opened'
          : 'Response submitted';
      case 'decline':
        return 'Request declined';
      default:
        return 'Request cancelled';
    }
  }

  private setValue(name: string, value: unknown): void {
    this.values.update((current) => ({ ...current, [name]: value }));
  }
}

function elicitationFields(schema: unknown): ElicitationField[] {
  const root = recordValue(schema);
  const properties = recordValue(root?.['properties']);
  const required = new Set(stringValues(root?.['required']));
  if (!properties) {
    return [];
  }
  return Object.entries(properties).flatMap(([name, rawProperty]) => {
    const property = recordValue(rawProperty);
    if (!property) {
      return [];
    }
    const type = typeof property['type'] === 'string' ? property['type'] : 'string';
    const options = elicitationOptions(property, type);
    const kind: ElicitationField['kind'] =
      type === 'array' && options.length > 0
        ? 'multi-enum'
        : options.length > 0
          ? 'enum'
          : type === 'boolean' || type === 'number' || type === 'integer'
            ? type
            : 'string';
    return [
      {
        name,
        label: stringValue(property['title']) || name,
        description: stringValue(property['description']),
        kind,
        required: required.has(name),
        options,
        minimum: finiteNumber(property['minimum']),
        maximum: finiteNumber(property['maximum']),
        minLength: finiteNumber(property['minLength']),
        maxLength: finiteNumber(property['maxLength']),
        minItems: finiteNumber(property['minItems']),
        maxItems: finiteNumber(property['maxItems']),
        pattern: stringValue(property['pattern']) || undefined,
        inputType: elicitationInputType(stringValue(property['format'])),
        defaultValue: property['default'],
      },
    ];
  });
}

function validFieldValue(field: ElicitationField, value: unknown): boolean {
  if (value === undefined || value === null || value === '') {
    return !field.required;
  }
  if (field.kind === 'boolean') {
    return typeof value === 'boolean';
  }
  if (field.kind === 'number' || field.kind === 'integer') {
    return (
      typeof value === 'number' &&
      Number.isFinite(value) &&
      (field.kind !== 'integer' || Number.isInteger(value)) &&
      (field.minimum === undefined || value >= field.minimum) &&
      (field.maximum === undefined || value <= field.maximum)
    );
  }
  if (field.kind === 'enum') {
    return field.options.some((option) => Object.is(option.value, value));
  }
  if (field.kind === 'multi-enum') {
    return (
      Array.isArray(value) &&
      (field.minItems === undefined || value.length >= field.minItems) &&
      (field.maxItems === undefined || value.length <= field.maxItems) &&
      value.every((item) => field.options.some((option) => Object.is(option.value, item)))
    );
  }
  return (
    typeof value === 'string' &&
    (field.minLength === undefined || value.length >= field.minLength) &&
    (field.maxLength === undefined || value.length <= field.maxLength) &&
    matchesPattern(value, field.pattern)
  );
}

function elicitationOptions(
  property: Record<string, unknown>,
  type: string,
): { label: string; value: unknown }[] {
  const source = type === 'array' ? recordValue(property['items']) : property;
  if (!source) {
    return [];
  }
  const titled = Array.isArray(source['oneOf'])
    ? source['oneOf']
    : Array.isArray(source['anyOf'])
      ? source['anyOf']
      : [];
  if (titled.length > 0) {
    return titled.flatMap((rawOption) => {
      const option = recordValue(rawOption);
      const value = option?.['const'];
      return typeof value === 'string'
        ? [{ value, label: stringValue(option?.['title']) || value }]
        : [];
    });
  }
  const values = Array.isArray(source['enum']) ? source['enum'] : [];
  const labels = stringValues(source['enumNames']);
  return values.map((value, index) => ({ value, label: labels[index] || String(value) }));
}

function elicitationInputType(format: string): string {
  switch (format) {
    case 'email':
      return 'email';
    case 'uri':
      return 'url';
    case 'date':
      return 'date';
    case 'date-time':
      return 'datetime-local';
    default:
      return 'text';
  }
}

function matchesPattern(value: string, pattern: string | undefined): boolean {
  if (!pattern) {
    return true;
  }
  try {
    return new RegExp(pattern).test(value);
  } catch {
    return false;
  }
}

function recordValue(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

function stringValues(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === 'string')
    : [];
}

function finiteNumber(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined;
}

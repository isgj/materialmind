export type ToolApprovalStatus = 'pending' | 'submitting' | 'approved' | 'executing' | 'denied';
export type FileChangeOperation = 'create' | 'update' | 'delete';

export interface FileChangePreview {
  operation: FileChangeOperation;
  path: string;
  diff: string;
}

export interface ToolApprovalState {
  id: string;
  status: ToolApprovalStatus;
  reason?: string;
  options?: readonly ToolApprovalOption[];
}

export interface ToolApprovalDecision {
  id: string;
  approved: boolean;
  reason: string;
  optionId?: string;
}
import { ToolApprovalOption } from '../../core/models';

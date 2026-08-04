import {
  MCPElicitationAction,
  MCPElicitationResolution,
  StreamMCPElicitationRequest,
} from '../../core/models';

export type MCPElicitationStatus = 'pending' | 'submitting' | 'resolved';

export interface MCPElicitationState extends StreamMCPElicitationRequest {
  status: MCPElicitationStatus;
  resolution?: MCPElicitationResolution;
  externalCompleted?: boolean;
}

export interface MCPElicitationSubmission {
  id: string;
  action: MCPElicitationAction;
  content?: Record<string, unknown>;
}

import {
  StreamUserInputRequest,
  UserInputAnswer,
  UserInputAnswerSubmission,
} from '../../core/models';

export type UserInputStatus = 'pending' | 'submitting' | 'answered';

export interface UserInputState extends StreamUserInputRequest {
  status: UserInputStatus;
  answers?: readonly UserInputAnswer[];
}

export interface UserInputSubmission {
  id: string;
  answers: UserInputAnswerSubmission[];
}
